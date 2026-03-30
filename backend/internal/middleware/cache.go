package middleware

import "backend/internal/platform"

// AuthCache is the caching interface for resolved API keys.
// The default implementation is in-memory (keyCache).
// Swap for a Redis-backed implementation when running multiple replicas.
type AuthCache interface {
	get(hash string) (platform.KeyRecord, bool)
	set(hash string, record platform.KeyRecord)
}
