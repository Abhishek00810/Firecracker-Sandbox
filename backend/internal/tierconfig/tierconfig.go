package tierconfig

import "time"

type TierName = string

const (
	PAYG = "payg"
)

type TierConfig struct {
	Name               string
	RateLimit          float64       // requests/sec, cast to rate.Limit in main.go
	RateBurst          int           // burst bucket size
	DefaultExecTimeout time.Duration // per-execution default when caller doesn't specify
	MaxExecTimeout     time.Duration // hard ceiling per request (caller requests clamped to this)
	MinPoolSize        int           // always-warm VMs kept in pool (0 = fully on-demand)
	MaxPoolSize        int           // hard cap on concurrent VMs for this tier
	MaxSessions        int           // 0 = sessions not allowed for this tier
	SessionIdleTimeout time.Duration // reaper evicts after this idle time
	SessionMaxLifetime time.Duration // hard ceiling regardless of activity (0 = no limit)
	RateUSDPerSec      float64       // billing rate — read from tier_configs in DB
}

var Tiers = map[string]TierConfig{
	PAYG: {
		Name:               PAYG,
		RateLimit:          1000.0,
		RateBurst:          100,
		DefaultExecTimeout: 60 * time.Second,
		MaxExecTimeout:     5 * time.Minute,
		MinPoolSize:        0,
		MaxPoolSize:        50,
		MaxSessions:        50,
		SessionIdleTimeout: 5 * time.Minute,
		SessionMaxLifetime: 24 * time.Hour,
		RateUSDPerSec:      0.000020,
	},
}

// Get returns the TierConfig for the given name, falling back to PAYG if unknown.
func Get(name string) TierConfig {
	if tc, ok := Tiers[name]; ok {
		return tc
	}
	return Tiers[PAYG]
}
