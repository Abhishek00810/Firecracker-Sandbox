// Package auth is the authentication use case: it identifies an API key and
// enforces account rules (active, not expired, funded). The rules live here,
// NOT in the database layer — the DB adapter only returns raw account data.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/renderops-ai/renderops-sandbox/services/control-plane/internal/policy"
)

var (
	ErrUnauthorized        = errors.New("unauthorized")
	ErrInsufficientBalance = errors.New("insufficient balance")
)

// Account is the raw identity a credential resolves to (data only, no rules).
type Account struct {
	APIKeyID   string
	TenantID   string
	IsActive   bool
	ExpiresAt  *time.Time
	BalanceUSD float64
}

// AccountStore resolves a hashed API key to an account. This package owns the
// port so the Postgres adapter depends inward on it.
type AccountStore interface {
	ResolveKey(ctx context.Context, keyHash string) (Account, error)
}

// Principal is the authenticated identity attached to a request.
type Principal struct {
	TenantID string
	APIKeyID string
	Balance  float64
	Policy   policy.ExecutionPolicy
}

// Authenticator resolves credentials to a Principal, applying account rules and
// attaching the server policy.
type Authenticator struct {
	store  AccountStore
	policy policy.ExecutionPolicy
}

func NewAuthenticator(store AccountStore, pol policy.ExecutionPolicy) *Authenticator {
	return &Authenticator{store: store, policy: pol}
}

// Authenticate identifies the API key (by SHA-256 hash) and enforces the
// account rules: exists, active, not expired, funded.
func (a *Authenticator) Authenticate(ctx context.Context, rawKey string) (Principal, error) {
	rawKey = strings.TrimSpace(rawKey)
	if rawKey == "" {
		return Principal{}, ErrUnauthorized
	}
	acct, err := a.store.ResolveKey(ctx, sha256Hex(rawKey))
	if err != nil {
		return Principal{}, ErrUnauthorized
	}
	if !acct.IsActive {
		return Principal{}, ErrUnauthorized
	}
	if acct.ExpiresAt != nil && acct.ExpiresAt.Before(time.Now()) {
		return Principal{}, ErrUnauthorized
	}
	if acct.BalanceUSD <= 0 {
		return Principal{}, ErrInsufficientBalance
	}
	return Principal{
		TenantID: acct.TenantID,
		APIKeyID: acct.APIKeyID,
		Balance:  acct.BalanceUSD,
		Policy:   a.policy,
	}, nil
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
