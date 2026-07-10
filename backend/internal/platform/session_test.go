package platform

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveSession(t *testing.T) {
	const token = "opaque+/session=token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/v1/session" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("token"); got != "eq."+token {
			t.Fatalf("unexpected token filter %q", got)
		}
		if got := r.URL.Query().Get("expires_at"); !strings.HasPrefix(got, "gt.") {
			t.Fatalf("missing expiry filter: %q", got)
		}
		if r.Header.Get("apikey") != "service-key" || r.Header.Get("Authorization") != "Bearer service-key" {
			t.Fatal("service role headers were not set")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"user_id":"user-1"}]`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "service-key")
	record, err := client.ResolveSession(token)
	if err != nil {
		t.Fatalf("ResolveSession returned error: %v", err)
	}
	if record.UserID != "user-1" {
		t.Fatalf("unexpected user id %q", record.UserID)
	}
}

func TestResolveSessionRejectsMissingOrExpiredToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "service-key")
	_, err := client.ResolveSession("expired-token")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}
