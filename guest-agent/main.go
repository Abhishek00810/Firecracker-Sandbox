package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	DefaultTimeout = 15 * time.Second
	MaxTimeout     = 60 * time.Second
)

// request from host
type ExecutionRequest struct {
	Code     string `json:"code"`
	Language string `json:"language"`
	Timeout  int    `json:"timeout"`
	Mode     string `json:"mode"`
}

// response to host
type ExecutionResponse struct {
	Stdout   string  `json:"stdout"`
	Stderr   string  `json:"stderr"`
	ExitCode int     `json:"exit_code"`
	Duration float64 `json:"duration"`
}

// ─── Kernel Bridge ────────────────────────────────────────────────────────────

// KernelBridge wraps a long-lived kernel_bridge.py process for one language.
// Requests are serialized by the session manager (sess.mu) so no extra
// locking is needed here.
type KernelBridge struct {
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	stdout       *bufio.Scanner
	bridgeStderr *bytes.Buffer // captures kernel_bridge.py stderr for error reporting
}

func newKernelBridge(language string) (*KernelBridge, error) {
	cmd := exec.Command("/usr/bin/python3", "/usr/local/bin/kernel_bridge.py", language)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderrBuf := new(bytes.Buffer)
	cmd.Stderr = io.MultiWriter(log.Writer(), stderrBuf)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start kernel_bridge.py: %w", err)
	}

	log.Printf("kernel bridge started language=%s pid=%d", language, cmd.Process.Pid)

	return &KernelBridge{
		cmd:          cmd,
		stdin:        stdin,
		stdout:       bufio.NewScanner(stdoutPipe),
		bridgeStderr: stderrBuf,
	}, nil
}

func (kb *KernelBridge) execute(code string, timeoutSec int) (*ExecutionResponse, error) {
	req := map[string]interface{}{
		"code":    code,
		"timeout": timeoutSec,
	}
	data, _ := json.Marshal(req)
	data = append(data, '\n')
	log.Printf("kernel bridge execute begin pid=%d timeout=%ds code_len=%d", kb.cmd.Process.Pid, timeoutSec, len(code))

	if _, err := kb.stdin.Write(data); err != nil {
		log.Printf("kernel bridge stdin write failed pid=%d err=%v", kb.cmd.Process.Pid, err)
		return nil, fmt.Errorf("write to bridge: %w", err)
	}
	log.Printf("kernel bridge stdin write ok pid=%d bytes=%d", kb.cmd.Process.Pid, len(data))

	type scanResult struct {
		resp *ExecutionResponse
		err  error
	}
	ch := make(chan scanResult, 1)
	go func() {
		log.Printf("kernel bridge waiting for stdout pid=%d", kb.cmd.Process.Pid)
		if !kb.stdout.Scan() {
			if err := kb.stdout.Err(); err != nil {
				log.Printf("kernel bridge stdout scan error pid=%d err=%v", kb.cmd.Process.Pid, err)
				ch <- scanResult{nil, fmt.Errorf("bridge stdout: %w", err)}
			} else {
				log.Printf("kernel bridge stdout closed pid=%d stderr=%q", kb.cmd.Process.Pid, kb.bridgeStderr.String())
				ch <- scanResult{nil, fmt.Errorf("bridge process closed: %s", kb.bridgeStderr.String())}
			}
			return
		}
		log.Printf("kernel bridge stdout line received pid=%d bytes=%d", kb.cmd.Process.Pid, len(kb.stdout.Bytes()))
		var resp ExecutionResponse
		if err := json.Unmarshal(kb.stdout.Bytes(), &resp); err != nil {
			log.Printf("kernel bridge decode failed pid=%d payload=%q err=%v", kb.cmd.Process.Pid, string(kb.stdout.Bytes()), err)
			ch <- scanResult{nil, fmt.Errorf("decode bridge response: %w", err)}
			return
		}
		ch <- scanResult{&resp, nil}
	}()

	deadline := time.Duration(timeoutSec+5) * time.Second
	select {
	case r := <-ch:
		if r.err == nil && r.resp != nil {
			log.Printf("kernel bridge execute complete pid=%d exit_code=%d duration=%.3fs", kb.cmd.Process.Pid, r.resp.ExitCode, r.resp.Duration)
		}
		return r.resp, r.err
	case <-time.After(deadline):
		if kb.cmd != nil && kb.cmd.Process != nil {
			log.Printf("kernel bridge timeout pid=%d deadline=%v stderr=%q", kb.cmd.Process.Pid, deadline, kb.bridgeStderr.String())
			kb.cmd.Process.Kill()
		}
		return nil, fmt.Errorf("kernel bridge response timeout after %v", deadline)
	}
}

