package preview

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultTTL = time.Hour
	MaximumTTL = 24 * time.Hour
)

var ErrInvalidToken = errors.New("invalid preview token")

type Claims struct {
	SandboxID string `json:"sid"`
	UserID    string `json:"uid"`
	Port      uint16 `json:"port"`
	ExpiresAt int64  `json:"exp"`
}

type Signer struct {
	key []byte
	now func() time.Time
}

func NewSigner(secret string) (*Signer, error) {
	secret = strings.TrimSpace(secret)
	if len(secret) < 32 {
		return nil, errors.New("preview signing secret must be at least 32 bytes")
	}
	return &Signer{key: []byte(secret), now: time.Now}, nil
}

// DeriveSigningSecret keeps preview tokens cryptographically separated from
// the internal worker-token protocol even when both start from one deployment secret.
func DeriveSigningSecret(workerToken string) string {
	sum := sha256.Sum256([]byte("renderops-preview-v1\x00" + workerToken))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (s *Signer) Sign(sandboxID, userID string, port uint16, ttl time.Duration) (string, time.Time, error) {
	if strings.TrimSpace(sandboxID) == "" || strings.TrimSpace(userID) == "" || port == 0 {
		return "", time.Time{}, errors.New("sandbox id, user id, and port are required")
	}
	if ttl <= 0 || ttl > MaximumTTL {
		return "", time.Time{}, fmt.Errorf("preview token ttl must be between 1 second and %s", MaximumTTL)
	}
	expiresAt := s.now().UTC().Add(ttl)
	payload, err := json.Marshal(Claims{
		SandboxID: sandboxID,
		UserID:    userID,
		Port:      port,
		ExpiresAt: expiresAt.Unix(),
	})
	if err != nil {
		return "", time.Time{}, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + s.signature(encoded), expiresAt, nil
}

func (s *Signer) Verify(token, sandboxID string, port uint16) (Claims, error) {
	encoded, signature, ok := strings.Cut(strings.TrimSpace(token), ".")
	if !ok || encoded == "" || signature == "" {
		return Claims{}, ErrInvalidToken
	}
	expected := s.signature(encoded)
	if !hmac.Equal([]byte(signature), []byte(expected)) {
		return Claims{}, ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var claims Claims
	if json.Unmarshal(payload, &claims) != nil || claims.SandboxID != sandboxID || claims.Port != port || claims.UserID == "" {
		return Claims{}, ErrInvalidToken
	}
	if claims.ExpiresAt <= s.now().UTC().Unix() {
		return Claims{}, ErrInvalidToken
	}
	return claims, nil
}

func ParsePort(value string) (uint16, error) {
	port, err := strconv.ParseUint(value, 10, 16)
	if err != nil || port == 0 {
		return 0, errors.New("port must be between 1 and 65535")
	}
	return uint16(port), nil
}

func (s *Signer) signature(payload string) string {
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
