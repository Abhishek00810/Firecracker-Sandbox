// Package policy holds the server-owned runtime policy (operational limits).
// It is a leaf domain type with no dependencies: clients never select it, and
// it is loaded once at startup from execution_policies (the "default" singleton).
package policy

import "time"

// ExecutionPolicy is the runtime limits applied to every authenticated request.
type ExecutionPolicy struct {
	RateLimit          float64
	RateBurst          int
	DefaultExecTimeout time.Duration
	MaxExecTimeout     time.Duration
	MinPoolSize        int
	MaxPoolSize        int
	MaxSessions        int
	SessionIdleTimeout time.Duration
	SessionMaxLifetime time.Duration
}
