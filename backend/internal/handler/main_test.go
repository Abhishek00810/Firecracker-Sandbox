package handler_test

import (
	"os"
	"testing"
	"time"

	"backend/internal/tierconfig"
)

// In production the tier map is loaded from the tier_configs table (seeded by
// sk-renderops-platform). Tests run without a database, so they install an
// equivalent fixture here.
func TestMain(m *testing.M) {
	tierconfig.Set([]tierconfig.TierConfig{{
		Name:               tierconfig.PAYG,
		RateLimit:          1000,
		RateBurst:          100,
		DefaultExecTimeout: 60 * time.Second,
		MaxExecTimeout:     5 * time.Minute,
		MinPoolSize:        0,
		MaxPoolSize:        50,
		MaxSessions:        50,
		SessionIdleTimeout: 5 * time.Minute,
		SessionMaxLifetime: 24 * time.Hour,
		RateUSDPerSec:      0.00002,
	}})
	os.Exit(m.Run())
}
