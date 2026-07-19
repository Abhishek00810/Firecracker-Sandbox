package platform

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

type Profile struct {
	BalanceUSD float64
}

func (c *Client) GetProfile(userID string) (Profile, error) {
	var profile Profile
	err := c.pool.QueryRow(context.Background(), `
		SELECT balance_usd::double precision
		FROM profiles WHERE id = $1`, userID).Scan(&profile.BalanceUSD)
	if err != nil {
		return Profile{}, fmt.Errorf("get profile: %w", err)
	}
	return profile, nil
}

// DebitBalance subtracts amount from the user's credit balance (never below zero).
func (c *Client) DebitBalance(ctx context.Context, userID string, amount float64) error {
	if amount <= 0 || userID == "" {
		return nil
	}
	tag, err := c.pool.Exec(ctx, `
		UPDATE profiles
		SET balance_usd = GREATEST(0::numeric, balance_usd - $2::numeric)
		WHERE id = $1`, userID, amount)
	if err != nil {
		return fmt.Errorf("debit balance: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("debit balance: profile %s not found", userID)
	}
	return nil
}

// BillSandboxRuntime charges unbilled wall-clock time for one sandbox at
// ratePerSec ($/s), debits the owner's balance, and advances metadata.last_billed_at
// so the same seconds are never charged twice.
func (c *Client) BillSandboxRuntime(ctx context.Context, sandboxID string, ratePerSec float64) {
	if sandboxID == "" || ratePerSec <= 0 {
		return
	}

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		slog.Warn("bill runtime: begin failed", "err", err)
		return
	}
	defer tx.Rollback(ctx)

	var (
		userID     string
		state      string
		createdAt  time.Time
		updatedAt  time.Time
		lastUsedAt *time.Time
		pausedAt   *time.Time
		lastBilled *time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT user_id::text,
		       state,
		       created_at,
		       updated_at,
		       last_used_at,
		       paused_at,
		       CASE
		         WHEN metadata ? 'last_billed_at'
		              AND NULLIF(metadata->>'last_billed_at','') IS NOT NULL
		         THEN (metadata->>'last_billed_at')::timestamptz
		         ELSE NULL
		       END
		FROM sandboxes
		WHERE id::text = $1
		FOR UPDATE`, sandboxID).Scan(
		&userID, &state, &createdAt, &updatedAt, &lastUsedAt, &pausedAt, &lastBilled,
	)
	if err != nil {
		slog.Warn("bill runtime: load sandbox failed", "sandbox_id", sandboxID, "err", err)
		return
	}

	start := createdAt
	if lastBilled != nil && lastBilled.After(createdAt) {
		start = *lastBilled
	}

	var end time.Time
	switch state {
	case "active", "creating", "resuming":
		end = time.Now().UTC()
	case "paused", "pausing":
		if pausedAt != nil {
			end = pausedAt.UTC()
		} else {
			end = updatedAt.UTC()
		}
	default:
		if lastUsedAt != nil {
			end = lastUsedAt.UTC()
		} else {
			end = updatedAt.UTC()
		}
	}

	seconds := end.Sub(start).Seconds()
	if seconds < 1 {
		return
	}
	cost := seconds * ratePerSec
	if cost <= 0 {
		return
	}

	if _, err = tx.Exec(ctx, `
		UPDATE profiles
		SET balance_usd = GREATEST(0::numeric, balance_usd - $2::numeric)
		WHERE id = $1`, userID, cost); err != nil {
		slog.Warn("bill runtime: debit failed", "user_id", userID, "err", err)
		return
	}

	// Stamp last_billed_at so we never double-charge these seconds.
	if _, err = tx.Exec(ctx, `
		UPDATE sandboxes
		SET metadata = jsonb_set(
		      COALESCE(metadata, '{}'::jsonb),
		      '{last_billed_at}',
		      to_jsonb($2::text),
		      true
		    )
		WHERE id::text = $1`, sandboxID, end.UTC().Format(time.RFC3339Nano)); err != nil {
		slog.Warn("bill runtime: stamp last_billed_at failed", "sandbox_id", sandboxID, "err", err)
		return
	}

	if err = tx.Commit(ctx); err != nil {
		slog.Warn("bill runtime: commit failed", "err", err)
		return
	}
	slog.Info("billed sandbox runtime",
		"sandbox_id", sandboxID,
		"user_id", userID,
		"seconds", int(seconds),
		"cost_usd", cost,
	)
}
