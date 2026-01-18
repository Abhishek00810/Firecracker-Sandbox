# Secure Code Execution Engine

A secure, multi-language code execution engine powered by Firecracker microVMs for high-performance, isolated code execution.

## Overview

This project provides a secure sandboxed environment for executing user-submitted code across multiple programming languages. Built with Firecracker microVMs, it offers hardware-level isolation with minimal overhead, ensuring that each code execution is completely isolated from the host system and other executions.

## Features

- 🔒 **Secure Sandboxing** - Hardware-level isolation using Firecracker microVMs
- ⚡ **High Performance** - MicroVMs boot in under 100ms with minimal resource overhead
- 🎯 **Resource Limits** - Strict CPU, memory, and timeout constraints per execution
- 🔄 **Job Queue** - Buffered job queue with worker pool for concurrent execution handling
- 🐍 **Language Support** - Currently supports Python (extensible to other languages)
- 🛡️ **Multi-Layer Security** - API validation, host-level security, and VM isolation
- 📊 **Execution Metrics** - Duration, exit codes, and termination reasons tracked

## Project Status

🚧 **In Development** - Currently implementing Firecracker integration (Sprint 3-4)

**Current Implementation:**
- ✅ Firecracker microVM executor
- ✅ Job queue with worker pool (10 workers, 100 job buffer)
- ✅ REST API with `/execute` and `/health` endpoints
- ✅ Code injection into VM rootfs
- ✅ Output collection via serial console
- ✅ Resource limits (256MB RAM, 2 vCPU, 30s timeout)

**In Progress:**
- 🔄 Multi-language support
- 🔄 VM pooling for faster boot times
- 🔄 Enhanced observability and metrics

## Quick Start

### Prerequisites

- Go 1.25.0 or later
- Firecracker v1.7.0 (included in `release-v1.7.0-aarch64/`)
- Linux kernel with KVM support
- Root/sudo access for VM management
- Assets: kernel (`vmlinux`) and rootfs (`rootfs.ext4`) in `assets/` directory

### Installation

1. **Clone the repository:**
   ```bash
   git clone <repository-url>
   cd sandbox_env
   ```

2. **Install Go dependencies:**
   ```bash
   cd backend
   go mod download
   ```

3. **Ensure Firecracker is accessible:**
   ```bash
   # Firecracker binary should be in release-v1.7.0-aarch64/
   chmod +x release-v1.7.0-aarch64/firecracker-v1.7.0-aarch64
   ```

4. **Verify assets exist:**
   ```bash
   ls assets/kernel/vmlinux
   ls assets/rootfs/rootfs.ext4
   ```

### Running the Server

1. **Start the API server:**
   ```bash
   cd backend/cmd/api
   go run main.go
   ```

   The server will start on port `8080` by default.

2. **Verify health:**
   ```bash
   curl http://localhost:8080/health
   ```

   Expected response:
   ```json
   {
     "status": "ok",
     "message": "Server is healthy and is rocking!!!"
   }
   ```

### Executing Code

**Example: Execute Python code**

```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -d '{
    "code": "print(\"Hello, World!\")\nfor i in range(5):\n    print(f\"Count: {i}\")",
    "language": "python"
  }'
```

**Example Response:**
```json
{
  "output": {
    "output": "Hello, World!\nCount: 0\nCount: 1\nCount: 2\nCount: 3\nCount: 4\n",
    "duration": 0.234,
    "exit_code": 0,
    "termination_reason": "success"
  },
  "status": "success"
}
```

## Architecture

See [ARCHITECTURE.md](./ARCHITECTURE.md) for detailed architecture documentation.

The system follows an E2B-inspired architecture with:

