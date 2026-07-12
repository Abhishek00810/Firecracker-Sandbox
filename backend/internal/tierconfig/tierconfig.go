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
	RateUSDPerSec      float64       // billing rate
}

// tiers is populated once at startup from the tier_configs table via
// platform.LoadTierConfigs + Set. The database — initialized and seeded by
// sk-renderops-platform (pnpm db:push) — is the single source of truth;
// no tier values are hardcoded in this backend.
var tiers = map[string]TierConfig{}

// Set replaces the tier map. Must be called before the servers start; reads
// after that point are unsynchronized by design.
func Set(configs []TierConfig) {
	m := make(map[string]TierConfig, len(configs))
	for _, tc := range configs {
		m[tc.Name] = tc
	}
	tiers = m
}

// Has reports whether a tier was loaded.
func Has(name string) bool {
	_, ok := tiers[name]
	return ok
}

// Get returns the TierConfig for the given name, falling back to PAYG if unknown.
func Get(name string) TierConfig {
	if tc, ok := tiers[name]; ok {
		return tc
	}
	return tiers[PAYG]
}
