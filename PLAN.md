# RenderOps Engineering Plan

This document records the current implementation boundary and the work needed
for a production rollout. It is a roadmap, not a claim that every listed
feature is production-ready.

## Current Baseline

Implemented and exercised in development:

- separate control-plane, orchestrator, worker, preview-gateway, template-
  builder, and guest-agent binaries;
- API-key and Better Auth session authentication;
- canonical `nano`, `small`, and `medium` sandbox shapes;
- durable sandbox rows and worker-host placement;
- healthy-worker filtering, best-of-three placement, contention retries, and
  worker-authoritative atomic admission;
- Firecracker create, run, exec, pause, resume, auto-pause, and destroy;
- CPU/memory cgroups, network namespaces/TAP/nftables, and guest vsock;
- multiple interactive PTYs over WebSocket -> gRPC -> vsock;
- signed private wildcard port previews;
- per-worker mounted writable-disk abstraction with reflink support;
- chunked Blob checkpoint writes and reads for VM state, memory, and writable
  disk generations;
- raw usage meters, run/output history, lifecycle audit events, and PAYG
  runtime debiting;
- container deployment for control-plane/orchestrator services and rolling
  systemd deployment for two workers.

## Production Invariants

Every change must preserve these properties:

1. A public caller cannot choose its billing model, worker, placement score,
   overcommit ratio, or internal endpoint.
2. A sandbox row exists before capacity is reserved.
3. Worker admission reserves resources before VM boot and releases them on
   every failed path.
4. A sandbox cannot be reported `active` until the worker confirms the guest
   agent is ready.
5. Pause cannot discard local state until a complete durable checkpoint
   generation is published.
6. Destroy is idempotent and releases VM, terminal, capacity, placement, and
   writable-disk state.
7. Workers never receive PostgreSQL credentials.
8. Foreign sandbox IDs are indistinguishable from missing IDs at public APIs.
9. Versioned templates and checkpoints are immutable after publication.
10. Deployment credentials and machine keys never enter the repository.

## P0: Correctness Gate

Required before external production traffic:

- Add end-to-end tests for create -> run -> pause -> resume -> run -> destroy
  with database and worker-capacity assertions after every transition.
- Add failure-injection tests for worker boot timeout, checkpoint upload
  interruption, corrupt chunk, worker restart, stale heartbeat, and ambiguous
  lifecycle response.
- Reconcile database state against worker state after control-plane,
  orchestrator, or worker restart; define repair rules for every lifecycle
  state rather than relying on manual cleanup.
- Make all public errors use one versioned JSON envelope and document stable
  error codes.
- Calculate/enforce per-tenant active-session limits atomically; remove the
  placeholder `active_sessions: 0` response.
- Make balance deduction and usage finalization idempotent and transactional;
  prove that retries cannot double-charge or lose chargeable runtime.
- Add idempotency keys to sandbox creation and destructive lifecycle calls.
- Replace process-local terminal attachment state before running more than one
  control-plane replica, or route terminal creation/attach with strict
  stickiness as a temporary constraint.

Exit criteria: repeated lifecycle tests leave zero active worker reservations,
zero orphan Firecracker processes, and correct durable rows after success,
failure, cancellation, and restart.

## P1: Storage and Mobility

- Keep active writable disks behind `writabledisk.Store`; support filesystem
  volumes without embedding Azure/AWS behavior in session logic.
- Validate XFS reflink and disk-reserve alarms on every worker during startup.
- Benchmark checkpoint write/read by changed bytes, logical disk size, and
  concurrency. Record p50/p95/p99 and Blob request cost.
- Add retention and garbage collection for unreferenced checkpoint chunks and
  obsolete generations.
- Finish portable template compatibility: pinned Firecracker version, kernel
  and rootfs digests, architecture, CPU template/features, and resource shape.
- Enable cross-host resume only after a target worker can restore an immutable
  checkpoint without source-worker files or baked host-only paths.
- Evaluate a lazy remote block layer (NBD/ublk or equivalent) only after the
  chunked checkpoint baseline is measured. Do not put Blob/S3 directly under
  ext4 without a block/cache service.

Exit criteria: a paused sandbox can resume on a compatible replacement worker,
and checkpoint garbage collection cannot delete data referenced by an active
generation.

## P1: Scheduling and Fleet Scale

- Separate worker-reported allocated capacity from orchestrator-local pending
  reservations when multiple orchestrator replicas or asynchronous lifecycle
  dispatch creates a real acknowledgement gap.
- Remove `ORDER BY random()` from the large-fleet path. Maintain a bounded
  healthy candidate cache/index, sample K candidates, and keep worker admission
  authoritative.
- Add template/version compatibility and pool/class constraints to worker
  eligibility.
