package platform

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Client stores all platform state directly in PostgreSQL. Both the Go API and
// SvelteKit therefore use the same database and the same Better Auth sessions.
type Client struct {
	pool *pgxpool.Pool
}

func NewClient(ctx context.Context, databaseURL string) (*Client, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	return &Client{pool: pool}, nil
}

func (c *Client) Close() { c.pool.Close() }

// KeyRecord is the resolved result of an api_keys lookup.
type KeyRecord struct {
	ID         string
	UserID     string
	IsActive   bool
	ExpiresAt  *string
	BalanceUSD float64
}

func (c *Client) ResolveKey(keyHash string) (KeyRecord, error) {
	var record KeyRecord
	var expiresAt *time.Time
	err := c.pool.QueryRow(context.Background(), `
		SELECT k.id::text, k.user_id::text, k.is_active,
		       k.expires_at, p.balance_usd::double precision
		FROM api_keys k
		JOIN profiles p ON p.id = k.user_id
		WHERE k.key_hash = $1
		LIMIT 1`, keyHash).Scan(
		&record.ID, &record.UserID, &record.IsActive,
		&expiresAt, &record.BalanceUSD,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return KeyRecord{}, fmt.Errorf("key not found")
	}
	if err != nil {
		return KeyRecord{}, fmt.Errorf("resolve api key: %w", err)
	}
	if expiresAt != nil {
		value := expiresAt.UTC().Format(time.RFC3339)
		record.ExpiresAt = &value
	}
	return record, nil
}
