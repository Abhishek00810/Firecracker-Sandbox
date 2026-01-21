package firecracker

import (
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// VsockClient handles communication with the guest agent via vsock
type VsockClient struct {
	socketPath string
	timeout    time.Duration
}

// ExecuteRequest matches the guest agent's ExecutionRequest
type ExecuteRequest struct {
	Code     string `json:"code"`
	Language string `json:"language"`
	Timeout  int    `json:"timeout"` // seconds
}

// ExecuteResponse matches the guest agent's ExecutionResponse
type ExecuteResponse struct {
	Stdout   string  `json:"stdout"`
	Stderr   string  `json:"stderr"`
	ExitCode int     `json:"exit_code"`
	Duration float64 `json:"duration"`
}

func NewVsockClient(socketPath string) *VsockClient {
	return &VsockClient{
		socketPath: socketPath,
		timeout:    20 * time.Second,
	}
}

func (v *VsockClient) Execute(code, language string, timeoutSec int) (*ExecuteResponse, error) {
	// Connect to the Unix socket that Firecracker exposes for vsock
	conn, err := net.DialTimeout("unix", v.socketPath, v.timeout)

	if err != nil {
		return nil, fmt.Errorf("failed to connect to vsock: %w", err)
	}
	defer conn.Close()

	deadline := time.Now().Add(v.timeout)
	conn.SetDeadline(deadline)

	req := ExecuteRequest{
		Code:     code,
		Language: language,
		Timeout:  timeoutSec,
	}

	encoder := json.NewEncoder(conn)
	if err := encoder.Encode(req); err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	var resp ExecuteResponse

	decoder := json.NewDecoder(conn)
	if err := decoder.Decode(&resp); err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	return &resp, nil
}
