package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"backend/internal/platform"
	"backend/internal/tierconfig"
)

// AuthInfo is injected into the request context after a successful key resolution.
// Handlers read this — they never touch tier names or headers directly.
type AuthInfo struct {
	TenantID         string
	APIKeyID         string
	Config           tierconfig.TierConfig
	FreeUSDRemaining float64
	RateUSDPerSec    float64
}

// authInfoKey is an unexported type to prevent context key collisions.
type authInfoKey struct{}

// AuthFromContext retrieves AuthInfo from the request context.
// Returns false if auth middleware did not run or the request was not authenticated.
func AuthFromContext(ctx context.Context) (AuthInfo, bool) {
	info, ok := ctx.Value(authInfoKey{}).(AuthInfo)
	return info, ok
}

// ── TTL cache ────────────────────────────────────────────────────────────────
// Avoids a Supabase round-trip on every request.
// Stale entries are lazily replaced on next lookup — no background goroutine needed.

type cacheEntry struct {
	record    platform.KeyRecord
	fetchedAt time.Time
}

type keyCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
	ttl     time.Duration
}

func newKeyCache(ttl time.Duration) *keyCache {
	return &keyCache{
		entries: make(map[string]cacheEntry),
		ttl:     ttl,
	}
}

func (c *keyCache) get(hash string) (platform.KeyRecord, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[hash]
	if !ok || time.Since(e.fetchedAt) > c.ttl {
		return platform.KeyRecord{}, false
	}
	return e.record, true
}

func (c *keyCache) set(hash string, record platform.KeyRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[hash] = cacheEntry{record: record, fetchedAt: time.Now()}
}

// ── Middleware ────────────────────────────────────────────────────────────────

// Auth returns middleware that validates Bearer tokens against Supabase.
// Keys are cached for 60 seconds to avoid a DB round-trip on every request.
// A revoked key can still be used for up to 60 seconds after revocation.
// AuthResolver resolves an SDK API key or a dashboard user (via Supabase JWT) to identity.
type AuthResolver interface {
	ResolveKey(keyHash string) (platform.KeyRecord, error)
	GetProfile(userID string) (platform.Profile, error)
}

func Auth(pc AuthResolver, supabaseURL, jwtSecret string) func(http.Handler) http.Handler {
	var cache AuthCache = newKeyCache(60 * time.Second)
	jwtv := newJWTVerifier(supabaseURL, jwtSecret)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip auth for infrastructure endpoints
			if r.URL.Path == "/health" || r.URL.Path == "/metrics" {
				next.ServeHTTP(w, r)
				return
			}

			// Extract Bearer token
			raw := r.Header.Get("Authorization")
			if !strings.HasPrefix(raw, "Bearer ") {
				writeAuthError(w, http.StatusUnauthorized, "missing or invalid Authorization header")
				return
			}
			key := strings.TrimPrefix(raw, "Bearer ")

			// Dashboard path: a Supabase user JWT (verified against the project's public
			// JWKS) authenticates as that user — no API key needed. API keys have no dots.
			if looksLikeJWT(key) {
				sub, err := jwtv.verify(key)
				if err != nil {
					slog.Warn("auth: jwt verification failed", "err", err)
					writeAuthError(w, http.StatusUnauthorized, "invalid session token")
					return
				}
				prof, err := pc.GetProfile(sub)
				if err != nil {
					slog.Warn("auth: profile lookup failed", "user_id", sub, "err", err)
					writeAuthError(w, http.StatusUnauthorized, "user profile not found")
					return
				}
				if prof.FreeUSDRemaining <= 0 {
					writeAuthError(w, http.StatusPaymentRequired, "insufficient balance — add credits to continue")
					return
				}
				tc := tierconfig.Get(prof.Tier)
				info := AuthInfo{
					TenantID:         sub,
					APIKeyID:         "",
					Config:           tc,
					FreeUSDRemaining: prof.FreeUSDRemaining,
					RateUSDPerSec:    tc.RateUSDPerSec,
				}
				ctx := context.WithValue(r.Context(), authInfoKey{}, info)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			hash := sha256Hex(key)

			// Check cache first
			record, ok := cache.get(hash)
			if !ok {
				var err error
				record, err = pc.ResolveKey(hash)
				if err != nil {
					slog.Warn("auth: key resolution failed", "err", err)
					writeAuthError(w, http.StatusUnauthorized, "invalid API key")
					return
				}
				cache.set(hash, record)
			}

			// Validate key state
			if !record.IsActive {
				writeAuthError(w, http.StatusUnauthorized, "API key is deactivated")
				return
			}

			if record.ExpiresAt != nil {
				exp, err := time.Parse(time.RFC3339, *record.ExpiresAt)
				if err == nil && time.Now().After(exp) {
					writeAuthError(w, http.StatusUnauthorized, "API key has expired")
					return
				}
			}

			if record.FreeUSDRemaining <= 0 {
				writeAuthError(w, http.StatusPaymentRequired, "insufficient balance — add credits to continue")
				return
			}

			tc := tierconfig.Get(record.Tier)
			info := AuthInfo{
				TenantID:         record.UserID,
				APIKeyID:         record.ID,
				Config:           tc,
				FreeUSDRemaining: record.FreeUSDRemaining,
				RateUSDPerSec:    tc.RateUSDPerSec,
			}

			slog.Debug("auth: resolved", "tenant_id", record.UserID, "tier", record.Tier)

			ctx := context.WithValue(r.Context(), authInfoKey{}, info)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func writeAuthError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
