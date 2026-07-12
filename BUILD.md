# Build and Run Guide

This document is the fresh-clone path for developers and operators. It separates
source builds from host/runtime artifacts, because Firecracker requires Linux
host setup and VM assets that are not normal source dependencies.

## Supported Targets

- Backend API: Linux amd64/arm64, Go from `backend/go.mod`
- Guest agent: Linux amd64 inside the VM rootfs
- TypeScript SDK: Node 22
- Python SDK: Python 3.12 for CI, package supports Python 3.9+

Firecracker itself must run on a Linux host with KVM. macOS can run source
checks, but it cannot run the sandbox service.

## Required Host Capabilities

Production or staging hosts need:

- Linux with `/dev/kvm`
- cgroup v2
- `ip`, `ip netns`, `iptables`, `ss`, and `sudo`
- Firecracker binary matching the host CPU architecture
- kernel, rootfs, and initramfs assets
- Supabase project URL and service role key

The backend validates these at startup unless `HOST_VALIDATION_MODE=warn` is set.
Use warning mode only for local inspection on a host that cannot run VMs.

## Source Checks

Run these after a fresh clone:

```bash
cd backend
test -z "$(gofmt -l .)"
go build -o /tmp/renderops-api ./cmd/api
go test ./...

cd ../guest-agent
test -z "$(gofmt -l .)"
GOOS=linux GOARCH=amd64 go test ./...

cd ../backend/sdk/ts
npm ci
npm run build

cd ../python
python -m pip install -e .
python -c "from renderops import AsyncSandbox, RunResult, Sandbox, Session"
```

These commands mirror CI. They prove the source builds and imports, not that the
host can run Firecracker.

## Runtime Assets

The service expects these files:

```text
assets/kernel/vmlinux
assets/rootfs/rootfs-alpine.ext4
assets/initramfs.cpio.gz
release-v1.7.0-aarch64/firecracker-v1.7.0-aarch64
```

or explicit environment overrides:

```bash
ASSETS_PATH=/opt/renderops/assets
FIRECRACKER_BINARY=/opt/renderops/firecracker/firecracker
```

Current note: the repo still contains some runtime artifacts and binaries. Do not
add new generated artifacts to git. The production path should eventually fetch
versioned assets from a release bucket or image build pipeline.

## Environment

Create `backend/.env` on the host. Do not commit it.

```bash
SUPABASE_URL=https://your-project.supabase.co
SUPABASE_SERVICE_ROLE_KEY=...
# Optional: only while old Supabase JWT clients still exist
SUPABASE_JWT_SECRET=...
PORT=8080
LOG_LEVEL=info
LOG_FORMAT=json
HOST_VALIDATION_MODE=strict
```

Optional paths:

```bash
ASSETS_PATH=/opt/renderops/assets
FIRECRACKER_BINARY=/opt/renderops/firecracker/firecracker
SOCKET_DIR=/tmp/fc-sockets
SNAPSHOT_DIR=/dev/shm/fc-snapshots
```

## Local API Start on a Linux/KVM Host

From the repository root:

```bash
bash server.sh
```

`server.sh` prepares the host network slots, Firecracker runtime user, sockets,
snapshots, and then starts the backend.

For direct backend startup from `backend/`:

```bash
SUPABASE_URL=... \
SUPABASE_SERVICE_ROLE_KEY=... \
go run ./cmd/api/main.go
```

Direct startup still requires host capabilities and assets unless validation is
set to warning mode.

## What Not to Commit

Keep these local:

- `.env` files
- private keys such as `*.pem`
- `node_modules/`
- Python virtualenvs
- local pricing/planning CSVs
- generated SDK `dist/`
- generated backend and guest-agent binaries
- VM assets until a release artifact policy is finalized
