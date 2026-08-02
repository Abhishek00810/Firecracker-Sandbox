package previewgateway

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"backend/internal/orchestrator"
	"backend/internal/plane"
	"backend/internal/preview"

	"github.com/google/uuid"
)

const cookieName = "__Host-renderops_preview"

type PlacementResolver interface {
	Placement(context.Context, string) (orchestrator.Placement, error)
}

type Gateway struct {
	domain      string
	signer      *preview.Signer
	placements  PlacementResolver
	workerToken string
}

func New(domain string, signer *preview.Signer, placements PlacementResolver, workerToken string) (*Gateway, error) {
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if domain == "" || signer == nil || placements == nil || strings.TrimSpace(workerToken) == "" {
		return nil, fmt.Errorf("preview domain, signer, placement resolver, and worker token are required")
	}
	return &Gateway{domain: domain, signer: signer, placements: placements, workerToken: workerToken}, nil
}

func (g *Gateway) Handler() http.Handler {
	return http.HandlerFunc(g.serveHTTP)
}

func (g *Gateway) serveHTTP(w http.ResponseWriter, r *http.Request) {
	port, sandboxID, ok := g.parseHost(r.Host)
	if !ok {
		writeNotFound(w)
		return
	}

	if token := r.URL.Query().Get("_renderops_token"); token != "" {
		claims, err := g.signer.Verify(token, sandboxID, port)
		if err != nil {
			writeNotFound(w)
			return
		}
		expiresAt := time.Unix(claims.ExpiresAt, 0)
		http.SetCookie(w, &http.Cookie{
			Name: cookieName, Value: token, Path: "/", Secure: true, HttpOnly: true,
			SameSite: http.SameSiteLaxMode, Expires: expiresAt, MaxAge: max(1, int(time.Until(expiresAt).Seconds())),
		})
		clean := *r.URL
		query := clean.Query()
		query.Del("_renderops_token")
		clean.RawQuery = query.Encode()
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Location", clean.RequestURI())
		w.WriteHeader(http.StatusSeeOther)
		return
	}

	cookie, err := r.Cookie(cookieName)
	if err != nil {
		writeNotFound(w)
		return
	}
	if _, err := g.signer.Verify(cookie.Value, sandboxID, port); err != nil {
		writeNotFound(w)
		return
	}

	placement, err := g.placements.Placement(r.Context(), sandboxID)
	if err != nil || placement.Endpoint == "" || subtle.ConstantTimeCompare([]byte(placement.State), []byte("active")) != 1 {
		writeNotFound(w)
		return
	}
	target, err := url.Parse(placement.Endpoint)
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		slog.Error("preview placement returned invalid worker endpoint", "sandbox_id", sandboxID, "worker_id", placement.WorkerID)
		writeNotFound(w)
		return
	}

	originalHost := r.Host
	originalPath := r.URL.Path
	if originalPath == "" {
		originalPath = "/"
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.Out.URL.Scheme = target.Scheme
			request.Out.URL.Host = target.Host
			request.Out.URL.Path = fmt.Sprintf("%s%s/ports/%d%s", plane.RouteSandboxPrefix, sandboxID, port, originalPath)
			request.Out.URL.RawPath = ""
			request.Out.Host = target.Host
			stripCookie(request.Out, cookieName)
			request.SetXForwarded()
			request.Out.Header.Set(plane.AuthHeader, g.workerToken)
			request.Out.Header.Set("X-Forwarded-Host", originalHost)
			request.Out.Header.Set("X-Forwarded-Proto", "https")
		},
		FlushInterval: -1,
		ModifyResponse: func(response *http.Response) error {
			setCookies := response.Header.Values("Set-Cookie")
			response.Header.Del("Set-Cookie")
			for _, value := range setCookies {
				cookie, err := http.ParseSetCookie(value)
				if err != nil || cookie.Name != cookieName {
					response.Header.Add("Set-Cookie", value)
				}
			}
			return nil
		},
		ErrorHandler: func(response http.ResponseWriter, _ *http.Request, proxyErr error) {
			slog.Warn("preview upstream unavailable", "sandbox_id", sandboxID, "worker_id", placement.WorkerID, "err", proxyErr)
			writeNotFound(response)
		},
	}
	proxy.ServeHTTP(w, r)
}

func stripCookie(request *http.Request, reservedName string) {
	cookies := request.Cookies()
	request.Header.Del("Cookie")
	for _, cookie := range cookies {
		if cookie.Name != reservedName {
			request.AddCookie(cookie)
		}
	}
}

func (g *Gateway) parseHost(value string) (uint16, string, bool) {
	host := strings.ToLower(value)
	if parsedHost, _, err := strings.Cut(host, ":"); err {
		host = parsedHost
	}
	suffix := "." + g.domain
	if !strings.HasSuffix(host, suffix) {
		return 0, "", false
	}
	label := strings.TrimSuffix(host, suffix)
	portText, sandboxID, ok := strings.Cut(label, "-")
	if !ok {
		return 0, "", false
	}
	port, err := preview.ParsePort(portText)
	if err != nil {
		return 0, "", false
	}
	parsedID, err := uuid.Parse(sandboxID)
	if err != nil || parsedID.String() != sandboxID {
		return 0, "", false
	}
	return port, sandboxID, true
}

func writeNotFound(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte("Not Found\n"))
}

func PortHost(port uint16, sandboxID, domain string) string {
	return strconv.Itoa(int(port)) + "-" + sandboxID + "." + strings.TrimSuffix(domain, ".")
}
