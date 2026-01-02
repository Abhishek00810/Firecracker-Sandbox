package firecracker

import (
	"context"
	"time"
)

type VMState string

const (
	VMStateCreated   VMState = "created"
	VMStateBooting   VMState = "booting"
	VMStateRunning   VMState = "running"
	VMStateStopped   VMState = "stopped"
	VMStateDestroyed VMState = "destroyed"
)

type VMConfig struct {
	VMID       string
	VCPUCount  int
	MemSizeMiB int
	Timeout    time.Duration
	KernelPath string
	RootfsPath string
	BootArgs   string
}

type MicroVM struct {
	ID         string
	Config     VMConfig
	State      VMState
	SocketPath string
	CreatedAt  time.Time
}

type VMManager interface {
	Create(ctx context.Context, cfg VMConfig) (*MicroVM, error)
	Boot(ctx context.Context, vmID string) error
	Stop(ctx context.Context, vmID string) error
	Destroy(ctx context.Context, vmID string) error
}
