# Control Plane

Standalone control-plane service under active development. The legacy backend
remains unchanged while this service grows behind compatibility tests.

## Current slice

- Health endpoint on `GET /health`
- Static, configuration-backed worker registry
- Private HTTPS/JSON worker client matching the current worker API
- Application execution service with sandbox allocation and worker lookup ports

Public execution routes are intentionally not exposed until this service owns
database-backed authentication and sandbox allocation persistence.

## Required configuration

```text
STATIC_WORKER_URL=http://127.0.0.1:9000
WORKER_TOKEN=development-secret
DATABASE_URL=postgres://user:pass@host:5432/db
PORT=8081
STATIC_WORKER_ID=worker-1   # optional — single-server default is "worker-1"
```

`STATIC_WORKER_ID` is optional: with one worker it auto-defaults to `worker-1`.
Multi-worker deployments set it explicitly (until workers self-register).
`DATABASE_URL` is required — any Postgres-compatible host (RDS/Aurora Postgres,
Supabase, Neon, PlanetScale for Postgres) works by changing only this string.
Worker URLs must use HTTPS except for loopback development.
