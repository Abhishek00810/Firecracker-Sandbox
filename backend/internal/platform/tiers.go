package platform

import (
	"context"
	"fmt"
	"time"

	"backend/internal/tierconfig"
)

// LoadTierConfigs reads the tier_configs table, which is created and seeded by
// sk-renderops-platform (pnpm db:push); this backend only reads it. Column
// mapping: workers = MaxPoolSize, pool_size = MinPoolSize.
func (c *Client) LoadTierConfigs(ctx context.Context) ([]tierconfig.TierConfig, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT name, rate_limit::double precision, rate_burst,
		       default_exec_timeout_ms, max_exec_timeout_ms,
		       pool_size, workers, max_sessions,
		       session_idle_timeout_ms, session_max_lifetime_ms,
		       rate_usd_per_sec::double precision
		FROM tier_configs`)
	if err != nil {
		return nil, fmt.Errorf("query tier_configs: %w", err)
	}
	defer rows.Close()

	var out []tierconfig.TierConfig
	for rows.Next() {
		var tc tierconfig.TierConfig
		var defaultMs, maxMs, idleMs, lifetimeMs int64
		if err := rows.Scan(
			&tc.Name, &tc.RateLimit, &tc.RateBurst,
			&defaultMs, &maxMs,
			&tc.MinPoolSize, &tc.MaxPoolSize, &tc.MaxSessions,
			&idleMs, &lifetimeMs,
			&tc.RateUSDPerSec,
		); err != nil {
			return nil, fmt.Errorf("scan tier_configs row: %w", err)
		}
		tc.DefaultExecTimeout = time.Duration(defaultMs) * time.Millisecond
		tc.MaxExecTimeout = time.Duration(maxMs) * time.Millisecond
		tc.SessionIdleTimeout = time.Duration(idleMs) * time.Millisecond
		tc.SessionMaxLifetime = time.Duration(lifetimeMs) * time.Millisecond
		out = append(out, tc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read tier_configs: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("tier_configs is empty — initialize the database from sk-renderops-platform first (pnpm db:push)")
	}
	return out, nil
}
