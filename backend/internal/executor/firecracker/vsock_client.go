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
	Mode     string `json:"mode"`
	Timeout  int    `json:"timeout"` // seconds
}

// ExecuteResponse matches the guest agent's ExecutionResponse
type ExecuteResponse struct {
	Stdout   string  `json:"stdout"`
	Stderr   string  `json:"stderr"`
	ExitCode int     `json:"exit_code"`
	Duration float64 `json:"duration"`
}

// ConfigureNetwork sends a configure_network message to the guest agent,
// which reconfigures eth0 directly in Go without spawning bash.
func (v *VsockClient) ConfigureNetwork(guestIP, gwIP string) error {
	var (
		conn net.Conn
		err  error
		buf  = make([]byte, 32)
		n    int
	)
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(100 * time.Millisecond)
		}
		conn, err = net.DialTimeout("unix", v.socketPath, v.timeout)
		if err != nil {
			continue
		}
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err = conn.Write([]byte("CONNECT 52\n")); err != nil {
			conn.Close()
			continue
		}
		n, err = conn.Read(buf)
		if err != nil {
			conn.Close()
			continue
		}
		break
	}
	if err != nil {
		return fmt.Errorf("vsock handshake failed: %w", err)
	}
	defer conn.Close()

	if n < 2 || string(buf[:2]) != "OK" {
		return fmt.Errorf("vsock handshake rejected: %s", string(buf[:n]))
	}

	req := struct {
		Type    string `json:"type"`
		GuestIP string `json:"guest_ip"`
		GWIP    string `json:"gw_ip"`
	}{
		Type:    "configure_network",
		GuestIP: guestIP,
		GWIP:    gwIP,
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return fmt.Errorf("send configure_network: %w", err)
	}

	var resp struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return fmt.Errorf("read configure_network response: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("configure_network: %s", resp.Error)
	}
	return nil
}

func NewVsockClient(socketPath string) *VsockClient {
	return &VsockClient{
		socketPath: socketPath,
		timeout:    60 * time.Second,
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

func (v *VsockClient) Execute(code, language, mode string, timeoutSec int) (*ExecuteResponse, error) {
	// Retry the handshake up to 3 times with 100ms gaps.
	// Firecracker's vsock proxy can transiently reject connections (EOF on
	// handshake read) between consecutive requests — a brief wait resolves it.
	// Retrying is safe here because code only executes AFTER a successful
	// handshake, so a failed handshake means no code ran yet.
	var (
		conn net.Conn
		err  error
		buf  = make([]byte, 32)
		n    int
	)
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(100 * time.Millisecond)
		}

		conn, err = net.DialTimeout("unix", v.socketPath, v.timeout)
		if err != nil {
			continue
		}

		conn.SetDeadline(time.Now().Add(v.timeout))

		if _, err = conn.Write([]byte("CONNECT 52\n")); err != nil {
			conn.Close()
			continue
		}

		n, err = conn.Read(buf)
		if err != nil {
			conn.Close()
			continue
		}

		// Handshake succeeded — break and proceed with execution
		break
	}

	if err != nil {
		return nil, fmt.Errorf("failed to read vsock handshake response: %w", err)
	}
	defer conn.Close()

	response := string(buf[:n])
	if len(response) < 2 || response[:2] != "OK" {
		return nil, fmt.Errorf("vsock handshake failed: %s", response)
	}

	req := ExecuteRequest{
		Code:     code,
		Language: language,
		Timeout:  timeoutSec,
		Mode:     mode,
	}

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	var resp ExecuteResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	return &resp, nil
}
