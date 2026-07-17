package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/renderops-ai/renderops-sandbox/services/control-plane/internal/application/auth"
	"github.com/renderops-ai/renderops-sandbox/services/control-plane/internal/policy"
)

type fakeAccountStore struct {
	acct auth.Account
	err  error
}

func (f *fakeAccountStore) ResolveKey(context.Context, string) (auth.Account, error) {
	return f.acct, f.err
}

func testRouter(store auth.AccountStore) http.Handler {
	return NewRouter(auth.NewAuthenticator(store, policy.ExecutionPolicy{MaxSessions: 10}))
}

func TestHealth(t *testing.T) {
	rec := httptest.NewRecorder()
	testRouter(&fakeAccountStore{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["status"] != "ok" || body["role"] != "control-plane" {
		t.Fatalf("body = %#v", body)
	}
}

func TestMeRequiresAPIKey(t *testing.T) {
	rec := httptest.NewRecorder()
	testRouter(&fakeAccountStore{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/me", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-key status = %d, want 401", rec.Code)
	}
}

func TestMeReturnsPrincipal(t *testing.T) {
	store := &fakeAccountStore{acct: auth.Account{APIKeyID: "k1", TenantID: "t1", IsActive: true, BalanceUSD: 5}}
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Authorization", "Bearer ro_live_test")
	rec := httptest.NewRecorder()
	testRouter(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["tenant_id"] != "t1" {
		t.Fatalf("body = %#v", body)
	}
}

func TestMePaymentRequiredWhenNoBalance(t *testing.T) {
	store := &fakeAccountStore{acct: auth.Account{TenantID: "t1", IsActive: true, BalanceUSD: 0}}
	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Authorization", "Bearer ro_live_test")
	rec := httptest.NewRecorder()
	testRouter(store).ServeHTTP(rec, req)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", rec.Code)
	}
}
