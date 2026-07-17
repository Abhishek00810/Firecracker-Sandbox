package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/renderops-ai/renderops-sandbox/services/control-plane/internal/policy"
)

// LoadExecutionPolicy reads the singleton runtime policy (execution_policies,
// id='default'). Loaded once at startup; clients never select it.
func (s *Store) LoadExecutionPolicy(ctx context.Context) (policy.ExecutionPolicy, error) {
	var (
		out                              policy.ExecutionPolicy
		defaultMs, maxMs, idleMs, lifeMs int64
	)
	err := s.pool.QueryRow(ctx, `
		SELECT rate_limit::double precision, rate_burst,
		       default_exec_timeout_ms, max_exec_timeout_ms,
		       pool_size, workers, max_sessions,
		       session_idle_timeout_ms, session_max_lifetime_ms
		FROM execution_policies
		WHERE id = 'default'`).Scan(
		&out.RateLimit, &out.RateBurst, &defaultMs, &maxMs,
		&out.MinPoolSize, &out.MaxPoolSize, &out.MaxSessions, &idleMs, &lifeMs)
	if err != nil {
		return policy.ExecutionPolicy{}, fmt.Errorf("load execution policy: %w", err)
	}
	out.DefaultExecTimeout = time.Duration(defaultMs) * time.Millisecond
	out.MaxExecTimeout = time.Duration(maxMs) * time.Millisecond
	out.SessionIdleTimeout = time.Duration(idleMs) * time.Millisecond
	out.SessionMaxLifetime = time.Duration(lifeMs) * time.Millisecond
	return out, nil
}
