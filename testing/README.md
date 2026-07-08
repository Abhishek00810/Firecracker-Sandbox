# Live SDK Testing

SDK-based test scripts (no raw curl). They import the local Python SDK from
`backend/sdk/python` and run against a live backend.

## Setup

```bash
# point at a backend (defaults to the Azure VM if unset)
export RENDEROPS_BASE_URL="http://20.228.220.165:8080"
export RENDEROPS_API_KEY="ro_live_..."   # must be ACTIVE in Supabase

# SDK deps
pip install requests aiohttp
```

## Run

```bash
python3 testing/test_execute.py    # stateless run(): python / node / bash
python3 testing/test_session.py    # stateful session: state persistence, exec, isolation
python3 testing/test_timeouts.py   # per-execution timeout behaviour
```

Each script prints `PASS` / `FAIL` per check and exits non-zero if any fail.

## GitHub Actions

These tests are intentionally not part of normal push/pull-request CI. They use
a live backend, a live API key, VM capacity, and real usage state.

Run the `Live SDK Tests` workflow manually from GitHub Actions. Configure:

- secret `RENDEROPS_API_KEY`
- optional workflow input `base_url` if testing a host other than the Azure VM

## Notes
- `_config.py` wires the SDK path and builds the client from the env vars above.
- If you see `API key is deactivated`, reactivate the key in Supabase (`api_keys.is_active = true`).
