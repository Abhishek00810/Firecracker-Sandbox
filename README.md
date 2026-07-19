# RenderOps Sandbox — Control Plane

Firecracker-backed code-execution sandboxes. This repo is currently reduced to
the **API surface only** while the distributed architecture is rebuilt — see
[plan.md](plan.md) for what exists, what was deleted (all recoverable from git
history), and the roadmap.

## What's here

- `backend/cmd/control-plane` — the single deployable: auth + Postgres + the
  public REST API. No VMs, no KVM requirement.
- `backend/internal/plane` — the wire contract between the control plane and
  future host agents (routes, types, `Service` interface).
- `API.md` — the public endpoint spec.

## Run

```bash
cp .env.example .env   # first time: fill in DATABASE_URL
./start.sh             # builds and starts the control plane
```

Or via docker: `docker compose up` from the project root builds
`Dockerfile.controlplane`.

## Rules

- The SvelteKit platform (`sk-renderops-platform`) owns the DB schema via
  drizzle migrations. Go code is query/insert only — never DDL.
- Billing is PAYG only. There are no tiers.
- Wire shapes in `internal/plane` and `internal/handler/api_types.go` change
  only additively.
