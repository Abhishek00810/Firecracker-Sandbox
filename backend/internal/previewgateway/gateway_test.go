package previewgateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/ide"
	"backend/internal/ideauth"
	"backend/internal/orchestrator"
	"backend/internal/preview"
)

const sandboxID = "1f6552e4-cf25-42b1-929b-7fd35a086f1b"

type fakePlacements struct {
	placement orchestrator.Placement
	err       error
}

func (f fakePlacements) Placement(context.Context, string) (orchestrator.Placement, error) {
	return f.placement, f.err
}

func TestGatewayExchangesTokenForCookieAndProxies(t *testing.T) {
	var upstreamPath, upstreamHost, workerToken, upstreamCookies string
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamPath = r.URL.RequestURI()
		upstreamHost = r.Header.Get("X-Forwarded-Host")
		workerToken = r.Header.Get("X-Worker-Token")
		upstreamCookies = r.Header.Get("Cookie")
		w.Header().Add("Set-Cookie", "__Host-renderops_preview=attacker; Secure; Path=/")
		w.Header().Add("Set-Cookie", "app_session=xyz; Secure; Path=/")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("guest response"))
	}))
	defer worker.Close()

	signer, _ := preview.NewSigner(preview.DeriveSigningSecret("worker-secret"))
	token, _, err := signer.Sign(sandboxID, "user-1", 3000, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := New("dev-sandbox.renderops.com", signer, fakePlacements{placement: orchestrator.Placement{
		SandboxID: sandboxID, WorkerID: "worker-1", Endpoint: worker.URL, State: "active",
	}}, "worker-secret")
	if err != nil {
		t.Fatal(err)
	}

	host := "3000-" + sandboxID + ".dev-sandbox.renderops.com"
	bootstrap := httptest.NewRequest(http.MethodGet, "https://"+host+"/app?q=1&_renderops_token="+token, nil)
	bootstrap.Host = host
	bootstrapResponse := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(bootstrapResponse, bootstrap)
	if bootstrapResponse.Code != http.StatusSeeOther {
		t.Fatalf("bootstrap status=%d body=%s", bootstrapResponse.Code, bootstrapResponse.Body.String())
	}
	if location := bootstrapResponse.Header().Get("Location"); location != "/app?q=1" {
		t.Fatalf("location=%q", location)
	}
	if policy := bootstrapResponse.Header().Get("Referrer-Policy"); policy != "no-referrer" {
		t.Fatalf("referrer policy=%q", policy)
	}
	cookies := bootstrapResponse.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatalf("preview cookie=%v", cookies)
	}

	request := httptest.NewRequest(http.MethodGet, "https://"+host+"/app/assets.js?q=2", nil)
	request.Host = host
	request.AddCookie(cookies[0])
	request.AddCookie(&http.Cookie{Name: "app_session", Value: "abc"})
	response := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "guest response" {
		t.Fatalf("proxy status=%d body=%q", response.Code, response.Body.String())
	}
	if upstreamPath != "/worker/sandbox/"+sandboxID+"/ports/3000/app/assets.js?q=2" {
		t.Fatalf("upstream path=%q", upstreamPath)
	}
	if upstreamHost != host || workerToken != "worker-secret" {
		t.Fatalf("forwarded host=%q token=%q", upstreamHost, workerToken)
	}
	if upstreamCookies != "app_session=abc" {
		t.Fatalf("unexpected upstream cookies: %q", upstreamCookies)
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == previewCookieName {
			t.Fatalf("guest overwrote reserved preview cookie")
		}
	}
}

func TestGatewayRedeemsIDEHandoffOnceAndProxiesWithSessionCookie(t *testing.T) {
	var upstreamCookies string
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCookies = r.Header.Get("Cookie")
		w.WriteHeader(http.StatusOK)
	}))
	defer worker.Close()

	previewSigner, _ := preview.NewSigner(preview.DeriveSigningSecret("worker-secret"))
	ideSigner, _ := ideauth.NewSigner(ideauth.DeriveSigningSecret("worker-secret"))
	handoff, _, err := ideSigner.IssueHandoff(sandboxID, "user-1", "", "owner", ide.DefaultPort, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	gateway, _ := New("dev-sandbox.renderops.com", previewSigner, fakePlacements{placement: orchestrator.Placement{
		SandboxID: sandboxID, WorkerID: "worker-1", Endpoint: worker.URL, State: "active",
	}}, "worker-secret")
	if err := gateway.EnableIDE(ideSigner, ideauth.NewMemoryNonceStore()); err != nil {
		t.Fatal(err)
	}

	host := "3001-" + sandboxID + ".dev-sandbox.renderops.com"
	bootstrap := httptest.NewRequest(http.MethodGet, "https://"+host+"/?ro_auth="+handoff, nil)
	response := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(response, bootstrap)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/" {
		t.Fatalf("bootstrap status=%d location=%q", response.Code, response.Header().Get("Location"))
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != ideCookieName || !cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatalf("IDE cookies=%v", cookies)
	}

	replay := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(replay, bootstrap)
	if replay.Code != http.StatusNotFound {
		t.Fatalf("replay status=%d", replay.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "https://"+host+"/", nil)
	request.AddCookie(cookies[0])
	request.AddCookie(&http.Cookie{Name: "app_session", Value: "abc"})
	proxied := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(proxied, request)
	if proxied.Code != http.StatusOK || upstreamCookies != "app_session=abc" {
		t.Fatalf("proxy status=%d upstream_cookies=%q", proxied.Code, upstreamCookies)
	}
}

func TestGatewayReturnsSameNotFoundForInvalidRequests(t *testing.T) {
	signer, _ := preview.NewSigner(preview.DeriveSigningSecret("worker-secret"))
	gateway, _ := New("dev-sandbox.renderops.com", signer, fakePlacements{}, "worker-secret")
	for _, target := range []string{
		"https://bad.dev-sandbox.renderops.com/",
		"https://3000-" + sandboxID + ".dev-sandbox.renderops.com/",
		"https://3000-" + sandboxID + ".dev-sandbox.renderops.com/?_renderops_token=invalid",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		response := httptest.NewRecorder()
		gateway.Handler().ServeHTTP(response, request)
		body, _ := io.ReadAll(response.Result().Body)
		if response.Code != http.StatusNotFound || !strings.Contains(string(body), "Not Found") {
			t.Fatalf("target=%s status=%d body=%q", target, response.Code, body)
		}
	}
}
