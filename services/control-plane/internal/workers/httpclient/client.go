package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/renderops-ai/renderops-sandbox/services/control-plane/internal/workers"
)

const maxResponseBytes = 4 << 20

// Client is the HTTP adapter for the worker API. It authenticates with the
// internal X-Worker-Token shared secret and speaks the worker's private
// /worker/* endpoints.
type Client struct {
	httpClient *http.Client
	token      string
}

func New(httpClient *http.Client, token string) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{httpClient: httpClient, token: token}
}

func (c *Client) Create(ctx context.Context, endpoint workers.Endpoint, req workers.CreateRequest) (workers.CreateResponse, error) {
	var out workers.CreateResponse
	err := c.do(ctx, http.MethodPost, endpoint.BaseURL+"/worker/sandbox", req, &out, endpoint.WorkerID)
	return out, err
}

func (c *Client) Run(ctx context.Context, endpoint workers.Endpoint, sandboxID string, req workers.RunRequest) (workers.ExecuteResult, error) {
	var out workers.ExecuteResult
	err := c.do(ctx, http.MethodPost, sandboxURL(endpoint, sandboxID, "run"), req, &out, endpoint.WorkerID)
	return out, err
}

func (c *Client) Exec(ctx context.Context, endpoint workers.Endpoint, sandboxID string, req workers.ExecRequest) (workers.ExecuteResult, error) {
	var out workers.ExecuteResult
	err := c.do(ctx, http.MethodPost, sandboxURL(endpoint, sandboxID, "exec"), req, &out, endpoint.WorkerID)
	return out, err
}

func (c *Client) Pause(ctx context.Context, endpoint workers.Endpoint, sandboxID string) error {
	return c.do(ctx, http.MethodPost, sandboxURL(endpoint, sandboxID, "pause"), nil, nil, endpoint.WorkerID)
}

func (c *Client) Resume(ctx context.Context, endpoint workers.Endpoint, sandboxID string) error {
	return c.do(ctx, http.MethodPost, sandboxURL(endpoint, sandboxID, "resume"), nil, nil, endpoint.WorkerID)
}

func (c *Client) Destroy(ctx context.Context, endpoint workers.Endpoint, sandboxID string) error {
	return c.do(ctx, http.MethodDelete, sandboxURL(endpoint, sandboxID, ""), nil, nil, endpoint.WorkerID)
}

func (c *Client) Capacity(ctx context.Context, endpoint workers.Endpoint) (workers.Capacity, error) {
	var out workers.Capacity
	err := c.do(ctx, http.MethodGet, endpoint.BaseURL+"/worker/capacity", nil, &out, endpoint.WorkerID)
	return out, err
}

func (c *Client) Health(ctx context.Context, endpoint workers.Endpoint) error {
	return c.do(ctx, http.MethodGet, endpoint.BaseURL+"/worker/health", nil, nil, endpoint.WorkerID)
}

// sandboxURL builds /worker/sandbox/{id}[/op]. op="" yields the bare sandbox
// path (used by DELETE).
func sandboxURL(endpoint workers.Endpoint, sandboxID, op string) string {
	u := strings.TrimRight(endpoint.BaseURL, "/") + "/worker/sandbox/" + url.PathEscape(sandboxID)
	if op != "" {
		u += "/" + op
	}
	return u
}

// do performs a worker request: marshals body (if any), sets auth + content
// headers, checks for a 2xx status, and decodes the response into out (if any).
// Worker error bodies ({code,error}) are surfaced wrapped in ErrWorkerRequest.
func (c *Client) do(ctx context.Context, method, endpointURL string, body, out any, workerID string) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode worker request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpointURL, reader)
	if err != nil {
		return fmt.Errorf("build worker request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("X-Worker-Token", c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", workers.ErrWorkerRequest, err)
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, maxResponseBytes)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		var werr struct {
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		if json.NewDecoder(limited).Decode(&werr) == nil && (werr.Code != "" || werr.Error != "") {
			return fmt.Errorf("%w: worker %s returned HTTP %d (%s): %s", workers.ErrWorkerRequest, workerID, resp.StatusCode, werr.Code, werr.Error)
		}
		return fmt.Errorf("%w: worker %s returned HTTP %d", workers.ErrWorkerRequest, workerID, resp.StatusCode)
	}

	if out == nil {
		return nil
	}
	if err := json.NewDecoder(limited).Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%w: worker %s returned an empty response", workers.ErrWorkerRequest, workerID)
		}
		return fmt.Errorf("%w: decode worker %s response: %v", workers.ErrWorkerRequest, workerID, err)
	}
	return nil
}

var _ workers.Client = (*Client)(nil)
