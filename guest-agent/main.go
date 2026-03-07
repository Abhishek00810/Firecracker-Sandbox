package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	DefaultTimeout = 15 * time.Second
	MaxTimeout     = 30 * time.Second
)

// request from host
type ExecutionRequest struct {
	Code     string `json:"code"`
	Language string `json:"language"`
	Timeout  int    `json:"timeout"`
}

// response to host
type ExecutionResponse struct {
	Stdout   string  `json:"stdout"`
	Stderr   string  `json:"stderr"`
	ExitCode int     `json:"exit_code"`
	Duration float64 `json:"duration"`
}

// fdConn wraps a file descriptor to implement io.Reader/Writer
type fdConn struct {
	fd int
}

func (c *fdConn) Read(p []byte) (n int, err error) {
	n, err = unix.Read(c.fd, p)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

func (c *fdConn) Write(p []byte) (n int, err error) {
	return unix.Write(c.fd, p)
}

func handleConnection(connFd int) {
	// NOTE: Connection is closed by caller, not here

	// wrap fd into a file easier reading
	conn := &fdConn{fd: connFd}

	var req ExecutionRequest
	decoder := json.NewDecoder(conn)

	log.Println("About to decode JSON request...")
	if err := decoder.Decode(&req); err != nil {
		log.Printf("ERROR: Failed to decode request: %v", err)
		// Send error response instead of just returning
		errResp := ExecutionResponse{
			Stdout:   "",
			Stderr:   fmt.Sprintf("Failed to decode request: %v", err),
			ExitCode: -1,
			Duration: 0,
		}
		encoder := json.NewEncoder(conn)
		encoder.Encode(errResp)
		return
	}

	log.Printf("Received request: language=%s, code_len=%d", req.Language, len(req.Code))

	resp := executeCode(req)

	log.Printf("Execution completed, sending response...")

	encoder := json.NewEncoder(conn)
	err := encoder.Encode(resp)
	if err != nil {
		log.Printf("ERROR: Failed to encode response: %v", err)
		return
	}

	log.Printf("Response sent successfully: exit_code=%d, stdout_len=%d, stderr_len=%d", resp.ExitCode, len(resp.Stdout), len(resp.Stderr))
}

func executeCode(req ExecutionRequest) ExecutionResponse {
	startTime := time.Now()

	var cmd *exec.Cmd

	// route to correct runtime based on language
	// Use absolute paths since we're running as PID 1 without proper PATH
	switch req.Language {
	case "python":
		cmd = exec.Command("/usr/bin/python3", "-c", req.Code)
	case "node":
		cmd = exec.Command("/usr/bin/node", "-e", req.Code)
	case "bash":
		cmd = exec.Command("/bin/bash", "-c", req.Code)
	default:
		return ExecutionResponse{
			Stdout:   "",
			Stderr:   fmt.Sprintf("Unsupported language: %s", req.Language),
			ExitCode: 1,
			Duration: 0.0,
		}
	}

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}

	if err := cmd.Start(); err != nil {
		return ExecutionResponse{
			Stdout:   "",
			Stderr:   fmt.Sprintf("Failed to start: %v", err),
			ExitCode: -1,
			Duration: time.Since(startTime).Seconds(),
		}
	}

	done := make(chan error, 1)

	//timeout logic

	timeout := DefaultTimeout

	if req.Timeout > 0 {
		timeout = time.Duration(req.Timeout) * time.Second

		if timeout > MaxTimeout {
			timeout = MaxTimeout
		}
	}

	go func() {
		done <- cmd.Wait()
	}()

	exitCode := 0

	select {
	case err := <-done:
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
		} else {
			exitCode = 0
		}
	case <-time.After(timeout):
		// TIMEOUT - Kill process group
		log.Printf("Process timeout after %v, killing...", timeout)

		// Kill entire process group (negative PID = process group)
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			log.Printf("Failed to kill process group: %v", err)
		}

		// Wait for cleanup
		cmd.Wait()

		return ExecutionResponse{
			Stdout:   stdout.String(),
			Stderr:   stderr.String() + fmt.Sprintf("\n[Timeout: Process killed after %v]", timeout),
			ExitCode: -1,
			Duration: time.Since(startTime).Seconds(),
		}
	}

	duration := time.Since(startTime).Seconds()

	return ExecutionResponse{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		Duration: duration,
	}

}

func listenVsock() (int, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return -1, fmt.Errorf("socket: %w", err)
	}
	addr := &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: 52}
	if err = unix.Bind(fd, addr); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("bind: %w", err)
	}
	if err = unix.Listen(fd, 1); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("listen: %w", err)
	}
	return fd, nil
}

func main() {
	fd, err := listenVsock()
	if err != nil {
		log.Fatalf("failed to listen on vsock: %v", err)
	}

	log.Println("Guest agent ready, waiting for connections...")

	for {
		log.Println("Waiting for next connection...")

		connFd, _, err := unix.Accept(fd)
		if err != nil {
			// After a VM snapshot restore the virtio-vsock transport is reset.
			// The old fd becomes invalid — Accept will keep failing on it.
			// Close it and create a fresh socket so we can accept again.
			log.Printf("Accept failed (%v) — reinitializing vsock listener", err)
			unix.Close(fd)
			for {
				fd, err = listenVsock()
				if err == nil {
					break
				}
				log.Printf("reinit failed: %v — retrying in 100ms", err)
				time.Sleep(100 * time.Millisecond)
			}
			log.Println("vsock listener reinitialized")
			continue
		}

		log.Println("Connection accepted, handling request...")
		handleConnection(connFd)
		unix.Close(connFd)
		log.Println("Request completed, ready for next connection")
	}
}
