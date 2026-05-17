package tierconfig

import "time"

type TierName = string

const (
	Free = "free"
	Pro  = "pro"
)

type TierConfig struct {
	Name               string
	RateLimit          float64       // requests/sec, cast to rate.Limit in main.go
	RateBurst          int           // burst bucket size
	MaxExecTimeout     time.Duration // max execution time per request
	MinPoolSize        int           // always-warm VMs kept in pool (0 = fully on-demand)
	MaxPoolSize        int           // hard cap on concurrent VMs for this tier
	MaxSessions        int           // 0 = sessions not allowed for this tier
	SessionIdleTimeout time.Duration // reaper evicts after this idle time
	SessionMaxLifetime time.Duration // hard ceiling regardless of activity (0 = no limit)
}

var Tiers = map[string]TierConfig{
	Free: {
		Name:               Free,
		RateLimit:          2.0,
		RateBurst:          5,
		MaxExecTimeout:     10 * time.Second,
		MinPoolSize:        0,
		MaxPoolSize:        8,
		MaxSessions:        1,
		SessionIdleTimeout: 5 * time.Minute,
		SessionMaxLifetime: 2 * time.Hour,
	},
	Pro: {
		Name:               Pro,
		RateLimit:          10.0,
		RateBurst:          20,
		MaxExecTimeout:     60 * time.Second,
		MinPoolSize:        2,
		MaxPoolSize:        16,
		MaxSessions:        3,
		SessionIdleTimeout: 15 * time.Minute,
		SessionMaxLifetime: 24 * time.Hour,
	},
}

// Get returns the TierConfig for the given name, falling back to Free if unknown.
func Get(name string) TierConfig {
	if tc, ok := Tiers[name]; ok {
		return tc
	}
	return Tiers[Free]
}
