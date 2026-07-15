// Package worker exposes the execution engine (session.Service) over a private
// HTTP API that the control plane dispatches to. Auth here is an internal shared
// token, not user auth — the control plane has already authenticated the user.
package worker

import "backend/internal/executor"

// CreateRequest is what the control plane sends to boot a sandbox on this worker.
type CreateRequest struct {
	UserID       string            `json:"user_id"`
	BillingModel string            `json:"billing_model"`
	Env          map[string]string `json:"env,omitempty"`
	VCPUs        int               `json:"vcpus"`
	MemoryMB     int               `json:"memory_mb"`
	DiskGB       int               `json:"disk_gb"`
	Internet     bool              `json:"internet"`
	IdleTimeoutS int               `json:"idle_timeout_s,omitempty"`
	MaxLifetimeS int               `json:"max_lifetime_s,omitempty"`
}

// CreateResponse is the worker's reply after booting a sandbox.
type CreateResponse struct {
	SandboxID string `json:"sandbox_id"`
	State     string `json:"state"`
	VCPUs     int    `json:"vcpus"`
	MemoryMB  int    `json:"memory_mb"`
	DiskGB    int    `json:"disk_gb"`
}

// RunRequest runs code in a sandbox.
type RunRequest struct {
	Code     string `json:"code"`
	Language string `json:"language"`
	TimeoutS int    `json:"timeout_s,omitempty"`
}

// ExecRequest runs a shell command in a sandbox.
type ExecRequest struct {
	Command  string `json:"command"`
	TimeoutS int    `json:"timeout_s,omitempty"`
}

// ExecResult mirrors the executor result returned to the control plane.
type ExecResult = executor.ExecutionResult

// Capacity reports the worker's free capacity (for the control-plane scheduler).
type Capacity struct {
	FreeSlots int `json:"free_slots"`
	MaxSlots  int `json:"max_slots"`
}

// ErrorResponse is a structured worker error.
type ErrorResponse struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}
