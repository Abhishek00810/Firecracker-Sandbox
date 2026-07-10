# Firecracker Sandbox Engine

A secure, low-latency code execution engine built on Firecracker microVMs. Runs untrusted code in hardware-isolated VMs with snapshot/restore boot, persistent sessions, outbound internet access, and structured observability.

[![Go Version](https://img.shields.io/badge/go-1.25.0-blue)]()
[![Firecracker](https://img.shields.io/badge/firecracker-v1.7.0-orange)]()
[![Status](https://img.shields.io/badge/status-active-brightgreen)]()

---

## Overview

Each execution runs inside a dedicated Firecracker microVM — a separate Linux kernel with no shared state and hard resource limits enforced by cgroup v2. VMs are restored from a memory snapshot (~130ms) rather than cold-booted. Persistent sessions reuse a single VM across many commands, bringing steady-state exec latency to ~16ms.

---

## Features

| Feature | Details |
|---------|---------|
| Hardware isolation | KVM-backed microVMs — separate kernel per execution |
| Snapshot/restore boot | VMs restored from memory snapshot in ~130ms, not cold-booted |
| Persistent sessions | One VM per session, reused across all exec/run calls |
| Persistent vsock | Single connection per session, no dial overhead on each call |
| Outbound internet | Full NAT from inside VMs — `pip install`, `git clone`, `curl` all work |
| DNS resolution | `nameserver 8.8.8.8` baked into guest at boot |
| Git credential injection | Pass `GITHUB_TOKEN` at session create — private repos work |
| cgroup v2 limits | CPU and memory hard caps enforced per VM by host kernel |
| Bearer token auth | API key validated against Supabase, cached 60s in-memory |
| Per-tenant tiers | Free and Pro tiers with separate VM pools and rate limits |
| Network isolation | Per-VM network namespace with TAP device and veth pair |
| Structured logging | JSON logs via `log/slog` with request IDs |
| Metrics | p50/p95/p99 latency, error breakdown, pool state, queue depth |
| ComputeSDK adapter | TypeScript SDK adapter for the computesdk benchmark suite |

---

## Use Cases

The sandbox is a use-case-agnostic execution primitive — what runs inside is up to the caller:

- **Code execution** — run untrusted snippets or whole programs in a throwaway VM (the `/execute` endpoint).
- **AI coding-agent backend** — give an agent an isolated machine to `git clone`, write code, run it, commit, and open a PR. Pass a `GITHUB_TOKEN` at session create and the full clone → edit → run → commit → push → PR loop runs inside the VM (demonstrated end-to-end).
- **RL training / agent rollouts** — spawn many isolated environments programmatically for execution-feedback loops; snapshot restore keeps per-rollout startup low.
- **DevOps / IaC** — run `terraform`, `kubectl`, or cloud CLIs against real APIs with credentials injected as session env, isolated from the host.

Isolation is the point: even if the code is malicious or an agent is prompt-injected, it runs in a separate kernel with a throwaway rootfs and an isolated network namespace — the blast radius is the VM, which is destroyed afterward.

---

## Performance (measured on an Azure 8 vCPU AMD EPYC VM, nested virtualization)

| Operation | Latency |
|-----------|---------|
| Session create (warm auth cache) | ~280–300ms |
| Session exec — first call | ~35ms |
| Session exec — steady state | ~16ms |
| Stateless execute — sequential | ~270–280ms total (~190ms host overhead + guest) |
| Stateless execute — 5 concurrent | ~500–775ms |
| Stateless execute — 35 concurrent | ~580ms–1.66s wall clock |
| VM snapshot restore | ~100–130ms |

> Numbers are from a shared, nested-virtualization cloud VM (8 vCPU = 4 physical cores × SMT). On bare-metal hosts with more physical cores, concurrent throughput scales substantially higher.

---

## Architecture

```
Client
  │
  │  HTTP + Bearer token
  ▼
Go REST API (port 8080)
  │
  ├── Auth middleware (Supabase key lookup, 60s TTL cache)
  ├── Rate limiter (token bucket per tenant)
  │
  ├── POST /execute  ──→  JobQueue  ──→  VMPool (on-demand snapshot restore)
  │                                           │
  │                                      vsock → guest-agent → execute code
  │                                           │
  │                                      destroy VM, replenish pool
  │
  ├── POST /session  ──→  SessionPool  ──→  snapshot restore
  │                              │               │
  │                         persistent vsock connection (kept open)
  │
  ├── POST /session/:id/exec   ──→  reuse persistent vsock → shell command
  ├── POST /session/:id/run    ──→  reuse persistent vsock → run code
  ├── DELETE /session/:id      ──→  close vsock, release VM
  ├── GET  /session/:id        ──→  session info
  ├── GET  /health
  └── GET  /metrics
```

### VM Pools (4 separate pools)

| Pool | Tier | Used by | Max concurrent |
|------|------|---------|----------------|
| freePool | free | `/execute` | 50 |
| premiumPool | pro | `/execute` | 50 |
| freeSessionPool | free | `/session` | 50 |
| proSessionPool | pro | `/session` | 50 |

All pools: `minPoolSize=0` (no pre-warming), on-demand snapshot restore. Shared 50 network slots across all pools.

### Per-VM resources

Every VM boots with a **fixed 1 vCPU and 256MB RAM** (`vcpu_count=1`, `mem_size_mib=256`). Host cgroup v2 caps vary by tier and path but are bounded by that single guest vCPU — and guest RAM stays 256MB regardless of the cgroup memory cap (a guest cannot see more memory than it booted with):

| Path / tier | cgroup CPU | cgroup memory |
|-------------|-----------|---------------|
| `/execute` free | 0.5 core | 256MB |
| `/execute` pro | 2 cores | 512MB |
| `/session` free | 1 core | 512MB |
| `/session` pro | 4 cores | 1GB |

> This fixed sizing is tuned for fast, ephemeral execution. Heavier workloads (large `npm install`, full builds) aren't supported yet — see [Current Limitations](#current-limitations) and [Roadmap](#roadmap).

### Network per VM

Each VM gets a fully isolated network environment:
```
Host default ns
  └── veth-fc-$i  (10.66.$i.1/30)
        │
        └── fc-ns-$i (network namespace)
              ├── veth-ns-$i  (10.66.$i.2/30) → default route for outbound
              ├── fc-tap-$i   (172.16.0.1/30) → guest TAP
              └── iptables MASQUERADE → outbound NAT
```

---

## API

### Authentication

All endpoints except `/health` and `/metrics` require:
```
Authorization: Bearer <api_key>
```

### `POST /execute` — stateless code execution

```bash
curl -X POST http://localhost:8080/execute \
  -H "Authorization: Bearer <api_key>" \
  -H "Content-Type: application/json" \
  -d '{"code": "print(1 + 1)", "language": "python"}'
```

```json
{
  "status": "success",
  "request_id": "...",
  "result": {
    "stdout": "2\n",
    "stderr": "",
    "exit_code": 0,
    "duration_ms": 241.5,
    "guest_duration_ms": 234.2
  }
}
```

### `POST /session` — create persistent session

```bash
curl -X POST http://localhost:8080/session \
  -H "Authorization: Bearer <api_key>" \
  -H "Content-Type: application/json" \
  -d '{"tier": "pro", "env": {"GITHUB_TOKEN": "ghp_..."}}'
```

```json
{
  "status": "success",
  "session": {
    "session_id": "abc-123",
    "tier": "pro",
    "state": "active",
    "created_at": "2026-06-04T10:00:00Z",
    "expires_at": "2026-06-04T10:15:00Z"
  }
}
```

### `POST /session/:id/exec` — run a shell command

```bash
curl -X POST http://localhost:8080/session/abc-123/exec \
  -H "Authorization: Bearer <api_key>" \
  -H "Content-Type: application/json" \
  -d '{"command": "git clone https://github.com/user/repo", "timeout": 30}'
```

### `POST /session/:id/run` — run code

```bash
curl -X POST http://localhost:8080/session/abc-123/run \
  -H "Authorization: Bearer <api_key>" \
  -H "Content-Type: application/json" \
  -d '{"code": "print(\"hello\")", "language": "python"}'
```

### `DELETE /session/:id` — destroy session

```bash
curl -X DELETE http://localhost:8080/session/abc-123 \
  -H "Authorization: Bearer <api_key>"
```

### `GET /health`

```json
{"status": "ok", "message": "Server is healthy and is rocking!!!"}
```

### `GET /metrics`

```json
{
  "total_executions": 142,
  "success_count": 139,
  "p50_duration_seconds": 0.24,
  "p95_duration_seconds": 0.78,
  "p99_duration_seconds": 1.62,
  "free_pool_available": 0,
  "pro_pool_available": 2
}
```

---

## Project Structure

```
sandbox_env/
├── server.sh                          # Server startup — process/port guard, network slots, backend start
├── build_rootfs.sh                    # Builds Alpine rootfs with language runtimes
├── guest-agent/                       # vsock listener running inside each VM
│   └── main.go                        # Handles exec, run, set_env, configure_network
├── backend/
│   ├── cmd/api/main.go                # Entry point, pool/queue/session manager setup
│   ├── internal/
│   │   ├── tierconfig/tierconfig.go   # Free/Pro tier definitions (limits, timeouts, pool sizes)
│   │   ├── executor/firecracker/
│   │   │   ├── vm_manager.go          # VM create/snapshot restore/destroy via Firecracker API
│   │   │   ├── vm_pool.go             # On-demand VM pool with slot management
│   │   │   └── vsock_client.go        # Host-guest vsock communication (persistent + one-shot)
│   │   ├── session/
│   │   │   ├── manager.go             # Session lifecycle — create, exec, run, destroy, reaper
│   │   │   └── store.go               # In-memory session store with idle eviction
│   │   ├── handler/
│   │   │   ├── execute.go             # POST /execute handler
│   │   │   └── session.go             # Session CRUD handlers
│   │   ├── middleware/
│   │   │   ├── auth.go                # Bearer token auth + Supabase key resolution + TTL cache
│   │   │   └── logging.go             # Request ID injection, structured access logging
│   │   ├── platform/
│   │   │   └── client.go              # Supabase REST client (key resolution, usage logging)
│   │   ├── metrics/                   # Ring buffer, p50/p95/p99, atomic counters
│   │   ├── queue/                     # Buffered job queue with worker pool
│   │   └── cgroup/                    # cgroup v2 lifecycle (CPU + memory limits)
│   └── sdk/
│       └── ts/                        # TypeScript ComputeSDK provider adapter
│           └── src/index.ts           # Wraps HTTP API → computesdk interface
└── assets/
    ├── kernel/vmlinux                 # Linux kernel image
    └── rootfs/rootfs-alpine.ext4     # Alpine rootfs with Python, Node, Go runtimes
```

---

## Running

### Prerequisites

- Linux with `/dev/kvm`
- Go 1.25+
- Firecracker v1.7.0 binary
- Kernel image and rootfs assets
- Supabase project with Better Auth `session`, `api_keys` and `profiles` tables

### Start (Azure / Linux)

```bash
cd ~/Firecracker-Sandbox
sudo bash server.sh
```

`server.sh` does in order:
1. Kill stale processes (including the compiled `go run` child) and refuse to start if `:8080` is still held
2. Clean up stale TAP devices, network namespaces, veth pairs, and leaked cgroups
3. Create network slots in parallel (netns + TAP + veth + NAT rules; count = `SLOT_COUNT`, default 50)
4. Start the Go backend (uses the prebuilt rootfs — guest-agent and pip/network configs are baked into the image at build time)

### Environment variables

| Variable | Required | Default |
|----------|----------|---------|
| `SUPABASE_URL` | Yes | — |
| `SUPABASE_SERVICE_ROLE_KEY` | Yes | — |
| `SUPABASE_JWT_SECRET` | No | legacy Supabase JWT support only |
| `PORT` | No | `8080` |
| `ASSETS_PATH` | No | auto-detected |
| `FIRECRACKER_BINARY` | No | auto-detected |
| `SOCKET_DIR` | No | `$TMPDIR/fc-sockets` |
| `SNAPSHOT_DIR` | No | `/dev/shm/fc-snapshots` |
| `LOG_LEVEL` | No | `info` |
| `LOG_FORMAT` | No | `json` |

---

## Tier Configuration

Defined in `backend/internal/tierconfig/tierconfig.go`:

| Setting | Free | Pro |
|---------|------|-----|
| Rate limit | 1000 req/s | 1000 req/s |
| Rate burst | 100 | 100 |
| Max exec timeout | 10s | 60s |
| Max pool size | 50 | 50 |
| Max sessions | 50 | 50 |
| Session idle timeout | 5 min | 15 min |
| Session max lifetime | 2 hours | 24 hours |

---

## Security Model

1. **Hardware (KVM)** — separate kernel per VM, memory isolated at hardware level
2. **cgroup v2** — CPU and memory hard limits enforced by host kernel
3. **Network namespace** — each VM in its own netns, no cross-VM traffic
4. **Ephemeral rootfs** — VM destroyed after each stateless execution, fresh VM replenished
5. **vsock only** — no TCP/IP inside VMs for host-guest communication

---

## Current Limitations

- **Fixed VM size** — every VM is 1 vCPU / 256MB RAM; resources are not yet configurable per request.
- **RAM-backed writable layer** — the rootfs upper layer is tmpfs (in guest RAM), so all writes count against the 256MB. Heavy `npm install` or full project builds will exhaust memory; the platform is currently sized for fast, ephemeral execution, not long-lived dev environments.
- **No public preview URLs** — a server running inside a VM is not yet reachable from the public internet.
- **Single template** — one prebuilt rootfs image; custom per-tenant images are not yet supported.

---

## Roadmap

Planned — not yet implemented:

- **Configurable resources** — choose vCPU / RAM / disk per sandbox at create time.
- **Disk-backed writable layer** — a real per-VM writable disk (GBs) so `npm install`, build caches, and terraform state land on disk instead of RAM.
- **Filesystem API** — `read` / `write` / `list` / `patch` files in a session without shell gymnastics.
- **Process & streaming exec** — long-running processes and streamed stdout/stderr.
- **Custom templates** — bring-your-own image (Dockerfile) with your own tools and runtimes baked in.
- **Python SDK** — alongside the existing TypeScript ComputeSDK adapter.
- **Preview URLs** — wildcard-subdomain reverse proxy mapping `<id>-<port>.<domain>` to a VM's port (HTTP + WebSocket).
- **BYOC / self-hosted** — deploy the data plane into your own cloud account.

---

## Acknowledgments

- [Firecracker](https://github.com/firecracker-microvm/firecracker) — microVM technology by AWS
- [E2B](https://e2b.dev/) — reference for production Firecracker usage patterns
- [ComputeSDK](https://github.com/computesdk/benchmarks) — sandbox benchmark suite

---

**Abhishek Dadwal** — [GitHub](https://github.com/Abhishek00810) · [LinkedIn](https://www.linkedin.com/in/abhishek-dadwal-5565781b6/)
