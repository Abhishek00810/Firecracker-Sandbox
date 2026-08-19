# RenderOps Sandbox

RenderOps is a multi-tenant sandbox runtime built on Firecracker. The public
control plane authenticates users, applies policy and billing configuration,
and exposes the sandbox API. A private orchestrator places lifecycle work on
healthy workers. Workers own KVM, Firecracker, networking, cgroups, writable
disks, guest communication, and pause checkpoints.

This repository contains the Go services, guest agent, deployment automation,
and infrastructure scripts. The SvelteKit dashboard and the Drizzle migration
history live in the separate platform repository.

## Architecture

```text
SDK / dashboard
      |
      | HTTPS + Bearer token
      v
Control plane ----------------------> PostgreSQL
      |                                  auth, policy, billing,
      | lifecycle                        sandbox state, usage, audit
      v
Private orchestrator <------------ worker registration + heartbeat
      |
      | selects a worker and invokes lifecycle operations
      v
Worker HTTP/gRPC service
      |
      | Firecracker API + vsock
      v
Firecracker microVM <-------------> guest-agent
```

The hot execution path is shorter: after resolving the durable placement, the
control plane sends `run` and `exec` directly to the selected worker. The
orchestrator is not in stdout/stderr or terminal-streaming traffic.

### Service boundaries

| Component | Entry point | Responsibility |
| --- | --- | --- |
| Control plane | `backend/cmd/control-plane` | Public REST API, authentication, policy, billing, usage, audit, and terminal WebSocket bridge |
| Orchestrator | `backend/cmd/orchestrator` | Private worker registry, health, placement, and sandbox lifecycle coordination |
| Worker | `backend/cmd/worker` | Firecracker lifecycle, admission, cgroups, networking, writable disks, checkpoints, execution, and PTYs |
| Preview gateway | `backend/cmd/preview-gateway` | Signed wildcard preview URLs and reverse proxying to sandbox ports |
| Template builder | `backend/cmd/template-builder` | Builds and publishes immutable Firecracker template artifacts |
| Guest agent | `guest-agent` | Runs inside each microVM and serves execution, runtime, and PTY requests over vsock |

## Repository Layout

```text
backend/cmd/                 deployable Go entry points
backend/internal/controlplane public-service composition and worker clients
backend/internal/orchestrator placement, lifecycle, client, and private API
backend/internal/worker      worker HTTP server and capacity admission
backend/internal/session     host-local sandbox runtime and pause/resume state
backend/internal/executor    Firecracker process, snapshot, pool, and vsock code
backend/internal/checkpoint  chunked checkpoint write/read implementations
backend/internal/template    template manifests, stores, cache, and validation
backend/internal/platform    PostgreSQL queries; never schema migrations
backend/internal/handler     public API handlers
backend/internal/rpc         internal worker gRPC contracts
guest-agent/                 microVM PID 1 and runtime bridge
ops/                         deployment setup and operations by service
.github/workflows/           CI and deployment workflows
```

See [ARCHITECTURE.md](ARCHITECTURE.md) for the detailed request flows and
ownership rules.

## Public API

The development API is `https://dev-api.renderops.com`. All public endpoints
except `GET /health` require:

```http
Authorization: Bearer ro_live_<api-key>
```

Dashboard requests may use a Better Auth session token in the same header.
The main lifecycle is:

```text
POST /session
POST /session/{id}/run       or POST /session/{id}/exec
POST /session/{id}/pause
POST /session/{id}/resume
DELETE /session/{id}
```

Interactive terminals, authenticated browser IDE sessions, and signed port
previews are also available. The full wire contract, examples, status codes,
and WebSocket framing are documented in [API.md](API.md).

## Sandbox Sizes

Only canonical shapes are accepted. Explicit custom resource combinations are
rejected rather than silently rounded.

| Size | vCPU | Memory | Writable disk |
| --- | ---: | ---: | ---: |
| `nano` | 1 | 256 MiB | 1 GiB |
| `small` | 2 | 512 MiB | 10 GiB |
| `medium` | 2 | 1024 MiB | 20 GiB |

## Development

Requirements:

- Go 1.25 or the version declared in `backend/go.mod`
- PostgreSQL containing the platform-owned Drizzle schema
- a reachable orchestrator for control-plane lifecycle calls
- Linux with KVM and cgroup v2 for a worker

Run the complete backend test suite:

```bash
cd backend
go test ./...
go vet ./...
test -z "$(gofmt -l .)"
```

Run the guest-agent tests:

```bash
cd guest-agent
GOOS=linux GOARCH=amd64 go test ./...
```

For local control-plane development, export at least `DATABASE_URL`,
`ORCHESTRATOR_URL`, `ORCHESTRATOR_TOKEN`, and `WORKER_TOKEN`, then run:

```bash
./start.sh
```

The control plane can run on any host. The worker must run on Linux with
`/dev/kvm`; it is deployed as a systemd service rather than a container.

## Data Ownership

- The platform repository owns `schema.js` and every Drizzle migration.
- This repository's Go code may query and mutate application rows but must not
  execute DDL or create schema at startup.
- The control plane owns user-facing sandbox state, usage, billing, and audit
  writes.
- The orchestrator owns `worker_hosts` and durable placement transitions in
  `sandboxes.host_id`.
- Workers have no PostgreSQL credentials. They report state, heartbeat, and raw
  usage through private authenticated APIs.

## Deployment

- Control plane, dashboard, PostgreSQL, preview gateway, and Caddy run in the
  control-plane Compose stack.
- The orchestrator runs as a private Compose service on its own server.
- Workers run directly on KVM hosts under systemd.
- CI tests every Go service. Deployment publishes immutable GHCR images for the
  containerized services and performs a rolling worker binary rollout.

Operational setup, ports, storage, and deployment secrets are documented in
[ops/README.md](ops/README.md). Current production-readiness work is tracked in
[PLAN.md](PLAN.md).

## Stability Rules

- Public and private wire contracts evolve additively unless a versioned route
  is introduced.
- Clients never select billing models, placement policy, worker endpoints, or
  overcommit ratios.
- A worker's atomic admission check is the final capacity authority.
- Missing, expired, or foreign sandbox identifiers return a non-revealing
  authorization/not-found response.
- Secrets and machine credentials stay out of Git; server-managed environment
  files or a managed secret store provide them at deployment time.