// ─── Kernel Manager ───────────────────────────────────────────────────────────

// kernelLanguages are routed through kernel_bridge.py for persistent state.
var kernelLanguages = map[string]bool{
	"python": true,
}

var (
	kernelsMu sync.Mutex
	kernels   = map[string]*KernelBridge{}
)

func getOrStartKernel(language string) (*KernelBridge, error) {
	kernelsMu.Lock()
	defer kernelsMu.Unlock()

	if kb, ok := kernels[language]; ok {
		slog.Info("kernel: found existing", "lang", language) // ← add this
		return kb, nil
	}
	slog.Info("kernel: starting new", "lang", language) // ← add this

	kb, err := newKernelBridge(language)
	if err != nil {
		return nil, err
	}

	kernels[language] = kb
	return kb, nil
}

func evictKernel(language string) {
	kernelsMu.Lock()
	delete(kernels, language)
	kernelsMu.Unlock()
}

// ─── Node Bridge ──────────────────────────────────────────────────────────────

// NodeBridge wraps a long-lived node_bridge.js process.
// Same stdin/stdout JSON protocol as KernelBridge.
type NodeBridge struct {
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	stdout       *bufio.Scanner
	bridgeStderr *bytes.Buffer
}

func newNodeBridge() (*NodeBridge, error) {
	cmd := exec.Command("/usr/bin/node", "/usr/local/bin/node_bridge.js")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	stderrBuf := new(bytes.Buffer)
	cmd.Stderr = io.MultiWriter(log.Writer(), stderrBuf)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start node_bridge.js: %w", err)
	}

	log.Printf("node bridge started pid=%d", cmd.Process.Pid)

	return &NodeBridge{
		cmd:          cmd,
		stdin:        stdin,
		stdout:       bufio.NewScanner(stdoutPipe),
		bridgeStderr: stderrBuf,
	}, nil
}

func (nb *NodeBridge) execute(code string, timeoutSec int) (*ExecutionResponse, error) {
	req := map[string]interface{}{
		"code":    code,
		"timeout": timeoutSec,
	}
	data, _ := json.Marshal(req)
	data = append(data, '\n')
	log.Printf("node bridge execute begin pid=%d timeout=%ds code_len=%d", nb.cmd.Process.Pid, timeoutSec, len(code))

	if _, err := nb.stdin.Write(data); err != nil {
		log.Printf("node bridge stdin write failed pid=%d err=%v", nb.cmd.Process.Pid, err)
		return nil, fmt.Errorf("write to node bridge: %w", err)
	}
	log.Printf("node bridge stdin write ok pid=%d bytes=%d", nb.cmd.Process.Pid, len(data))

	type scanResult struct {
		resp *ExecutionResponse
		err  error
	}
	ch := make(chan scanResult, 1)
	go func() {
		log.Printf("node bridge waiting for stdout pid=%d", nb.cmd.Process.Pid)
		if !nb.stdout.Scan() {
			if err := nb.stdout.Err(); err != nil {
				log.Printf("node bridge stdout scan error pid=%d err=%v", nb.cmd.Process.Pid, err)
				ch <- scanResult{nil, fmt.Errorf("node bridge stdout: %w", err)}
			} else {
				log.Printf("node bridge stdout closed pid=%d stderr=%q", nb.cmd.Process.Pid, nb.bridgeStderr.String())
				ch <- scanResult{nil, fmt.Errorf("node bridge process closed: %s", nb.bridgeStderr.String())}
			}
			return
		}
		log.Printf("node bridge stdout line received pid=%d bytes=%d", nb.cmd.Process.Pid, len(nb.stdout.Bytes()))
		var resp ExecutionResponse
		if err := json.Unmarshal(nb.stdout.Bytes(), &resp); err != nil {
			log.Printf("node bridge decode failed pid=%d payload=%q err=%v", nb.cmd.Process.Pid, string(nb.stdout.Bytes()), err)
			ch <- scanResult{nil, fmt.Errorf("decode node bridge response: %w", err)}
			return
		}
		ch <- scanResult{&resp, nil}
	}()

	deadline := time.Duration(timeoutSec+5) * time.Second
	select {
	case r := <-ch:
		if r.err == nil && r.resp != nil {
			log.Printf("node bridge execute complete pid=%d exit_code=%d duration=%.3fs", nb.cmd.Process.Pid, r.resp.ExitCode, r.resp.Duration)
		}
		return r.resp, r.err
	case <-time.After(deadline):
		if nb.cmd != nil && nb.cmd.Process != nil {
			log.Printf("node bridge timeout pid=%d deadline=%v stderr=%q", nb.cmd.Process.Pid, deadline, nb.bridgeStderr.String())
			nb.cmd.Process.Kill()
		}
		return nil, fmt.Errorf("node bridge response timeout after %v", deadline)
	}
}

