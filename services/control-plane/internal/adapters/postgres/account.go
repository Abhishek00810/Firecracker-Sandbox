package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/renderops-ai/renderops-sandbox/services/control-plane/internal/application/auth"
)

// ResolveKey looks up an API key by its hash, joined to its profile for the
// balance. Thin data access only — the auth rules (active/expiry/funded) are
// applied in application/auth, not here.
func (s *Store) ResolveKey(ctx context.Context, keyHash string) (auth.Account, error) {
	var (
		acct      auth.Account
		expiresAt *time.Time
	)
	err := s.pool.QueryRow(ctx, `
		SELECT k.id::text, k.user_id::text, k.is_active, k.expires_at,
		       p.balance_usd::double precision
		FROM api_keys k
		JOIN profiles p ON p.id = k.user_id
		WHERE k.key_hash = $1
		LIMIT 1`, keyHash).Scan(&acct.APIKeyID, &acct.TenantID, &acct.IsActive, &expiresAt, &acct.BalanceUSD)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Account{}, auth.ErrUnauthorized
	}
	if err != nil {
		return auth.Account{}, fmt.Errorf("resolve api key: %w", err)
	}
	acct.ExpiresAt = expiresAt
	return acct, nil
}

var _ auth.AccountStore = (*Store)(nil)
