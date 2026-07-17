package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/renderops-ai/renderops-sandbox/services/control-plane/internal/application/auth"
)

type principalKey struct{}

// PrincipalFrom returns the authenticated principal attached by Auth.
func PrincipalFrom(ctx context.Context) (auth.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(auth.Principal)
	return p, ok
}

// Auth authenticates a request via its API key and attaches the Principal to
// the context. It maps the auth-layer rules to HTTP status codes and holds no
// business logic itself. Key is read from "Authorization: Bearer <key>" or the
// "X-API-Key" header.
func Auth(authn *auth.Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := extractKey(r)
			if key == "" {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing API key"})
				return
			}
			principal, err := authn.Authenticate(r.Context(), key)
			switch {
			case errors.Is(err, auth.ErrInsufficientBalance):
				writeJSON(w, http.StatusPaymentRequired, map[string]string{"error": "insufficient balance — add credits to continue"})
				return
			case err != nil:
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid API key"})
				return
			}
			ctx := context.WithValue(r.Context(), principalKey{}, principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func extractKey(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}