var (
	nodeBridgeMu sync.Mutex
	nodeBridge   *NodeBridge
)

func getOrStartNodeBridge() (*NodeBridge, error) {
	nodeBridgeMu.Lock()
	defer nodeBridgeMu.Unlock()

	if nodeBridge != nil {
		slog.Info("node bridge: found existing")
		return nodeBridge, nil
	}
	slog.Info("node bridge: starting new")

	nb, err := newNodeBridge()
	if err != nil {
		return nil, err
	}

	nodeBridge = nb
	return nb, nil
}

func evictNodeBridge() {
	nodeBridgeMu.Lock()
	nodeBridge = nil
	nodeBridgeMu.Unlock()
}

// ─── Execution Router ─────────────────────────────────────────────────────────

func executeCode(req ExecutionRequest) ExecutionResponse {
	startTime := time.Now()

	// node   → persistent node_bridge.js (vm module, no ZMQ)
	// python → persistent Jupyter kernel via kernel_bridge.py
	// bash   → direct exec (new process per call, no state)
	if req.Language == "node" {
		log.Printf("executeCode node branch mode=%s", req.Mode)
		return executeViaNodeBridge(req, startTime)
	}
	if req.Language == "python" {
		if req.Mode == "stateful" {
			return executeViaKernel(req, startTime)
		}
	}
	return executeDirect(req, startTime)
}

func executeViaNodeBridge(req ExecutionRequest, startTime time.Time) ExecutionResponse {
	timeout := int(MaxTimeout.Seconds())
	if req.Timeout > 0 {
		timeout = req.Timeout
		if timeout > int(MaxTimeout.Seconds()) {
			timeout = int(MaxTimeout.Seconds())
		}
	}

	nb, err := getOrStartNodeBridge()
	if err != nil {
		log.Printf("node bridge start failed err=%v", err)
		return ExecutionResponse{
			Stderr:   fmt.Sprintf("failed to start node bridge: %v", err),
			ExitCode: -1,
			Duration: time.Since(startTime).Seconds(),
		}
	}

	resp, err := nb.execute(req.Code, timeout)
	if err != nil {
		evictNodeBridge()
		log.Printf("node bridge execution failed err=%v — evicted", err)
		return ExecutionResponse{
			Stderr:   fmt.Sprintf("node bridge error: %v", err),
			ExitCode: -1,
			Duration: time.Since(startTime).Seconds(),
		}
	}

	resp.Duration = time.Since(startTime).Seconds()
	return *resp
}

func executeViaKernel(req ExecutionRequest, startTime time.Time) ExecutionResponse {
	timeout := int(MaxTimeout.Seconds())
	if req.Timeout > 0 {
		timeout = req.Timeout
		if timeout > int(MaxTimeout.Seconds()) {
			timeout = int(MaxTimeout.Seconds())
		}
	}

	kb, err := getOrStartKernel(req.Language)
	if err != nil {
		log.Printf("kernel start failed language=%s err=%v", req.Language, err)
		return ExecutionResponse{
			Stderr:   fmt.Sprintf("failed to start kernel: %v", err),
			ExitCode: -1,
			Duration: time.Since(startTime).Seconds(),
		}
	}

	resp, err := kb.execute(req.Code, timeout)
	if err != nil {
		// kernel died — evict so next request restarts it
		evictKernel(req.Language)
		log.Printf("kernel execution failed language=%s err=%v — evicted", req.Language, err)
		return ExecutionResponse{
			Stderr:   fmt.Sprintf("kernel error: %v", err),
			ExitCode: -1,
			Duration: time.Since(startTime).Seconds(),
		}
	}

	resp.Duration = time.Since(startTime).Seconds()
	return *resp
}