- **API Layer** (`/backend/internal/handler/`): Request validation and HTTP handling
- **Job Queue** (`/backend/internal/queue/`): Buffered channel (100 jobs) with worker pool (10 workers)
- **Executor** (`/backend/internal/executor/`): Replaceable sandbox runtime (Firecracker implementation)
- **VM Manager** (`/backend/internal/executor/firecracker/`): Firecracker VM lifecycle management
- **Isolation Boundary**: Multi-layer security (API → Host → VM)
- **Result Channel**: One-way output collection via serial console

### System Flow

1. Client submits code via `/execute` endpoint
2. API validates request and enqueues job
3. Worker pool picks up job from queue
4. Firecracker executor creates microVM with injected code
5. Code executes within VM with resource limits
6. Output collected via serial console
7. VM destroyed after execution
8. Result returned to client

## API Documentation

### POST `/execute`

Execute code in a sandboxed environment.

**Request Body:**
```json
{
  "code": "string",      // Code to execute
  "language": "string"   // Programming language (currently "python")
}
```

**Response:**
```json
{
  "output": {
    "output": "string",              // Combined stdout+stderr
    "duration": 0.0,                 // Execution time in seconds
    "exit_code": 0,                  // Process exit code (0 = success)
    "termination_reason": "string"   // "success", "timeout", "oom_kill", "runtime_error"
  },
  "status": "string",                // "success" or "error"
  "error": "string"                  // Error message (if status is "error")
}
```

**Status Codes:**
- `200 OK`: Execution completed
- `400 Bad Request`: Invalid request format
- `503 Service Unavailable`: Queue is full, try again later

### GET `/health`

Health check endpoint.

**Response:**
```json
{
  "status": "ok",
  "message": "Server is healthy and is rocking!!!"
}
```

## Resource Limits

Each execution is constrained by:

- **Memory**: 256MB RAM
- **CPU**: 2 vCPUs
- **Timeout**: 30 seconds (configurable)
- **Network**: Completely disabled
- **File System**: Ephemeral (destroyed after execution)

## Security

### Isolation Guarantees

- **Hardware-level virtualization**: Each execution runs in a separate microVM
- **No shared kernel**: VMs use independent kernel instances
- **Ephemeral VMs**: Destroyed immediately after execution
- **No network access**: Complete network isolation
- **Resource limits**: Prevents resource exhaustion attacks

### Security Layers

1. **API Layer**: Input validation, rate limiting (future)
2. **Host Layer**: Seccomp filters, capabilities management
3. **VM Layer**: Hardware isolation, minimal attack surface

## Development

### Project Structure

```
sandbox_env/
├── backend/
│   ├── cmd/api/           # API server entry point
│   ├── internal/
│   │   ├── executor/      # Executor interfaces and implementations
│   │   │   ├── firecracker/  # Firecracker executor
│   │   │   └── docker.go     # Docker executor (legacy)
│   │   ├── handler/       # HTTP handlers
│   │   └── queue/         # Job queue implementation
│   └── go.mod
├── assets/
│   ├── kernel/            # Linux kernel (vmlinux)
│   └── rootfs/            # Root filesystem (rootfs.ext4)
├── release-v1.7.0-aarch64/  # Firecracker binaries
└── ARCHITECTURE.md        # Detailed architecture docs
```

### Running Tests

```bash
cd backend
go test ./...
```

## Roadmap

This project follows an 8-sprint development plan:

- ✅ **Sprint 1**: Docker MVP
- ✅ **Sprint 2**: Hardened Docker Sandbox
- 🔄 **Sprint 3**: Firecracker Foundation (Current)
- 🔄 **Sprint 4**: Firecracker Integration v1 (Current)
- ⏳ **Sprint 5**: Performance + VM Pooling
- ⏳ **Sprint 6**: Multi-Language Support
- ⏳ **Sprint 7**: Observability + Kubernetes Deployment
- ⏳ **Sprint 8**: Documentation + Public Release

## Contributing

Contributions are welcome! Please ensure:

1. Code follows Go best practices
2. Tests are included for new features
3. Documentation is updated
4. Security considerations are addressed

## License

_To be determined_

