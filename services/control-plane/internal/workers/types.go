package workers

import (
	"context"
	"errors"
)

var (
	ErrWorkerNotFound = errors.New("worker not found")
	ErrWorkerRequest  = errors.New("worker request failed")
)

// Endpoint addresses a single worker.
type Endpoint struct {
	WorkerID string
	BaseURL  string
}

// Registry resolves a worker id to its endpoint.
type Registry interface {
	GetEndpoint(ctx context.Context, workerID string) (Endpoint, error)
}

// ---- Contract types: mirror the worker's private HTTP API. They deliberately
// carry NO billing/pricing fields — the worker is billing-agnostic; pricing
// stays control-plane only. ----

// CreateRequest boots a sandbox on a worker (POST /worker/sandbox).
type CreateRequest struct {
	UserID       string            `json:"user_id"`
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

// RunRequest runs code (POST /worker/sandbox/{id}/run).
type RunRequest struct {
	Code     string `json:"code"`
	Language string `json:"language"`
	TimeoutS int    `json:"timeout_s,omitempty"`
}

// ExecRequest runs a shell command (POST /worker/sandbox/{id}/exec).
type ExecRequest struct {
	Command  string `json:"command"`
	TimeoutS int    `json:"timeout_s,omitempty"`
}

// ExecuteResult is the outcome of a run or exec (mirrors executor.ExecutionResult).
type ExecuteResult struct {
	Stdout            string  `json:"stdout"`
	Stderr            string  `json:"stderr"`
	Duration          float64 `json:"duration"`
	GuestDuration     float64 `json:"guest_duration"`
	ExitCode          int64   `json:"exit_code"`
	TerminationReason string  `json:"termination_reason,omitempty"`
}

// Capacity is a worker's free/total slot report (for the scheduler).
type Capacity struct {
	FreeSlots int `json:"free_slots"`
	MaxSlots  int `json:"max_slots"`
}

// ---- Role interfaces (segregated so each consumer depends only on what it
// uses). The concrete HTTP adapter satisfies Client, i.e. all roles. ----

// Creator boots sandboxes.
type Creator interface {
	Create(ctx context.Context, endpoint Endpoint, req CreateRequest) (CreateResponse, error)
}

// Runner runs code in a sandbox.
type Runner interface {
	Run(ctx context.Context, endpoint Endpoint, sandboxID string, req RunRequest) (ExecuteResult, error)
}

// Executor runs shell commands in a sandbox.
type Executor interface {
	Exec(ctx context.Context, endpoint Endpoint, sandboxID string, req ExecRequest) (ExecuteResult, error)
}

// Lifecycle pauses, resumes, and destroys sandboxes.
type Lifecycle interface {
	Pause(ctx context.Context, endpoint Endpoint, sandboxID string) error
	Resume(ctx context.Context, endpoint Endpoint, sandboxID string) error
	Destroy(ctx context.Context, endpoint Endpoint, sandboxID string) error
}

// Prober reports worker capacity and health (for the scheduler/heartbeat).
type Prober interface {
	Capacity(ctx context.Context, endpoint Endpoint) (Capacity, error)
	Health(ctx context.Context, endpoint Endpoint) error
}

// Client is the full worker client. The concrete HTTP adapter implements it;
// individual services depend on the narrower role interfaces above.
type Client interface {
	Creator
	Runner
	Executor
	Lifecycle
	Prober
}
