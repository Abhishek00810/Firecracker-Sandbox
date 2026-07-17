package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/renderops-ai/renderops-sandbox/services/control-plane/internal/application/auth"
)

// NewRouter builds the control-plane HTTP mux. Public routes (health) are open;
// tenant routes wrap with Auth(authn). Sandbox lifecycle routes land here next.
func NewRouter(authn *auth.Authenticator) http.Handler {
	mux := http.NewServeMux()

	// Public.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status": "ok",
			"role":   "control-plane",
		})
	})

	// Protected: echoes the authenticated identity (proves the auth path works).
	mux.Handle("GET /me", Auth(authn)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r.Context())
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no principal"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"tenant_id":    p.TenantID,
			"api_key_id":   p.APIKeyID,
			"balance_usd":  p.Balance,
			"max_sessions": p.Policy.MaxSessions,
		})
	})))

	return mux
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