func executeDirect(req ExecutionRequest, startTime time.Time) ExecutionResponse {
	log.Printf("executeDirect invoked language=%s mode=%s", req.Language, req.Mode)
	var cmd *exec.Cmd

	// Use absolute paths since we're running as PID 1 without proper PATH
	switch req.Language {
	case "bash":
		cmd = exec.Command("/bin/bash", "-c", req.Code)
	case "python":
		cmd = exec.Command("/usr/bin/python3", "-c", req.Code)
	case "node":
		cmd = exec.Command("/usr/bin/node", "-e", req.Code)
	default:
		return ExecutionResponse{
			Stderr:   fmt.Sprintf("unsupported language: %s", req.Language),
			ExitCode: 1,
			Duration: time.Since(startTime).Seconds(),
		}
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return ExecutionResponse{
			Stderr:   fmt.Sprintf("failed to start: %v", err),
			ExitCode: -1,
			Duration: time.Since(startTime).Seconds(),
		}
	}
	log.Printf("executeDirect started language=%s pid=%d timeout_req=%d", req.Language, cmd.Process.Pid, req.Timeout)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timeout := DefaultTimeout
	if req.Timeout > 0 {
		timeout = time.Duration(req.Timeout) * time.Second
		if timeout > MaxTimeout {
			timeout = MaxTimeout
		}
	}

	exitCode := 0
	select {
	case err := <-done:
		log.Printf("executeDirect finished language=%s pid=%d duration_ms=%d err=%v", req.Language, cmd.Process.Pid, time.Since(startTime).Milliseconds(), err)
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
		}
	case <-time.After(timeout):
		log.Printf("process timeout after %v, killing...", timeout)
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		cmd.Wait()
		log.Printf("executeDirect timed out language=%s pid=%d duration_ms=%d", req.Language, cmd.Process.Pid, time.Since(startTime).Milliseconds())
		return ExecutionResponse{
			Stdout:   stdout.String(),
			Stderr:   stderr.String() + fmt.Sprintf("\n[Timeout: Process killed after %v]", timeout),
			ExitCode: -1,
			Duration: time.Since(startTime).Seconds(),
		}
	}

	return ExecutionResponse{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		Duration: time.Since(startTime).Seconds(),
	}
}

// ─── Entropy Seeding ─────────────────────────────────────────────────────────

// seedCRNG injects entropy into the kernel's random pool via RNDADDENTROPY
// ioctl. Required in nested-KVM environments (Hyper-V → KVM → Firecracker)
// where virtio-rng fails to deliver entropy through DMA, leaving entropy_avail
// permanently below 128 bits. Without this, Node.js v20+ blocks indefinitely
// in getrandom() during V8 init. Data source is nanosecond timing jitter —
// real unpredictability from CPU scheduling and hypervisor overhead variation.
func seedCRNG() {
	f, err := os.OpenFile("/dev/random", os.O_WRONLY, 0)
	if err != nil {
		log.Printf("seedCRNG: open /dev/random: %v", err)
		return
	}
	defer f.Close()

	// Collect 64 bytes of timing jitter across multiple calls.
	var buf [64]byte
	for i := 0; i < 64; i++ {
		ns := time.Now().UnixNano()
		buf[i] = byte(ns >> (uint(i%8) * 8))
	}

	// struct rnd_pool_info { int entropy_count; int buf_size; __u32 buf[]; }
	// RNDADDENTROPY = _IOW('R', 3, int[2]) = 0x40085203
	type randPoolInfo struct {
		entropyCount int32
		size         int32
		buf          [64]byte
	}
	rpi := randPoolInfo{
		entropyCount: 512, // claim 512 bits — well above the 128 needed for crng_init=2
		size:         64,
		buf:          buf,
	}

	const RNDADDENTROPY = 0x40085203
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		f.Fd(),
		RNDADDENTROPY,
		uintptr(unsafe.Pointer(&rpi)),
	)
	if errno != 0 {
		log.Printf("seedCRNG: RNDADDENTROPY ioctl failed: %v", errno)
		return
	}
	log.Printf("seedCRNG: injected 512 bits into entropy pool")
}

