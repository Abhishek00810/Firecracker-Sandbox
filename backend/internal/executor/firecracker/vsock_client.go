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

// Ping tests whether the guest agent is ready by doing the vsock handshake
// and checking for an "OK" response. Used by the pool to verify a VM is
// actually accepting connections before it is made available for requests.
func (v *VsockClient) Ping() bool {
	conn, err := net.DialTimeout("unix", v.socketPath, 2*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err = conn.Write([]byte("CONNECT 52\n")); err != nil {
		return false
	}
	buf := make([]byte, 32)
	n, err := conn.Read(buf)
	if err != nil || n < 2 {
		return false
	}
	return string(buf[:2]) == "OK"
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

	// CRITICAL: Send Firecracker vsock handshake to connect to guest port 52
	// Format: "CONNECT <port>\n"
	_, err = conn.Write([]byte("CONNECT 52\n"))
	if err != nil {
		return nil, fmt.Errorf("failed to send vsock handshake: %w", err)
	}

	// Read the response (should be "OK <port>\n" on success)
	buf := make([]byte, 32)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("failed to read vsock handshake response: %w", err)
	}
	response := string(buf[:n])
	if len(response) < 2 || response[:2] != "OK" {
		return nil, fmt.Errorf("vsock handshake failed: %s", response)
	}

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