- Define workload classes: shared PAYG overcommit, reserved compute, and
  enterprise/dedicated pools. Resolve class from authenticated server-side
  policy, not arbitrary request input.
- Add queue/backpressure semantics for burst demand. A request must either have
  a bounded scheduling deadline or return a retryable status; it must not wait
  indefinitely.
- Add worker drain, replacement, and autoscaling hooks. New instances bootstrap
  assets, mount storage, register, sync templates, and become schedulable only
  after readiness.

Exit criteria: adding/removing a worker needs no code or CI path change, and a
multi-orchestrator contention test cannot over-allocate a worker.

## P1: Security

- Put production secrets in Azure Key Vault or the target cloud's managed
  secret service and deliver short-lived credentials through workload identity.
- Rotate and separate worker, orchestrator, terminal, preview, database, and
  Blob credentials. Do not derive unrelated service credentials from one
  long-lived token in production.
- Use private DNS/networking and mTLS or workload identity for service-to-
  service calls; firewall worker `9876` and orchestrator `8090` from the public
  internet.
- Apply explicit guest egress policy, DNS controls, SSRF blocks, metadata-
  endpoint denial, and preview request limits.
- Threat-model preview tokens, WebSocket origins, terminal replay, log
  injection, environment-secret exposure, and archive/chunk parsing.
- Run dependency, container, secret, and IaC scanning in CI.

Exit criteria: documented credential rotation and incident procedures work
without rebuilding the product or manually editing every worker.

## P1: Observability and SLOs

- Export Prometheus metrics for request latency/status, placement retries,
  worker rejection, VM boot phases, vsock readiness, checkpoint I/O, terminal
  connections, resource reservations, and reconciliation.
- Propagate one trace/request ID across control plane, orchestrator, worker,
  guest operation, database writes, and checkpoint generations.
- Build dashboards for fleet health, allocatable/reserved/actual resources,
  sandbox state counts, error codes, and p50/p95/p99 lifecycle latency.
- Alert on stale heartbeats, disk headroom, orphan VMs, reconciliation drift,
  failed billing batches, checkpoint backlog, and certificate expiry.
- Define initial SLOs only after controlled load tests establish a baseline.

Exit criteria: an operator can answer which layer caused a failed or slow
sandbox from metrics/logs without SSHing into every host.

## P2: Product and SDK

- Introduce a versioned public namespace before making incompatible changes;
  keep `/session` compatible while SDK users migrate.
- Generate or maintain typed SDKs for create/list/get/run/exec/pause/resume/
  destroy, terminals, and previews.
- Add SDK retry rules based on stable error codes and idempotency keys.
- Add public/private preview policy, revocation, and optional discovered-port
  UX after the security model is agreed.
- Add custom template APIs, build history, ownership/RBAC, activation, rollback,
  quotas, and build logs. Keep template build execution separate from runtime
  workers when load warrants it.
- Add organization-level roles, audit queries, budgets, and enterprise pool
  assignment.

## Database Migration Policy

The platform repository remains the only migration owner:

1. Change its Drizzle `schema.js`.
2. Generate a reviewed, immutable SQL migration.
3. Test it against a production-like database and existing rows.
4. Run a one-shot migration job before deploying code that requires the new
   columns/tables.
5. Deploy backward-compatible application code.
6. Remove compatibility paths only in a later release.

Never use `drizzle-kit push` against shared staging/production databases and
never let a long-running Go service perform startup DDL.

## Validation Matrix

Every release candidate should cover:

| Area | Minimum validation |
| --- | --- |
| API | auth, ownership isolation, canonical resources, error contract |
| Lifecycle | create/run/exec/pause/resume/destroy and auto-pause |
| Concurrency | simultaneous admission at last free slot; duplicate lifecycle requests |
| Failure | process kill/restart, stale worker, timeout after success, Blob interruption |
| Storage | sparse/reflink behavior, chunk reuse, corrupt/missing chunk, low disk |
| Networking | egress policy, namespace isolation, preview HTTP/WebSocket |
| Terminal | multiple PTYs, resize, close, token expiry/replay, disconnect cleanup |
| Billing | minute idempotency, retry, final debit, insufficient balance |
| Deployment | image rollback, rolling worker restart, rootfs/template compatibility |

## Deliberately Deferred

- Kafka/RabbitMQ is not required for synchronous lifecycle or execution today.
  Introduce a durable queue/outbox only for a measured asynchronous need such
  as builds, billing exports, webhooks, or reconciliation fan-out.
- Kubernetes/Nomad is not required to schedule individual Firecracker VMs. A
  fleet manager may later run one worker daemon per host, while RenderOps keeps
  sandbox placement authority.
- gRPC is useful for versioned internal lifecycle streaming, but changing
  transport does not fix ownership, idempotency, or capacity correctness. The
  service interfaces intentionally allow transport replacement later.
