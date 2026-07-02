package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// Sandbox is a row in the public.sandboxes registry table — the dashboard's source of
// truth for listing sandboxes and their state. The backend is the only writer (service
// role, bypasses RLS), same as usage_logs.
type Sandbox struct {
	ID       string `json:"id"`
	UserID   string `json:"user_id"`
	APIKeyID string `json:"api_key_id,omitempty"`
	Name     string `json:"name"`
	State    string `json:"state"`
	Tier     string `json:"tier"`
	VCPUs    int    `json:"vcpus"`
	MemoryMB int    `json:"memory_mb"`
	DiskGB   int    `json:"disk_gb"`
	Internet bool   `json:"internet"`
}

// UpsertSandbox inserts (or upserts on id) a sandbox row when a session is created.
// Best-effort: failures are logged and never block the request.
func (c *Client) UpsertSandbox(ctx context.Context, sb Sandbox) {
	body, err := json.Marshal(sb)
	if err != nil {
		slog.Warn("sandbox upsert marshal failed", "err", err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/rest/v1/sandboxes", bytes.NewReader(body))
	if err != nil {
		slog.Warn("sandbox upsert request build failed", "err", err)
		return
	}
	req.Header.Set("apikey", c.serviceRoleKey)
	req.Header.Set("Authorization", "Bearer "+c.serviceRoleKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "resolution=merge-duplicates,return=minimal")
	c.doSandbox(req, "upsert")
}

// UpdateSandboxState patches a sandbox's state (active/paused/destroyed) and the relevant
// timestamps so the dashboard reflects reality after pause/resume/destroy — including
// automatic idle pauses driven by the reaper.
func (c *Client) UpdateSandboxState(ctx context.Context, id, state string) {
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)
	patch := map[string]any{"state": state, "updated_at": nowStr}
	switch state {
	case "paused":
		patch["paused_at"] = nowStr
		// 7-day retention TTL (matches the backend reaper's pauseTTL).
		patch["expires_at"] = now.Add(7 * 24 * time.Hour).Format(time.RFC3339)
	case "active":
		patch["last_used_at"] = nowStr
		patch["expires_at"] = nil // running → no TTL
	}
	body, err := json.Marshal(patch)
	if err != nil {
		slog.Warn("sandbox state marshal failed", "err", err)
		return
	}
	url := c.baseURL + "/rest/v1/sandboxes?id=eq." + id
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		slog.Warn("sandbox state request build failed", "err", err)
		return
	}
	req.Header.Set("apikey", c.serviceRoleKey)
	req.Header.Set("Authorization", "Bearer "+c.serviceRoleKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "return=minimal")
	c.doSandbox(req, "state")
}

// SandboxLog is one line of execution output for a sandbox (public.sandbox_logs).
type SandboxLog struct {
	SandboxID string `json:"sandbox_id"`
	UserID    string `json:"user_id"`
	Stream    string `json:"stream"` // stdout | stderr | system
	Language  string `json:"language,omitempty"`
	Content   string `json:"content"`
}

// InsertSandboxLog appends an execution-output line for a sandbox. Best-effort.
func (c *Client) InsertSandboxLog(ctx context.Context, l SandboxLog) {
	body, err := json.Marshal(l)
	if err != nil {
		slog.Warn("sandbox log marshal failed", "err", err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/rest/v1/sandbox_logs", bytes.NewReader(body))
	if err != nil {
		slog.Warn("sandbox log request build failed", "err", err)
		return
	}
	req.Header.Set("apikey", c.serviceRoleKey)
	req.Header.Set("Authorization", "Bearer "+c.serviceRoleKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "return=minimal")
	c.doSandbox(req, "log")
}

func (c *Client) doSandbox(req *http.Request, op string) {
	resp, err := c.http.Do(req)
	if err != nil {
		slog.Warn("sandbox "+op+" failed", "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Warn("sandbox "+op+" unexpected status", "status", resp.StatusCode)
	}
}
