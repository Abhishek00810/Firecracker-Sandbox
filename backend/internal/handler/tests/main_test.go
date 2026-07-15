package handler_test

import (
	"time"

	"backend/internal/billing"
	"backend/internal/policy"
)

// Tests run without a database, so they use the same singleton PAYG policy shape.
func testExecutionPolicy() policy.ExecutionPolicy {
	return policy.ExecutionPolicy{
		RateLimit:          1000,
		RateBurst:          100,
		DefaultExecTimeout: 60 * time.Second,
		MaxExecTimeout:     5 * time.Minute,
		MinPoolSize:        0,
		MaxPoolSize:        50,
		MaxSessions:        50,
		SessionIdleTimeout: 5 * time.Minute,
		SessionMaxLifetime: 24 * time.Hour,
	}
}

func testBillingConfig() billing.Config {
	return billing.Config{Model: billing.PAYG, ExecutionRateUSDPerSec: 0.00002}
}
