# Firecracker Sandbox Engine

A secure code execution engine built on Firecracker microVMs. Runs untrusted code in hardware-isolated VMs with enforced CPU and memory limits, structured observability, and a pre-warmed VM pool for low-latency execution.

[![Go Version](https://img.shields.io/badge/go-1.25.0-blue)]()
[![Firecracker](https://img.shields.io/badge/firecracker-v1.7.0-orange)]()
[![Status](https://img.shields.io/badge/status-active-brightgreen)]()

---

## Overview

Each execution runs inside a dedicated Firecracker microVM — a separate Linux kernel with no shared state, no network access, and hard resource limits enforced by the host kernel via cgroup v2. VMs are pre-booted and pooled to avoid cold-start latency on each request.

---

## Features

| Feature | Details |
|---------|---------|
| Hardware isolation | KVM-backed microVMs, not containers. Separate kernel per execution. |
| cgroup v2 limits | CPU quota and memory cap enforced by the host kernel per VM |
| Per-tenant tiers | Separate free (0.5 CPU, 256MB) and premium (2 CPU, 512MB) VM pools and queues |
| Burst protection | Token bucket rate limiter per tenant ID — free: 2 req/s burst 5, premium: 10 req/s burst 20 |
| VM pooling | Pre-booted VMs eliminate boot latency on the hot path |
| Structured logging | JSON logs via `log/slog` with request IDs and configurable levels |
| Request tracing | UUID injected per request, propagated through logs and response headers |
| Metrics | p50/p95/p99 latency percentiles, error breakdown, active executions, queue depth, VM pool state |
| Job queue | Buffered channel (100 jobs) with configurable concurrent workers per tier |
| Network isolation | No TCP/IP inside VMs — vsock only for host-guest communication |
| Ephemeral VMs | Each VM is destroyed after use, rootfs copy deleted, fresh VM replenished |

---

## Architecture

```
Client
  │
  │  HTTP POST /execute
  │  X-Tenant-ID: <id>   X-Tenant-Tier: free|premium
  ▼
Go REST API (port 8080)
  │  middleware: request ID, structured access log
  ├── /health
  ├── /execute
  │     │
  │     ├── TenantLimiter (token bucket per tenant)
  │     │       free:    2 req/s, burst 5
  │     │       premium: 10 req/s, burst 20
  │     │
  │     ├── free tier ──→  FreeJobQueue  (5 workers)
  │     │                       │
  │     │              FreeVMPool (3 VMs, 0.5 CPU, 256MB)
  │     │
  │     └── premium tier ──→  PremiumJobQueue  (10 workers)
  │                                │
  │                       PremiumVMPool (3 VMs, 2 CPU, 512MB)
  │
  └── /metrics    (pool stats, percentiles, error breakdown)

Each VM:
  [cgroup v2: cpu.max, memory.max]
        │
   vsock (unix socket)
        │
   Guest Agent (inside VM)
   executes code, returns stdout/stderr
```

### Request lifecycle

1. POST `/execute` with `{code, language}` and optional tier headers
2. Middleware injects UUID request ID, sets `X-Request-ID` header
3. Tier resolved from `X-Tenant-Tier` header (`free` default, `premium` opt-in)
4. Token bucket checked for `X-Tenant-ID` — returns 429 if burst exceeded
5. Job submitted to tier-specific buffered queue — returns 503 if full
6. Worker dequeues job, acquires VM from the matching tier pool (blocks up to 30s)
7. Code sent to guest agent via vsock, executed in isolated VM under cgroup limits
8. Result returned, VM released — destroyed and replenished in background
9. Metrics recorded (duration, error type, active count)

---

## API

### `POST /execute`

**Headers:**

| Header | Required | Values | Description |
|--------|----------|--------|-------------|
| `X-Tenant-ID` | No | any string | Identifies the tenant for rate limiting. Defaults to `"anonymous"`. |
| `X-Tenant-Tier` | No | `free` / `premium` | Selects resource pool. Defaults to `free`. |

**Free tier (default):**
```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -H "X-Tenant-ID: my-app" \
  -d '{"code": "print(1 + 1)", "language": "python"}'
```

**Premium tier:**
```bash
curl -X POST http://localhost:8080/execute \
  -H "Content-Type: application/json" \
  -H "X-Tenant-ID: my-app" \
  -H "X-Tenant-Tier: premium" \
  -d '{"code": "print(1 + 1)", "language": "python"}'
```

```json
{
  "output": {
    "output": "2\n",
    "duration": 0.087,
    "exit_code": 0,
    "termination_reason": "success"
  },
  "status": "success"
}
```

Response headers include `X-Request-ID` for tracing.

### `GET /metrics`

```json
{
  "total_executions": 142,
  "success_count": 139,
  "failure_count": 3,
  "success_rate": 0.9788,
  "active_executions": 1,
  "timeout_count": 1,
  "oom_count": 0,
  "runtime_err_count": 2,
  "system_err_count": 0,
  "avg_duration_seconds": 1.12,
  "p50_duration_seconds": 0.94,
  "p95_duration_seconds": 3.21,
  "p99_duration_seconds": 6.87,
  "queue_depth": 0,
  "vm_pool_available": 2,
  "vm_pool_in_use": 1
}
```

### `GET /health`

```json
{"status": "ok", "message": "Server is healthy and is rocking!!!"}
```

---

## Project Structure

```
sandbox_env/
├── backend/
│   ├── cmd/api/
│   │   └── main.go                     # Entry point, server setup
│   ├── internal/
│   │   ├── cgroup/
│   │   │   └── cgroup.go               # cgroup v2 lifecycle (Init, New, AddPID, Destroy)
│   │   ├── executor/
│   │   │   ├── executor.go             # Executor interface + ExecutionResult
│   │   │   ├── docker.go               # Docker executor (reference implementation)
│   │   │   └── firecracker/
│   │   │       ├── executor.go         # Firecracker executor
│   │   │       ├── vm_manager.go       # VM create/boot/destroy via Firecracker API
│   │   │       ├── vm_pool.go          # Pre-booted VM pool with cgroup wiring
│   │   │       └── vsock_client.go     # Host-guest communication
│   │   ├── handler/
│   │   │   └── execute.go              # HTTP handler, metrics recording, error classification
│   │   ├── middleware/
│   │   │   └── logging.go              # Request ID injection, structured access logging
│   │   ├── metrics/
│   │   │   └── metris.go               # Ring buffer, percentiles, atomic counters
│   │   ├── ratelimit/
│   │   │   └── limiter.go              # Per-tenant token bucket rate limiter
│   │   └── queue/
│   │       └── job_queue.go            # Buffered job queue, worker pool
│   └── go.mod
├── guest-agent/                         # vsock listener running inside each VM
├── assets/
│   ├── kernel/vmlinux                   # Linux kernel image for Firecracker
│   └── rootfs/rootfs-alpine.ext4        # Alpine rootfs with language runtimes
└── release-v1.7.0-aarch64/             # Firecracker binary
```

---

## Configuration

**Tier configuration** (`main.go`):

```go
// Shared VM config (both tiers use the same kernel/rootfs)
config := firecracker.VMConfig{
    VCPUCount:  2,
    MemSizeMiB: 256,
    Timeout:    30 * time.Second,
}

// Free tier — cgroup v2 limits
freeCgroupCfg := cgroup.Config{
    CPUQuotaUS:  50_000,             // 0.5 core
    CPUPeriodUS: 100_000,
    MemMaxBytes: 256 * 1024 * 1024, // 256MB
}

// Premium tier — cgroup v2 limits
premiumCgroupCfg := cgroup.Config{
    CPUQuotaUS:  200_000,            // 2 cores
    CPUPeriodUS: 100_000,
    MemMaxBytes: 512 * 1024 * 1024, // 512MB
}

freePool    := firecracker.NewVMPool(3, config, vmManager, freeCgroupCfg)
premiumPool := firecracker.NewVMPool(3, config, vmManager, premiumCgroupCfg)

// Rate limiters — token bucket per tenant ID
freeLimiter    := ratelimit.NewTenantLimiter(rate.Limit(2), 5)   // 2 req/s, burst 5
premiumLimiter := ratelimit.NewTenantLimiter(rate.Limit(10), 20) // 10 req/s, burst 20
```

**Log format** (environment variables):

```bash
LOG_FORMAT=text   # default: json
LOG_LEVEL=debug   # default: info
```

---

## Running

### Prerequisites

- Linux with KVM support (or Lima on macOS for development)
- Go 1.25+
- Firecracker v1.7.0 binary
- Kernel image and rootfs assets

### Development (macOS via Lima)

```bash
# Start Lima VM
limactl start firecracker
limactl shell firecracker

# Inside Lima
cd /path/to/sandbox_env/backend
sudo go run cmd/api/main.go
```

### Production (Linux)

```bash
cd backend
go build -o api ./cmd/api
sudo ./api
```

**Expected log output (JSON):**
```json
{"time":"2026-03-04T10:00:00Z","level":"INFO","msg":"Firecracker executor initialized successfully"}
{"time":"2026-03-04T10:00:00Z","level":"INFO","msg":"Server is running","port":":8080"}
{"time":"2026-03-04T10:00:01Z","level":"INFO","msg":"http request","method":"POST","path":"/execute","status":200,"duration_ms":94,"request_id":"a3f1c2d4-..."}
```

---

## Roadmap

**Completed**
- [x] Firecracker microVM execution
- [x] VM pool with vsock communication
- [x] Job queue with concurrent workers
- [x] Structured JSON logging with request tracing
- [x] Metrics: percentiles, error breakdown, pool state
- [x] cgroup v2 CPU and memory enforcement per VM
- [x] Per-tenant resource tiers (free: 0.5 CPU/256MB, premium: 2 CPU/512MB)
- [x] Burst protection via token bucket rate limiter per tenant

**Planned**
- [ ] Fair scheduling across tenant queues
- [ ] Firecracker vs Docker benchmark with flame graphs

**Planned**
- [ ] VM snapshot/restore for sub-10ms boot
- [ ] WebSocket streaming for long-running executions
- [ ] Horizontal scaling with shared queue
- [ ] Custom language runtime support

---

## Security Model

Execution is isolated at four layers:

1. **Hardware (KVM)** — separate kernel per VM, memory isolated at hardware level
2. **cgroup v2** — CPU and memory hard limits enforced by host kernel, VM killed on breach
3. **Network** — no TCP/IP stack inside VMs, vsock only
4. **Ephemeral state** — VM destroyed after each execution, rootfs copy discarded

Malicious code cannot access the network, read other executions' data, consume unbounded resources, or persist state between runs.

---

## Acknowledgments

- [Firecracker](https://github.com/firecracker-microvm/firecracker) — microVM technology by AWS
- [E2B](https://e2b.dev/) — reference for production Firecracker usage
- [Alpine Linux](https://alpinelinux.org/) — minimal rootfs

---

**Abhishek Dadwal** — [GitHub](https://github.com/Abhishek00810) · [LinkedIn](https://www.linkedin.com/in/abhishek-dadwal-5565781b6/)
