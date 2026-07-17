package registry

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/renderops-ai/renderops-sandbox/services/control-plane/internal/workers"
)

type Static struct {
	workers map[string]workers.Endpoint
}

func NewStatic(workerID, baseURL string) (*Static, error) {
	workerID = strings.TrimSpace(workerID)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if workerID == "" {
		return nil, fmt.Errorf("static worker id is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("invalid static worker URL %q", baseURL)
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return nil, fmt.Errorf("static worker URL must use HTTPS; HTTP is allowed only for loopback development")
	}
	endpoint := workers.Endpoint{WorkerID: workerID, BaseURL: baseURL}
	return &Static{workers: map[string]workers.Endpoint{workerID: endpoint}}, nil
}

func (r *Static) GetEndpoint(_ context.Context, workerID string) (workers.Endpoint, error) {
	endpoint, ok := r.workers[workerID]
	if !ok {
		return workers.Endpoint{}, fmt.Errorf("%w: %s", workers.ErrWorkerNotFound, workerID)
	}
	return endpoint, nil
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
