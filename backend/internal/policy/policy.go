package policy

import "time"

// ExecutionPolicy is the server-owned policy applied to every authenticated
// request. Billing model selection is never accepted from API clients.
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
