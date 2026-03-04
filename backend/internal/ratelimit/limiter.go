package ratelimit

import (
	"sync"

	"golang.org/x/time/rate"
)

type TenantLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	rate     rate.Limit
	burst    int
}

func NewTenantLimiter(r rate.Limit, burst int) *TenantLimiter {
	return &TenantLimiter{
		limiters: make(map[string]*rate.Limiter),
		rate:     r,
		burst:    burst,
	}
}

func (t *TenantLimiter) GetLimiter(tenantID string) *rate.Limiter {
	t.mu.Lock()
	defer t.mu.Unlock()

	if limiter, exists := t.limiters[tenantID]; exists {
		return limiter
	}

	limiter := rate.NewLimiter(t.rate, t.burst)
	t.limiters[tenantID] = limiter
	return limiter
}
