package platform

import (
	"context"
	"fmt"
	"time"

	"backend/internal/billing"
	"backend/internal/policy"
)

// LoadExecutionPolicy loads the singleton server policy. Resource sizes are
// configured separately; clients cannot select a billing or scheduling tier.
func (c *Client) LoadExecutionPolicy(ctx context.Context) (policy.ExecutionPolicy, error) {
	var out policy.ExecutionPolicy
	var defaultMs, maxMs, idleMs, lifetimeMs int64
	err := c.pool.QueryRow(ctx, `
		SELECT rate_limit::double precision, rate_burst,
		       default_exec_timeout_ms, max_exec_timeout_ms,
		       pool_size, workers, max_sessions,
		       session_idle_timeout_ms, session_max_lifetime_ms
		FROM execution_policies
		WHERE id = 'default'`).Scan(
		&out.RateLimit, &out.RateBurst,
		&defaultMs, &maxMs,
		&out.MinPoolSize, &out.MaxPoolSize, &out.MaxSessions,
		&idleMs, &lifetimeMs,
	)
	if err != nil {
		return policy.ExecutionPolicy{}, fmt.Errorf("load PAYG execution policy: %w", err)
	}
	out.DefaultExecTimeout = time.Duration(defaultMs) * time.Millisecond
	out.MaxExecTimeout = time.Duration(maxMs) * time.Millisecond
	out.SessionIdleTimeout = time.Duration(idleMs) * time.Millisecond
	out.SessionMaxLifetime = time.Duration(lifetimeMs) * time.Millisecond
	return out, nil
}

func (c *Client) LoadBillingConfig(ctx context.Context) (billing.Config, error) {
	var out billing.Config
	err := c.pool.QueryRow(ctx, `
		SELECT billing_model, rate_execution_sec
		FROM pricing_rates
		WHERE billing_model = $1 AND effective_from <= now()
		ORDER BY effective_from DESC, version DESC
		LIMIT 1`, billing.PAYG).Scan(&out.Model, &out.ExecutionRateUSDPerSec)
	if err != nil {
		return billing.Config{}, fmt.Errorf("load PAYG billing config: %w", err)
	}
	return out, nil
}
