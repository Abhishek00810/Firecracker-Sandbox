package ideauth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	Audience           = "sandbox-ide"
	DefaultHandoffTTL  = 60 * time.Second
	MaximumHandoffTTL  = 2 * time.Minute
	DefaultSessionTTL  = time.Hour
	MaximumSessionLife = 8 * time.Hour
)

var ErrInvalidToken = errors.New("invalid IDE token")

type Kind string

const (
	KindHandoff Kind = "handoff"
	KindSession Kind = "session"
)

type Claims struct {
	Audience       string `json:"aud"`
	Kind           Kind   `json:"kind"`
	SandboxID      string `json:"sid"`
	UserID         string `json:"uid"`
	OrganizationID string `json:"oid,omitempty"`
	Role           string `json:"role"`
	Port           uint16 `json:"port"`
	Nonce          string `json:"nonce,omitempty"`
	IssuedAt       int64  `json:"iat"`
	ExpiresAt      int64  `json:"exp"`
	AbsoluteExpiry int64  `json:"abs_exp"`
}

type NonceStore interface {
	Consume(context.Context, string, time.Time) (bool, error)
}

type Signer struct {
	key []byte
	now func() time.Time
}

func NewSigner(secret string) (*Signer, error) {
	secret = strings.TrimSpace(secret)
	if len(secret) < 32 {
		return nil, errors.New("IDE signing secret must be at least 32 bytes")
	}
	return &Signer{key: []byte(secret), now: time.Now}, nil
}

func DeriveSigningSecret(workerToken string) string {
	sum := sha256.Sum256([]byte("renderops-ide-v1\x00" + workerToken))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (s *Signer) IssueHandoff(sandboxID, userID, organizationID, role string, port uint16, ttl time.Duration) (string, Claims, error) {
	if ttl <= 0 || ttl > MaximumHandoffTTL {
		return "", Claims{}, fmt.Errorf("IDE handoff ttl must be between 1 second and %s", MaximumHandoffTTL)
	}
	now := s.now().UTC()
	claims := Claims{
		Audience: Audience, Kind: KindHandoff, SandboxID: strings.TrimSpace(sandboxID),
		UserID: strings.TrimSpace(userID), OrganizationID: strings.TrimSpace(organizationID),
		Role: strings.TrimSpace(role), Port: port, Nonce: uuid.NewString(),
		IssuedAt: now.Unix(), ExpiresAt: now.Add(ttl).Unix(),
		AbsoluteExpiry: now.Add(MaximumSessionLife).Unix(),
	}
	if err := validateClaims(claims); err != nil {
		return "", Claims{}, err
	}
	token, err := s.sign(claims)
	return token, claims, err
}

func (s *Signer) Redeem(ctx context.Context, token, sandboxID string, port uint16, nonces NonceStore) (string, Claims, error) {
	if nonces == nil {
		return "", Claims{}, errors.New("IDE nonce store is required")
	}
	claims, err := s.verify(token, KindHandoff, sandboxID, port)
	if err != nil {
		return "", Claims{}, err
	}
	consumed, err := nonces.Consume(ctx, claims.Nonce, time.Unix(claims.ExpiresAt, 0))
	if err != nil {
		return "", Claims{}, fmt.Errorf("consume IDE handoff nonce: %w", err)
	}
	if !consumed {
		return "", Claims{}, ErrInvalidToken
	}

	now := s.now().UTC()
	expiresAt := now.Add(DefaultSessionTTL)
	absoluteExpiry := time.Unix(claims.AbsoluteExpiry, 0)
	if expiresAt.After(absoluteExpiry) {
		expiresAt = absoluteExpiry
	}
	claims.Kind = KindSession
	claims.Nonce = ""
	claims.IssuedAt = now.Unix()
	claims.ExpiresAt = expiresAt.Unix()
	sessionToken, err := s.sign(claims)
	return sessionToken, claims, err
}

func (s *Signer) VerifySession(token, sandboxID string, port uint16) (Claims, error) {
	return s.verify(token, KindSession, sandboxID, port)
}

// RenewSession applies the idle timeout again without extending the original
// absolute lifetime. Callers must recheck the user's current authorization first.
func (s *Signer) RenewSession(token, sandboxID string, port uint16) (string, Claims, error) {
	claims, err := s.VerifySession(token, sandboxID, port)
	if err != nil {
		return "", Claims{}, err
	}
	now := s.now().UTC()
	expiresAt := now.Add(DefaultSessionTTL)
	absoluteExpiry := time.Unix(claims.AbsoluteExpiry, 0)
	if expiresAt.After(absoluteExpiry) {
		expiresAt = absoluteExpiry
	}
	if !expiresAt.After(now) {
		return "", Claims{}, ErrInvalidToken
	}
	claims.IssuedAt = now.Unix()
	claims.ExpiresAt = expiresAt.Unix()
	renewed, err := s.sign(claims)
	return renewed, claims, err
}

func (s *Signer) verify(token string, kind Kind, sandboxID string, port uint16) (Claims, error) {
	encoded, signature, ok := strings.Cut(strings.TrimSpace(token), ".")
	if !ok || encoded == "" || signature == "" || !hmac.Equal([]byte(signature), []byte(s.signature(encoded))) {
		return Claims{}, ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var claims Claims
	if json.Unmarshal(payload, &claims) != nil || validateClaims(claims) != nil {
		return Claims{}, ErrInvalidToken
	}
	if claims.Audience != Audience || claims.Kind != kind || claims.SandboxID != sandboxID || claims.Port != port {
		return Claims{}, ErrInvalidToken
	}
	now := s.now().UTC().Unix()
	if claims.ExpiresAt <= now || claims.AbsoluteExpiry <= now || (kind == KindHandoff && claims.Nonce == "") {
		return Claims{}, ErrInvalidToken
	}
	return claims, nil
}

func (s *Signer) sign(claims Claims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + s.signature(encoded), nil
}

func (s *Signer) signature(payload string) string {
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func validateClaims(claims Claims) error {
	if claims.SandboxID == "" || claims.UserID == "" || claims.Role == "" || claims.Port == 0 {
		return errors.New("sandbox id, user id, role, and port are required")
	}
	if claims.Audience != Audience || (claims.Kind != KindHandoff && claims.Kind != KindSession) {
		return ErrInvalidToken
	}
	return nil
}

type MemoryNonceStore struct {
	mu   sync.Mutex
	used map[string]time.Time
	now  func() time.Time
}

func NewMemoryNonceStore() *MemoryNonceStore {
	return &MemoryNonceStore{used: make(map[string]time.Time), now: time.Now}
}

func (s *MemoryNonceStore) Consume(_ context.Context, nonce string, expiresAt time.Time) (bool, error) {
	if strings.TrimSpace(nonce) == "" {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	for value, expiry := range s.used {
		if !expiry.After(now) {
			delete(s.used, value)
		}
	}
	if !expiresAt.After(now) {
		return false, nil
	}
	if _, exists := s.used[nonce]; exists {
		return false, nil
	}
	s.used[nonce] = expiresAt
	return true, nil
}
