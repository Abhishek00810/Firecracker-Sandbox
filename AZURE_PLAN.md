# Azure Plan

## Goal

Move serious pool and concurrency testing from local Lima to one Azure VM that supports nested virtualization, then add CI/CD to deploy and run smoke tests automatically.

## Recommended First VM

- Size: `Standard_D8_v5`
- OS: Ubuntu 24.04 LTS or Ubuntu 22.04 LTS
- Why:
  - balanced CPU/RAM for first serious testing
  - nested virtualization supported on Dv5
  - simpler starting point than over-sizing too early

If Python session memory pressure becomes an issue, next step:

- `Standard_E8_v5`

## Azure Docs

- VM sizes overview:
  - https://learn.microsoft.com/en-us/azure/virtual-machines/sizes-gpu
- Dv5 series:
  - https://learn.microsoft.com/en-us/azure/virtual-machines/sizes/general-purpose/dv5-series
- Ev5 series:
  - https://learn.microsoft.com/en-us/azure/virtual-machines/sizes/memory-optimized/ev5-series
- GitHub Actions for Azure:
  - https://learn.microsoft.com/en-us/azure/developer/github/github-actions
- IaC + GitHub Actions:
  - https://learn.microsoft.com/en-us/devops/deliver/iac-github-actions
- Azure Developer CLI pipeline:
  - https://learn.microsoft.com/en-us/azure/developer/azure-developer-cli/pipeline-github-actions

## Phase 1

Create one Azure VM and prove these:

1. `/dev/kvm` is available
2. Firecracker boots correctly
3. Snapshot restore works
4. warm pools work
5. stateful Python session first-run stays fast with session-pool warmup
6. 10+ concurrent requests behave better than local Lima

## VM Setup

Use:

- Ubuntu LTS
- Premium SSD
- one public IP for initial testing
- SSH access locked to your IP

Install:

- Go
- Firecracker
- iproute2
- Python
- Node
- any rootfs/build dependencies you already use locally

Verify:

- `test -e /dev/kvm && echo yes`
- `kvm-ok` if you install `cpu-checker`

## Infra Shape

Keep it simple first:

- one VM
- one deployment target
- one test environment

Do not start with:

- VM scale sets
- autoscaling groups
- load balancers
- multi-region

First prove runtime behavior on one strong machine.

## CI/CD Shape

Use GitHub Actions.

### Pipeline stages

1. `ci`
   - run unit tests
   - run lint
   - validate build

2. `deploy-staging`
   - trigger on merge to `main`
   - SSH or rsync code to Azure VM
   - restart service

3. `post-deploy-smoke`
   - run health check
   - run `local/runtime_single_diag.sh`
   - run session benchmark script

### Suggested branch policy

- PR:
  - run CI only
- main:
  - deploy to Azure staging VM
  - run smoke checks

## Deployment Strategy

For now, simplest is:

1. build on GitHub Actions runner
2. copy artifacts or repo to Azure VM
3. restart backend service
4. run smoke tests remotely

Do not overbuild this yet.

You can start with:

- `scp` / `rsync`
- `ssh` remote restart
- systemd service on the VM

Later you can move to:

- image-based deploys
- immutable releases
- blue/green or canary

## Suggested Systemd Units

Create:

- one service for backend API
- optional one for background maintenance scripts if needed

The main backend service should:

- start on boot
- restart on failure
- write logs to journald

## What To Measure On Azure

Once deployed, measure:

1. stateless Python single-run latency
2. stateless Node single-run latency
3. stateful Python:
   - session create
   - first run
   - second run
4. 10 concurrent requests
5. pool exhaustion behavior
6. replenish timing
7. VM-ready count versus latency

## Pool Strategy To Validate

On Azure, validate this exact direction:

- free stateless pool:
  - minimal warmup
- pro stateless pool:
  - warm Node bridge
- free session pool:
  - minimal warmup
- pro session pool:
  - warm Python stateful kernel

Then measure whether the ready-pool thresholds need to be:

- fixed
- tier-specific
- dynamically replenished

## Next Step After One VM

Only after one VM testing is solid:

- consider larger VM size
- then consider multiple host VMs
- then consider autoscaling

Do not jump to autoscaling before one-host behavior is understood.

## Immediate Action

1. provision `Standard_D8_v5`
2. install runtime dependencies
3. deploy current code
4. verify KVM and Firecracker
5. run smoke + session tests
6. compare against local results
