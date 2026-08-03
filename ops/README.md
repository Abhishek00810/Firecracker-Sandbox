# Operations

Runtime operations are grouped by deployment target.

## Control-plane VPS

`control-plane/` owns the Docker Compose stack running PostgreSQL, the Go
control plane, the SvelteKit platform, Caddy, and database backups.

Run once on the VPS from a repository checkout:

```bash
sudo RUNNER_USER=renderops-admin bash ops/control-plane/setup.sh
```

Then:

1. Fill in `/opt/renderops/.env`.
2. Set `ORCHESTRATOR_URL` to the orchestrator's private URL.
3. Register the GitHub runner with the `control-plane` label.
4. Re-login so Docker and deployment group membership applies.
5. Run the `Deploy` workflow with target `control-plane`.

The setup script preserves an existing `/opt/renderops/.env`.

## Bare-metal worker

`worker/` owns the Firecracker worker systemd unit, one-time host setup, and
the CI installer.

Copy the repository setup files and the asset bundle to the worker, then run:

```bash
sudo WORKER_TOKEN='<same value as the control-plane VPS>' \
  ORCHESTRATOR_URL='http://10.0.0.7:8090' \
  WORKER_ID='worker-1' \
  WORKER_BIND='0.0.0.0:9876' \
  WORKER_ADVERTISE_URL='http://10.0.0.4:9876' \
  WORKER_ALLOCATABLE_VCPUS='8' \
  WORKER_ALLOCATABLE_MEMORY_MB='28000' \
  WORKER_DISK_RESERVE_GB='100' \
  ASSET_BUNDLE=/path/to/renderops-assets.tar.gz \
  bash ops/worker/setup.sh
```

The host must provide `/dev/kvm` and cgroup v2. Setup installs host networking
tools, creates `/etc/renderops/worker.env`, and installs the systemd unit.
Disk capacity is detected from the filesystem containing `ROOT_DIRECTORY`.
`WORKER_DISK_RESERVE_GB` optionally reserves host space (the default is 5%,
with a 10 GiB minimum), while `WORKER_DISK_CAP_GB` optionally imposes a lower
operator ceiling.
Normal deployments subsequently use `ops/worker/install.sh`.
The advertised endpoint must be reachable only over the private network from
the control plane and orchestrator; do not expose port `9876` to the internet.

The deploy workflow builds one worker binary and rolls it across both workers,
waiting for worker 1 to become healthy before restarting worker 2. Configure
these GitHub Actions secrets in the `production` environment:

```text
WORKER_SSH_KEY
WORKER_HOST
WORKER_USER

WORKER_2_SSH_KEY
WORKER_2_HOST
WORKER_2_USER
```

Each worker needs a distinct `WORKER_ID`, private `WORKER_ADVERTISE_URL`, and
an authorized deployment public key. Use workflow targets `worker-1` or
`worker-2` for a single host, and `worker` for the rolling two-host deployment.
The workflow also sets each host's network-slot and maximum-session capacity:
32 on the 8-vCPU worker and 192 on the 48-vCPU worker, matching the current
4x CPU overcommit policy for 1-vCPU nano sandboxes.

## Orchestrator server

`orchestrator/` owns the standalone private Compose service used for worker
registration, heartbeat, capacity reservation, and sandbox placement.

The deploy workflow records the immutable orchestrator image tag in the
server's protected `.env`. Subsequent health checks and manual Compose restarts
therefore use the exact deployed image instead of implicitly pulling `latest`.
The file is writable only by root and the deployment group because CI owns this
non-secret image pointer alongside the server-managed secrets.

After Docker is installed, create a temporary repository runner token in
GitHub and prepare the dedicated server:

```bash
sudo RUNNER_TOKEN='<temporary-token>' \
  bash ops/orchestrator/setup-github-runner.sh
sudo RUNNER_USER=renderops-runner bash ops/orchestrator/setup.sh
```

Populate `/opt/renderops-orchestrator/.env` with a database URL reachable over
the private network, the server's private bind IP, and an orchestrator token.
Register the runner with the `orchestrator` label. Port `8090` must be allowed
only from control-plane and worker private addresses; there is no public proxy.

After the runner, database route, and environment are verified, set the GitHub
repository variable `ORCHESTRATOR_DEPLOY_ENABLED=true`. Until then CI builds
and publishes the image but deliberately skips deployment.

## Database

`database/` contains database administration and observability scripts.
Drizzle migrations remain owned by the separate platform repository.
