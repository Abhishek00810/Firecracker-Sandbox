# 🔥 Firecracker Sandbox Engine

> **A high-performance, secure code execution engine powered by Firecracker microVMs**

Execute untrusted code safely with hardware-level isolation, sub-100ms boot times, and multi-language support. Built for production scale with VM pooling, job queuing, and comprehensive resource limits.

[![Status](https://img.shields.io/badge/status-production--ready-brightgreen)]()
[![Go Version](https://img.shields.io/badge/go-1.25.0-blue)]()
[![Firecracker](https://img.shields.io/badge/firecracker-v1.7.0-orange)]()

---

## ✨ Features

| Feature | Description |
|---------|-------------|
| 🔒 **Hardware Isolation** | Firecracker microVMs provide KVM-based virtualization, not just containers |
| ⚡ **Sub-100ms Boot** | VM pool keeps warm microVMs ready, achieving <100ms cold starts |
| 🎯 **Resource Limits** | 256MB RAM, 2 vCPU, 30s timeout per execution |
| 🌐 **Multi-Language** | Python, Node.js, Bash, Go (extensible architecture) |
| 🔄 **VM Pooling** | Pre-booted VMs drastically reduce latency |
| 📊 **Job Queue** | Buffered queue (100 jobs) with 10 concurrent workers |
| 🛡️ **Network Isolation** | Zero network access - complete air-gapping |
| 🚀 **vsock Communication** | Fast, secure host-guest communication without networking |

---

## 🏗️ Architecture

```
┌─────────────┐
│   Client    │
└──────┬──────┘
       │ HTTP POST /execute
       ▼
┌─────────────────────────────────┐
│      Go REST API (Port 8080)    │
│  ┌──────────┐  ┌──────────────┐ │
│  │ Handlers │→ │ Job Queue    │ │
│  └──────────┘  │ (100 buffer) │ │
│                └──────┬───────┘ │
└───────────────────────┼─────────┘
                        │
         ┌──────────────┼──────────────┐
         │ Worker Pool (10 workers)    │
         └──────────────┬──────────────┘
                        ▼
         ┌──────────────────────────────┐
         │   Firecracker VM Pool (3)    │
         │  ┌────────┐ ┌────────┐       │
         │  │microVM1│ │microVM2│ ...   │
         │  │(ready) │ │(ready) │       │
         │  └────────┘ └────────┘       │
         └──────────────────────────────┘
                        │
                   vsock (secure)
                        ▼
         ┌──────────────────────────────┐
         │   Guest Agent (in VM)        │
         │   - Receives code via vsock  │
         │   - Executes in isolated env │
         │   - Returns stdout/stderr    │
         └──────────────────────────────┘
```

### How It Works

1. **Client submits code** → HTTP POST to `/execute` with `{code, language}`
2. **Request validation** → API validates input, checks language support
3. **Job enqueued** → Job added to buffered channel (fails fast if full)
4. **Worker picks job** → One of 10 workers dequeues the job
5. **VM acquired from pool** → Pre-booted VM grabbed (or boot new if needed)
6. **Code executed via vsock** → Code sent to guest agent, runs isolated
7. **Output collected** → stdout/stderr captured, VM returned to pool
8. **Response returned** → JSON with output, duration, exit code

---

## 🚀 Quick Start

### Prerequisites

- **OS**: Linux (x86_64 or aarch64) with KVM support
- **Go**: 1.25.0 or later
- **Firecracker**: v1.7.0 (included in `release-v1.7.0-aarch64/`)
- **Permissions**: Root/sudo for KVM access
- **Assets**: Kernel (`vmlinux`) and rootfs (`rootfs-alpine.ext4`)

### Installation

```bash
# 1. Clone the repository
git clone <your-repo-url>
cd sandbox_env

# 2. Install Go dependencies
cd backend
go mod download

# 3. Verify Firecracker binary
chmod +x ../release-v1.7.0-aarch64/firecracker-v1.7.0-aarch64

# 4. Check assets exist
ls ../assets/kernel/vmlinux
ls ../assets/rootfs/rootfs-alpine.ext4
```

### Running the Server

```bash
cd backend/cmd/api
sudo go run main.go
```

**Why sudo?** Firecracker requires KVM access (`/dev/kvm`), which typically needs root privileges.

**Expected output:**
```
Firecracker executor initialized successfully!
Server is running on Port 8080 huh!!
```

---

## 📚 Usage Examples

### Health Check

```bash
curl http://localhost:8080/health
```

**Response:**
```json
{
  "status": "ok",
  "message": "Server is healthy and is rocking!!!"
}
```

### Execute Python Code

```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "print(\"Hello from microVM!\")\nfor i in range(5):\n    print(f\"Loop {i}\")",
    "language": "python"
  }'
```

**Response:**
```json
{
  "output": {
    "output": "Hello from microVM!\nLoop 0\nLoop 1\nLoop 2\nLoop 3\nLoop 4\n",
    "duration": 0.087,
    "exit_code": 0,
    "termination_reason": "success"
  },
  "status": "success"
}
```

### Execute Node.js Code

```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "console.log(\"Node.js says hello!\"); console.log(Math.random());",
    "language": "nodejs"
  }'
```

### Execute Bash Script

```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "#!/bin/bash\necho \"System info:\"\nuname -a\necho \"Memory:\"\nfree -h",
    "language": "bash"
  }'
```

For more examples, see [DEMO.md](./DEMO.md).

---

## 📖 Documentation

| Document | Description |
|----------|-------------|
| [ARCHITECTURE.md](./ARCHITECTURE.md) | Detailed system design and component breakdown |
| [API.md](./API.md) | Complete API reference with curl examples |
| [SECURITY.md](./SECURITY.md) | Security model, isolation guarantees, threat analysis |
| [DEMO.md](./DEMO.md) | Sample executions, benchmarks, edge cases |

---

## 🛡️ Security

### Isolation Layers

1. **Hardware Virtualization (KVM)** 
   - Each microVM runs on a separate kernel
   - No shared kernel vulnerabilities
   - Memory isolated at hardware level

2. **Resource Enforcement**
   - 256MB RAM hard limit (OOM killer activated)
   - 2 vCPU maximum
   - 30-second timeout (force-killed)
   - Read-only root filesystem (except `/tmp`)

3. **Network Isolation**
   - **Zero network access** - no TCP/IP stack in VM
   - Only vsock for controlled host communication
   - Prevents data exfiltration, reverse shells

4. **Ephemeral VMs**
   - VMs reset after each execution
   - No state persists between runs
   - Impossible to leave backdoors

### What Can't Malicious Code Do?

❌ Access the internet  
❌ Read files from other executions  
❌ Consume unlimited CPU/memory  
❌ Run longer than 30 seconds  
❌ Escape to the host system  
❌ Attack other microVMs  

See [SECURITY.md](./SECURITY.md) for threat model details.

---

## 📊 Performance Benchmarks

| Metric | Value | Notes |
|--------|-------|-------|
| **Cold Boot Time** | 95-120ms | Fresh VM from scratch |
| **Warm Pool Time** | 20-40ms | VM already booted |
| **Hello World (Python)** | ~85ms | Including queue time |
| **CPU-Intensive (Fibonacci)** | ~2.5s | Respects CPU limits |
| **Memory-Heavy (100MB)** | ~180ms | Allocated successfully |
| **Timeout Test (15s sleep)** | 15.02s | Clean termination |

**Load Test Results:**
- 50 concurrent executions: ✅ All succeeded
- 100 concurrent executions: ✅ Queue handled gracefully
- 150 requests: ~50 queued, rest handled in ~8 seconds

---

## 🔧 Configuration

Key parameters in `main.go`:

```go
config := firecracker.VMConfig{
    VCPUCount:  2,              // CPU cores per VM
    MemSizeMiB: 256,            // RAM limit
    Timeout:    30 * time.Second, // Max execution time
    KernelPath: "assets/kernel/vmlinux",
    RootfsPath: "assets/rootfs/rootfs-alpine.ext4",
}

// VM Pool: 3 pre-booted VMs
pool := firecracker.NewVMPool(3, config, vmManager)

// Job Queue: 10 workers, 100 job buffer
JobQueue := queue.NewJobQueue(firecrackerExec, 10)
```

### Tuning for Your Workload

**For higher throughput:**
- Increase pool size: `NewVMPool(10, ...)`
- Increase workers: `NewJobQueue(exec, 20)`

**For lower latency:**
- Use larger VM pool (more warm VMs)
- Reduce timeout for faster failure detection

**For memory-constrained hosts:**
- Reduce pool size: `NewVMPool(1, ...)`
- Lower VM memory: `MemSizeMiB: 128`

---

## 🗂️ Project Structure

```
sandbox_env/
├── backend/
│   ├── cmd/api/
│   │   └── main.go                  # Server entry point
│   ├── internal/
│   │   ├── executor/
│   │   │   ├── executor.go          # Interface definition
│   │   │   ├── docker.go            # Docker executor (legacy)
│   │   │   └── firecracker/
│   │   │       ├── executor.go      # Firecracker executor
│   │   │       ├── vm_manager.go    # VM lifecycle management
│   │   │       ├── vm_pool.go       # Pre-booted VM pool
│   │   │       └── vsock_client.go  # Host-guest communication
│   │   ├── handler/
│   │   │   └── execute.go           # HTTP handlers
│   │   ├── queue/
│   │   │   └── queue.go             # Job queue + worker pool
│   │   └── metrics/
│   │       └── metris.go            # Prometheus metrics (WIP)
│   └── go.mod
├── assets/
│   ├── kernel/
│   │   └── vmlinux                  # Linux kernel for Firecracker
│   └── rootfs/
│       ├── rootfs-alpine.ext4       # Alpine Linux with runtimes
│       └── rootfs.ext4              # Alternative rootfs
├── guest-agent/                     # VM guest agent (vsock listener)
├── release-v1.7.0-aarch64/          # Firecracker binaries
├── ARCHITECTURE.md                  # System design docs
├── API.md                           # API reference
├── SECURITY.md                      # Security documentation
├── DEMO.md                          # Usage examples
└── README.md                        # This file
```

---

## 🛣️ Development Roadmap

### Completed ✅

- ✅ Sprint 1: Docker MVP
- ✅ Sprint 2: Hardened Docker Sandbox
- ✅ Sprint 3: Firecracker Foundation
- ✅ Sprint 4: Firecracker Integration v1
- ✅ Sprint 5: Performance + VM Pooling
- ✅ Sprint 6: Multi-Language Support

### In Progress 🔄

- 🔄 Sprint 7: Observability + Kubernetes Deployment
  - Prometheus metrics
  - Structured JSON logging
  - Kubernetes manifests
  - Grafana dashboards

- 🔄 Sprint 8: Documentation + Public Release
  - Complete API documentation
  - Security whitepapers
  - Blog post draft

### Future Enhancements 🚀

- [ ] Snapshot/restore for instant boot (<10ms)
- [ ] Per-user rate limiting
- [ ] Language-specific resource profiles
- [ ] Horizontal auto-scaling
- [ ] Real-time execution streaming (WebSocket)
- [ ] Custom language runtime support
- [ ] Execution history/caching

---

## 🤝 Contributing

This is a portfolio project, but contributions are welcome! Areas where help is appreciated:

- **Language support**: Add Ruby, Rust, Java runtimes to rootfs
- **Optimization**: Reduce VM boot time further
- **Testing**: More comprehensive integration tests
- **Documentation**: Typo fixes, clarifications

**Guidelines:**
1. Follow Go best practices (gofmt, golint)
2. Include tests for new features
3. Update documentation
4. Explain security implications of changes

---

## 📝 License

MIT License - see [LICENSE](./LICENSE) for details.

---

## 🙏 Acknowledgments

**Inspired by:**
- [E2B](https://e2b.dev/) - Pioneered secure code execution with Firecracker
- [Judge0](https://judge0.com/) - Code execution API for competitive programming
- [AWS Firecracker](https://firecracker-microvm.github.io/) - Lightweight virtualization

**Built with:**
- [Firecracker](https://github.com/firecracker-microvm/firecracker) - MicroVM technology
- [Go](https://go.dev/) - Backend implementation
- [Alpine Linux](https://alpinelinux.org/) - Minimal rootfs

---

## 📧 Contact

**Abhishek Dadwal**

- GitHub: [@Abhishek00810](https://github.com/Abhishek00810)
- LinkedIn: [Linkedin Profile](https://www.linkedin.com/in/abhishek-dadwal-5565781b6/)

---

## ⭐ Star This Project

If this helped you understand Firecracker or secure code execution, please star the repo! 🌟

Built with ❤️ for learning and production use.
