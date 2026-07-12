# Local Run

This backend should fail fast at startup if required env vars, assets, runtime directories, or Firecracker host prerequisites are missing.

## Required

- Linux host
- `/dev/kvm` available
- Firecracker binary present
- sandbox assets present
- `DATABASE_URL`

## Optional env vars

- `SUPABASE_JWT_SECRET` only for legacy Supabase HS256 dashboard tokens; Better Auth sessions do not need it
- `PORT` default: `8080`
- `ASSETS_PATH` default: auto-detected from `/app/assets`, `./assets`, or `../assets`
- `FIRECRACKER_BINARY` default: auto-detected from `/app/firecracker/firecracker-v1.7.0-aarch64` or repo release paths
- `SOCKET_DIR` default: `$TMPDIR/fc-sockets`
- `SNAPSHOT_DIR` default: `/dev/shm/fc-snapshots`
- `LOG_LEVEL` default: `info`
- `LOG_FORMAT` default: `json`
- `HOST_VALIDATION_MODE` default: `strict`

Set `HOST_VALIDATION_MODE=warn` only when you intentionally want startup to continue on a host that cannot actually run Firecracker.

## Start

From the `backend/` directory:

```bash
DATABASE_URL=postgresql://renderops:password@postgres:5432/renderops \
go run ./cmd/api/main.go
```

On success, startup now validates:

- PostgreSQL connectivity used for Better Auth sessions, profiles, keys and usage data
- assets path
- kernel/rootfs/initramfs files
- Firecracker binary path and execute bit
- socket and snapshot directories
- Linux/KVM host requirements
