---
name: Platform Architecture
description: Core technical architecture of the sandbox/code execution platform — VM model, session lifecycle, execution flow, and pricing decisions
type: project
---

Firecracker microVM-based sandbox platform. Two distinct execution modes:

1. **Stateless executions** (`/execute`): Job queue (free/premium lanes), acquires a pooled VM, runs code via vsock, releases VM back (VM is destroyed + replenished, never returned dirty). 15s guest timeout.

2. **Stateful sessions** (`/session`): Long-lived VMs pinned to a session. Create → run many times → destroy. 15m idle reaper evicts abandoned sessions. Session tier is currently hardcoded to "premium" on creation.

**VM lifecycle**: Pool pre-boots VMs using overlayFS + snapshot restore. On release, dirty VM is destroyed and pool replenishes async. Cold boot fallback if pool exhausted.

**VM specs by tier**:
- Free: 2 vCPU, 256MiB RAM
- Pro/Team/Enterprise: 2 vCPU, 512MiB RAM

**Existing billing hooks in the code**:
- `handler/execute.go`: captures `duration := time.Since(start).Seconds()` at the handler level (includes queue wait time, not just execution time — a known measurement gap)
- `executor/firecracker/executor.go`: also captures `duration := time.Since(startTime).Seconds()` at the VM-execute level (cleaner — starts after VM acquire)
- `session/manager.go`: `Execute()` returns `resp.Duration` from the vsock response — this is guest-side execution duration (most accurate for billing)
- Tenant ID comes from `X-Tenant-ID` header; tier from `X-Tenant-Tier` header (no auth validation yet)

**Cgroup**: Per-session cgroups exist for resource isolation but CPU accounting not currently surfaced for billing.

**Billing value metric**: `exec_duration_ms` (guest-side execution time). Minimum 1 second, rounded up to nearest second.

---

## Pricing Tiers (decided 2026-03-21)

**Three self-serve tiers: Free, Pro ($29/mo), Team ($149/mo). Enterprise is sales-led.**

### Free
- 50 executions/day, max 10s duration, 1 concurrent exec, no sessions
- Rate limit: 10 req/min
- Cost floor per user per day (worst case): ~$0.004 — safe at scale

### Pro — $29/month
- 10,000 included exec-seconds/month, $0.003/exec-second overage
- Max duration: 60s, 5 concurrent execs, 3 concurrent sessions
- Session idle timeout: 15 minutes
- Session-minute fee: $0.0008/session-minute
- Rate limit: 60 req/min

### Team — $149/month
- 60,000 included exec-seconds/month, $0.0025/exec-second overage
- Max duration: 120s, 20 concurrent execs, 15 concurrent sessions
- Session idle timeout: 30 minutes
- Session-minute fee: $0.0006/session-minute
- Rate limit: 300 req/min

### Enterprise — custom
- $0.002/exec-second, $0.0004/session-minute
- Max duration: 300s, 100 concurrent execs, 50 concurrent sessions
- Session idle timeout: 60 minutes
- Target ACV: $24K-$120K/year

---

## Cost Model (assumed Hetzner/bare metal, 2026-03-21)

- Raw vCPU-second cost: ~$0.0000028
- Fully-loaded vCPU-second (with overhead, warm pool idle): ~$0.000004
- Per execution-second cost (2 vCPU VM): ~$0.000008
- Markup on Pro included seconds: ~362x on raw compute; ~95%+ gross margin after fully-loaded COGS

**If on AWS (EC2), multiply cost floor by 3-4x — pricing needs revisiting.**

---

## Session Billing Design

Sessions are metered two ways:
1. **Execution-seconds**: same pool as stateless, drawn from included allowance
2. **Session-minutes**: separate meter from `session_created_at` to `session_destroyed_at`, rounded up to nearest minute. Covers idle VM cost. No included allowance — always metered.

Key: idle VM cost for a 15-minute session = $0.0072. Pro session-minute rate yields $0.012 per 15-min session. Margin positive.

---

## Known Gaps / Next Steps
- Warm pool idle cost not yet measured — recommended: measure for 1 week, verify it's <30% of total infra spend
- No auth validation on tenant headers yet
- Cgroup CPU accounting not surfaced for billing (would enable CPU-weighted pricing later)

**Why/How to apply**: Use this to anchor all billing schema and measurement recommendations to what the code actually emits today. The vsock resp.Duration is the cleanest billing signal for session executions. For stateless, executor-level duration (post-VM-acquire) is better than handler-level duration.
