package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
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

// SandboxRun is one execution (an /exec or /run) — the run-history summary the dashboard
// lists per sandbox (public.sandbox_runs). Its ID links the output lines in sandbox_logs.
type SandboxRun struct {
	ID         string `json:"id"` // run_id (backend-generated)
	SandboxID  string `json:"sandbox_id"`
	UserID     string `json:"user_id"`
	Kind       string `json:"kind"`     // exec | run
	Language   string `json:"language"` // bash | python | ...
	Command    string `json:"command"`  // command or code (truncated)
	ExitCode   int    `json:"exit_code"`
	Status     string `json:"status"` // ok | error | timeout
	DurationMs int    `json:"duration_ms"`
	StartedAt  string `json:"started_at"` // RFC3339, stamped at execution time (not DB insert time)
}

// InsertSandboxRun records one execution's summary. Best-effort.
func (c *Client) InsertSandboxRun(ctx context.Context, run SandboxRun) {
	body, err := json.Marshal(run)
	if err != nil {
		slog.Warn("sandbox run marshal failed", "err", err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/rest/v1/sandbox_runs", bytes.NewReader(body))
	if err != nil {
		slog.Warn("sandbox run request build failed", "err", err)
		return
	}
	req.Header.Set("apikey", c.serviceRoleKey)
	req.Header.Set("Authorization", "Bearer "+c.serviceRoleKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "return=minimal")
	c.doSandbox(req, "run")
}

// SandboxLog is one line of execution output for a sandbox (public.sandbox_logs).
// RunID ties the line to its SandboxRun so the dashboard can filter logs per run.
type SandboxLog struct {
	SandboxID string `json:"sandbox_id"`
	RunID     string `json:"run_id,omitempty"`
	UserID    string `json:"user_id"`
	Stream    string `json:"stream"`          // stdout | stderr | system
	Level     string `json:"level,omitempty"` // info | warn | error (reliable, from stream+exit)
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

// UsageMeter is one accrual interval's raw resource-time delta for a sandbox. RAW UNITS
// ONLY — no cost. Bucket is the HOUR the delta belongs to; the delta is added into the
// single (sandbox_id, hour) row via meter_accrue so one row accumulates the whole hour.
type UsageMeter struct {
	UserID        string
	SandboxID     string
	Tier          string
	Bucket        string // RFC3339, truncated to the hour
	VCPUSeconds   float64
	RAMGBSeconds  float64
	DiskGBSeconds float64
}

// AccrueUsageMeter adds a delta into the sandbox's current hour row (INSERT ... ON CONFLICT
// DO UPDATE, via the meter_accrue RPC). One accumulating row per sandbox per hour, not a new
// row each minute. Best-effort.
func (c *Client) AccrueUsageMeter(ctx context.Context, m UsageMeter) {
	args := map[string]any{
		"p_user_id":    m.UserID,
		"p_sandbox_id": m.SandboxID,
		"p_tier":       m.Tier,
		"p_bucket":     m.Bucket,
		"p_vcpu":       m.VCPUSeconds,
		"p_ram":        m.RAMGBSeconds,
		"p_disk":       m.DiskGBSeconds,
	}
	body, err := json.Marshal(args)
	if err != nil {
		slog.Warn("usage meter marshal failed", "err", err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/rest/v1/rpc/meter_accrue", bytes.NewReader(body))
	if err != nil {
		slog.Warn("usage meter request build failed", "err", err)
		return
	}
	req.Header.Set("apikey", c.serviceRoleKey)
	req.Header.Set("Authorization", "Bearer "+c.serviceRoleKey)
	req.Header.Set("Content-Type", "application/json")
	c.doSandbox(req, "meter")
}

// SandboxRef is a minimal sandbox identity + state for startup reconciliation.
type SandboxRef struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// ListSandboxesByState returns the id+state of sandboxes currently in the given states, used
// on startup to reconcile against the live store (destroy ghosts, sync stale states).
func (c *Client) ListSandboxesByState(ctx context.Context, states []string) ([]SandboxRef, error) {
	url := c.baseURL + "/rest/v1/sandboxes?select=id,state&state=in.(" + strings.Join(states, ",") + ")"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", c.serviceRoleKey)
	req.Header.Set("Authorization", "Bearer "+c.serviceRoleKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("list sandboxes status %d", resp.StatusCode)
	}
	var refs []SandboxRef
	if err := json.NewDecoder(resp.Body).Decode(&refs); err != nil {
		return nil, err
	}
	return refs, nil
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
