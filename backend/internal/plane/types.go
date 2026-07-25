package plane

import "time"

// SessionInfo is the plane-neutral view of a sandbox session — everything the
// control plane may know about one. Host-side runtime state (VM handles,
// snapshots, vsock) lives in internal/session on top of this.
type SessionInfo struct {
	ID               string
	UserID           string
	BillingModel     string
	VCPUs            int
	MemoryMB         int
	DiskGB           int
	Env              map[string]string
	Internet         bool
	IdleTimeout      time.Duration
	MaxLifetime      time.Duration
	CreatedAt        time.Time
	LastUsed         time.Time
	PausedAt         time.Time
	State            SessionState
	RunCount         int
	TotalExecutionMs float64
	LastExitCode     *int
}

// CreateRequest is what the control plane sends to boot a sandbox on an agent.
type CreateRequest struct {
	SandboxID    string            `json:"sandbox_id"`
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

// CreateResponse is the agent's reply after booting a sandbox.
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

// ExecResult is the wire form of an execution outcome. Field tags stay in
// lockstep with executor.ExecutionResult, which the agent encodes directly.
type ExecResult struct {
	Stdout            string  `json:"stdout"`
	Stderr            string  `json:"stderr"`
	Duration          float64 `json:"duration"`
	GuestDuration     float64 `json:"guest_duration"`
	ExitCode          int64   `json:"exit_code"`
	TerminationReason string  `json:"termination_reason,omitempty"`
}

// Capacity reports an agent's free capacity (for the control-plane scheduler).
type Capacity struct {
	FreeSlots int `json:"free_slots"`
	MaxSlots  int `json:"max_slots"`
}

// ErrorResponse is a structured agent error.
type ErrorResponse struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}