// ─── Vsock Listener ───────────────────────────────────────────────────────────

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
	conn := &fdConn{fd: connFd}
	log.Printf("connection accepted fd=%d", connFd)

	var req ExecutionRequest
	log.Println("waiting for request...")
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		log.Printf("decode request: %v", err)
		json.NewEncoder(conn).Encode(ExecutionResponse{
			Stderr:   fmt.Sprintf("failed to decode request: %v", err),
			ExitCode: -1,
		})
		return
	}

	log.Printf("execute language=%s code_len=%d", req.Language, len(req.Code))
	resp := executeCode(req)
	log.Printf("done exit_code=%d duration=%.2fs", resp.ExitCode, resp.Duration)

	if err := json.NewEncoder(conn).Encode(resp); err != nil {
		log.Printf("encode response: %v", err)
	} else {
		log.Printf("response encoded fd=%d", connFd)
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

// readNetworkConfig reads guest IP and gateway from /etc/vm-network.env,
// which the host writes into the rootfs copy before, Firecracker starts.
func readNetworkConfig() (guestIP, gwIP string) {
	data, err := os.ReadFile("/etc/vm-network.env")
	if err != nil {
		log.Printf("no /etc/vm-network.env: %v", err)
		return "", ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		switch parts[0] {
		case "GUESTIP":
			guestIP = parts[1]
		case "GWIP":
			gwIP = parts[1]
		}
	}
	return
}

func main() {
	// Seed the kernel CRNG immediately. In nested-KVM (Azure/Hyper-V), the
	// virtio-rng device fails to deliver entropy via DMA so entropy_avail
	// stays at ~38 bits forever — below the 128 bits getrandom() requires.
	// This must run before any bridge process starts.
	seedCRNG()

	// bring up loopback interface so 127.0.0.1 works inside the VM
	// required for ZMQ (jupyter kernel ↔ kernel_bridge.py communicate via TCP localhost)
	// 1. assign 127.0.0.1/8 to lo (may already exist, ignore error)
	exec.Command("/sbin/ip", "addr", "add", "127.0.0.1/8", "dev", "lo").Run()
	// 2. bring the interface up
	if out, err := exec.Command("/sbin/ip", "link", "set", "lo", "up").CombinedOutput(); err != nil {
		log.Printf("warning: failed to bring up lo: %v — %s", err, out)
	} else {
		log.Println("loopback interface up (127.0.0.1)")
	}

	// bring up eth0 for outbound internet access (pip install, etc.)
	// IPs are written into /etc/vm-network.env by the host before boot.
	guestIP, gwIP := readNetworkConfig()
	if guestIP != "" && gwIP != "" {
		exec.Command("/sbin/ip", "addr", "add", guestIP+"/30", "dev", "eth0").Run()
		exec.Command("/sbin/ip", "link", "set", "eth0", "up").Run()
		exec.Command("/sbin/ip", "route", "add", "default", "via", gwIP).Run()
		if err := os.WriteFile("/etc/resolv.conf", []byte("nameserver 8.8.8.8\n"), 0644); err != nil {
			log.Printf("warning: failed to write resolv.conf: %v", err)
		}
		log.Printf("network up: guest=%s gateway=%s", guestIP, gwIP)
	} else {
		log.Println("no network config found, eth0 not configured")
	}

	fd, err := listenVsock()
	if err != nil {
		log.Fatalf("failed to listen on vsock: %v", err)
	}

	log.Println("guest agent ready, waiting for connections...")

	for {
		connFd, _, err := unix.Accept(fd)
		if err != nil {
			log.Printf("accept failed (%v) — reinitializing vsock listener", err)
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

		handleConnection(connFd)
		unix.Close(connFd)
	}
}
