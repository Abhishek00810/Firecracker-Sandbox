package main

import (
	"bytes"
	"encoding/json" // ← MISSING
	"fmt"           // ← MISSING
	"io"            // ← MISSING
	"log"
	"os/exec"
	"syscall" // ← MISSING
	"time"    // ← MISSING

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
	defer unix.Close(connFd)

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
	switch req.Language {
	case "python":
		cmd = exec.Command("python3", "-c", req.Code)
	case "node":
		cmd = exec.Command("node", "-e", req.Code)
	case "bash":
		cmd = exec.Command("bash", "-c", req.Code)
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

func main() {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)

	if err != nil {
		log.Fatal(err)
	}

	defer unix.Close(fd)

	addr := &unix.SockaddrVM{
		CID:  unix.VMADDR_CID_ANY,
		Port: 52,
	}

	err = unix.Bind(fd, addr)
	if err != nil {
		log.Fatalf("failed to bind: %v", err)
	}
	err = unix.Listen(fd, 1)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	log.Println("waiting for connection: backlog is 1 as of now")

	// acccpeting the request
	connFd, _, err := unix.Accept(fd)

	if err != nil {
		log.Fatalf("failed to accept: %v", err)
	}

	defer unix.Close(connFd)

	log.Println("Connection accepted, handling request...")

	handleConnection(connFd)

	log.Println("Request completed, shutting down")

}
