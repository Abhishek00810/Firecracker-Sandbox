// Package terminal owns control-plane metadata for interactive terminal sessions.
// PTY creation and streaming remain worker-plane responsibilities.
package terminal

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const tokenVersion = 1

const terminalSigningContext = "renderops/terminal-attachment-token/v1"

var (
	ErrInvalidToken = errors.New("invalid terminal attachment token")
	ErrExpiredToken = errors.New("terminal attachment token expired")
	ErrTokenUsed    = errors.New("terminal attachment token already used")
)

type State string

const (
	StateCreating State = "creating"
	StateReady    State = "ready"
	StateClosed   State = "closed"
)

type Session struct {
	ID        string    `json:"terminal_id"`
	SandboxID string    `json:"sandbox_id"`
	UserID    string    `json:"-"`
	WorkerID  string    `json:"-"`
	State     State     `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"token_expires_at"`
}

type Claims struct {
	Version    int    `json:"version"`
	TerminalID string `json:"terminal_id"`
	SandboxID  string `json:"sandbox_id"`
	UserID     string `json:"user_id"`
	WorkerID   string `json:"worker_id"`
	IssuedAt   int64  `json:"iat"`
	ExpiresAt  int64  `json:"exp"`
	TokenID    string `json:"jti"`
}

type record struct {
	Session
	tokenID  string
	consumed bool
}

type Manager struct {
	mu       sync.Mutex
	secret   []byte
	tokenTTL time.Duration
	now      func() time.Time
	sessions map[string]*record
}

func NewManager(secret string, tokenTTL time.Duration) (*Manager, error) {
	if len(secret) < 32 {
		return nil, errors.New("terminal token secret must contain at least 32 characters")
	}
	if tokenTTL <= 0 {
		return nil, errors.New("terminal token TTL must be positive")
	}
	return &Manager{
		secret:   []byte(secret),
		tokenTTL: tokenTTL,
		now:      time.Now,
		sessions: make(map[string]*record),
	}, nil
}

// DeriveSigningSecret keeps terminal attachment tokens in a separate
// cryptographic domain while using the existing worker credential as key material.
func DeriveSigningSecret(workerToken string) string {
	mac := hmac.New(sha256.New, []byte(workerToken))
	_, _ = mac.Write([]byte(terminalSigningContext))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (m *Manager) Reserve(sandboxID, userID, workerID string) (Session, error) {
	sandboxID = strings.TrimSpace(sandboxID)
	userID = strings.TrimSpace(userID)
	workerID = strings.TrimSpace(workerID)
	if sandboxID == "" || userID == "" || workerID == "" {
		return Session{}, errors.New("sandbox id, user id, and worker id are required")
	}
	now := m.now().UTC()
	terminalID, err := randomID("term_", 18)
	if err != nil {
		return Session{}, fmt.Errorf("generate terminal id: %w", err)
	}

	session := Session{
		ID:        terminalID,
		SandboxID: sandboxID,
		UserID:    userID,
		WorkerID:  workerID,
		State:     StateCreating,
		CreatedAt: now,
	}

	m.mu.Lock()
	m.pruneExpiredLocked(now)
	m.sessions[terminalID] = &record{Session: session}
	m.mu.Unlock()
	return session, nil
}

func (m *Manager) Authorize(terminalID string) (Session, string, error) {
	now := m.now().UTC()
	tokenID, err := randomID("", 18)
	if err != nil {
		return Session{}, "", fmt.Errorf("generate terminal token id: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.sessions[terminalID]
	if !ok || rec.State != StateCreating {
		return Session{}, "", errors.New("terminal reservation not found")
	}
	rec.State = StateReady
	rec.ExpiresAt = now.Add(m.tokenTTL)
	rec.tokenID = tokenID
	claims := Claims{
		Version: tokenVersion, TerminalID: rec.ID, SandboxID: rec.SandboxID,
		UserID: rec.UserID, WorkerID: rec.WorkerID, IssuedAt: now.Unix(),
		ExpiresAt: rec.ExpiresAt.Unix(), TokenID: tokenID,
	}
	token, err := m.sign(claims)
	if err != nil {
		return Session{}, "", err
	}
	return rec.Session, token, nil
}

func (m *Manager) Cancel(terminalID string) {
	m.mu.Lock()
	delete(m.sessions, terminalID)
	m.mu.Unlock()
}

// Consume validates a single-use attachment token. The WebSocket handler calls this when
// a WebSocket is attached, preventing token replay after the first connection.
func (m *Manager) Consume(token string) (Session, error) {
	claims, err := m.verify(token)
	if err != nil {
		return Session{}, err
	}
	now := m.now().UTC()
	if now.Unix() >= claims.ExpiresAt {
		return Session{}, ErrExpiredToken
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.sessions[claims.TerminalID]
	if !ok || rec.State != StateReady || rec.tokenID != claims.TokenID ||
		rec.SandboxID != claims.SandboxID || rec.UserID != claims.UserID || rec.WorkerID != claims.WorkerID {
		return Session{}, ErrInvalidToken
	}
	if rec.consumed {
		return Session{}, ErrTokenUsed
	}
	rec.consumed = true
	return rec.Session, nil
}

func (m *Manager) sign(claims Claims) (string, error) {
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal terminal token: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, m.secret)
	_, _ = mac.Write([]byte(encoded))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encoded + "." + signature, nil
}

func (m *Manager) verify(token string) (Claims, error) {
	var claims Claims
	separator := -1
	for i := 0; i < len(token); i++ {
		if token[i] == '.' {
			if separator != -1 {
				return claims, ErrInvalidToken
			}
			separator = i
		}
	}
	if separator <= 0 || separator == len(token)-1 {
		return claims, ErrInvalidToken
	}
	payloadPart, signaturePart := token[:separator], token[separator+1:]
	signature, err := base64.RawURLEncoding.DecodeString(signaturePart)
	if err != nil {
		return claims, ErrInvalidToken
	}
	mac := hmac.New(sha256.New, m.secret)
	_, _ = mac.Write([]byte(payloadPart))
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return claims, ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil || json.Unmarshal(payload, &claims) != nil || claims.Version != tokenVersion ||
		claims.TerminalID == "" || claims.SandboxID == "" || claims.UserID == "" ||
		claims.WorkerID == "" || claims.TokenID == "" || claims.ExpiresAt <= claims.IssuedAt {
		return Claims{}, ErrInvalidToken
	}
	return claims, nil
}

func (m *Manager) pruneExpiredLocked(now time.Time) {
	for id, rec := range m.sessions {
		reservationExpired := rec.State == StateCreating && !now.Before(rec.CreatedAt.Add(m.tokenTTL))
		tokenExpired := rec.State == StateReady && !rec.consumed && !now.Before(rec.ExpiresAt)
		if rec.State == StateClosed || reservationExpired || tokenExpired {
			delete(m.sessions, id)
		}
	}
}

func randomID(prefix string, byteCount int) (string, error) {
	raw := make([]byte, byteCount)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}
