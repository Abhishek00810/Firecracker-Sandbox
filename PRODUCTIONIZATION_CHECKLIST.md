# Productionization Checklist

## Current Stage

- Runtime architecture: validated
- Stateless execution: working well
- Stateful Python sessions: working with pool warmup
- Pool strategy: emerging, not yet production-grade
- Infra maturity: pre-production

## Priority 1: Reliability

- Define min-ready pool targets per tier and per mode
- Define replenish policy when pool falls below threshold
- Define trim policy when excess warmed VMs sit idle
- Prove snapshot restore reliability on Azure
- Prove session create / first run / second run consistency on cloud
- Test pool exhaustion behavior explicitly
- Eliminate unexpected fallback paths where possible

## Priority 2: Observability

- Finalize pool metrics by tier and by mode
- Add session pool metrics separately from stateless pool metrics
- Classify failures correctly in metrics
- Track:
  - pool ready count
  - acquire wait time
  - replenish duration
  - warmup duration
  - restore failures
  - cold boot fallback count
- Improve guest-runtime debug visibility without response hacks
- Create one dashboard for:
  - latency
  - success rate
  - pool health
  - restore failures

## Priority 3: Deployment

- Deploy one Azure staging VM
- Verify `/dev/kvm` and Firecracker there
- Run smoke tests after every deploy
- Add systemd service for backend
- Add restart-on-failure policy
- Separate staging vs production config
- Define rollback path

## Priority 4: CI/CD

- PR:
  - lint
  - unit tests
  - build validation
- Main branch:
  - deploy staging
  - run smoke tests
  - run session benchmark checks
- Add clear pass/fail thresholds for post-deploy checks

## Priority 5: Session Product Contract

- Define free vs paid session behavior clearly
- Define idle timeout and max lifetime per tier
- Define how many sessions each tier gets
- Define expected first-run and repeated-run latency targets
- Decide what guarantees are public vs best effort

## Priority 6: Security / Isolation

- Review network policy defaults
- Review filesystem isolation expectations
- Review process cleanup and dirty-VM disposal
- Review package installation policy inside sessions
- Review abuse controls and rate limits
- Review log redaction and secret exposure risks

## Priority 7: Cleanup

- Remove temporary debug markers from execution stderr
- Remove temporary experimental logs that are now noisy
- Fix SDK/examples that still use outdated imports or assumptions
- Clean up naming where `stateful` / `stateless` modes should be explicit
- Keep runtime architecture docs short and accurate

## Azure Validation Goals

- Stateless Python single-run latency
- Stateless Node single-run latency
- Stateful Python:
  - session create
  - first run
  - second run
- 10+ concurrent request behavior
- Session pool depletion behavior
- Cost of keeping paid session pool warmed

## Exit Criteria For “Production Beta”

- Stable Azure deploys
- Snapshot restore reliability proven
- Warm pool policy defined and measured
- First-run stateful Python no longer regresses badly
- Failure metrics accurate
- Smoke tests automated in CI/CD
- Basic dashboards and alerts in place
