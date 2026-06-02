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
func (v *VsockClient) Ping() (ok bool, stage string, elapsed time.Duration) {
	t := time.Now()
	conn, err := net.DialTimeout("unix", v.socketPath, 2*time.Second)
	if err != nil {
		return false, "connect", time.Since(t)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err = conn.Write([]byte("CONNECT 52\n")); err != nil {
		return false, "write", time.Since(t)
	}
	buf := make([]byte, 32)
	n, err := conn.Read(buf)
	if err != nil || n < 2 {
		return false, "read", time.Since(t)
	}
	if string(buf[:2]) != "OK" {
		return false, "handshake", time.Since(t)
	}
	return true, "ok", time.Since(t)
}

func (v *VsockClient) SetEnv(env map[string]string) error {
	conn, err := v.dial()
	if err != nil {
		return err
	}
	defer conn.Close()

	req := struct {
		Type string            `json:"type"`
		Env  map[string]string `json:"env"`
	}{Type: "set_env", Env: env}

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return fmt.Errorf("send set_env: %w", err)
	}

	var resp struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return fmt.Errorf("read set_env response: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("set_env failed: %s", resp.Error)
	}
	return nil
}

func (v *VsockClient) Exec(command string, timeoutSec int) (*ExecuteResponse, error) {
	conn, err := v.dial()
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	req := struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}{Type: "exec", Command: command, Timeout: timeoutSec}

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, fmt.Errorf("send exec request: %w", err)
	}

	var resp ExecuteResponse
	conn.SetDeadline(time.Now().Add(time.Duration(timeoutSec+10) * time.Second))
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("read exec response: %w", err)
	}
	return &resp, nil
}

// Connect opens a persistent vsock connection to the guest agent.
// The caller is responsible for closing the connection when done (e.g. on session destroy).
func (v *VsockClient) Connect() (net.Conn, error) {
	return v.dial()
}

// SetEnvOnConn sends a set_env message on an existing persistent connection.
func (v *VsockClient) SetEnvOnConn(conn net.Conn, env map[string]string) error {
	req := struct {
		Type string            `json:"type"`
		Env  map[string]string `json:"env"`
	}{Type: "set_env", Env: env}

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return fmt.Errorf("send set_env: %w", err)
	}

	var resp struct {
		Success bool   `json:"success"`
		Error   string `json:"error,omitempty"`
	}
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return fmt.Errorf("read set_env response: %w", err)
	}
	conn.SetDeadline(time.Time{})
	if !resp.Success {
		return fmt.Errorf("set_env failed: %s", resp.Error)
	}
	return nil
}

// ExecOnConn runs a shell command on an existing persistent connection.
func (v *VsockClient) ExecOnConn(conn net.Conn, command string, timeoutSec int) (*ExecuteResponse, error) {
	req := struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}{Type: "exec", Command: command, Timeout: timeoutSec}

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, fmt.Errorf("send exec request: %w", err)
	}

	var resp ExecuteResponse
	conn.SetDeadline(time.Now().Add(time.Duration(timeoutSec+10) * time.Second))
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("read exec response: %w", err)
	}
	conn.SetDeadline(time.Time{})
	return &resp, nil
}

// ExecuteOnConn runs code on an existing persistent connection.
func (v *VsockClient) ExecuteOnConn(conn net.Conn, code, language, mode string, timeoutSec int) (*ExecuteResponse, error) {
	req := ExecuteRequest{
		Code:     code,
		Language: language,
		Timeout:  timeoutSec,
		Mode:     mode,
	}

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, fmt.Errorf("send execute request: %w", err)
	}

	var resp ExecuteResponse
	conn.SetDeadline(time.Now().Add(time.Duration(timeoutSec+10) * time.Second))
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return nil, fmt.Errorf("read execute response: %w", err)
	}
	conn.SetDeadline(time.Time{})
	return &resp, nil
}

// dial performs the vsock handshake and returns a ready connection.
func (v *VsockClient) dial() (net.Conn, error) {
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
		break
	}
	if err != nil {
		return nil, fmt.Errorf("vsock handshake failed: %w", err)
	}
	if n < 2 || string(buf[:2]) != "OK" {
		conn.Close()
		return nil, fmt.Errorf("vsock handshake rejected: %s", string(buf[:n]))
	}
	return conn, nil
}

func (v *VsockClient) Execute(code, language, mode string, timeoutSec int) (*ExecuteResponse, error) {
	conn, err := v.dial()
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}
	defer conn.Close()

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
