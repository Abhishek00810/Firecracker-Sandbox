# Renderops Python SDK

Run code in secure, isolated sandboxes. Built on Firecracker microVMs.

## Installation

```bash
pip install renderops
```

## Quickstart

```python
from renderops import Sandbox

sb = Sandbox(api_key="ro_live_...")
result = sb.run("print('hello')", language="python")
print(result.stdout)  # hello
```

---

## Authentication

Get your API key from the Renderops dashboard and pass it when creating a client:

```python
sb = Sandbox(api_key="ro_live_...")
```

Every request is automatically authenticated. You never touch headers or tokens directly.

---

## Sandbox (sync)

Use `Sandbox` for standard synchronous execution.

### Initialize

```python
from renderops import Sandbox

sb = Sandbox(
    api_key="ro_live_...",       # required
    base_url="http://localhost:8080",  # optional, defaults to localhost
    timeout=60,                  # optional, HTTP timeout in seconds
)
```

### Run code

```python
result = sb.run("print(1 + 1)", language="python")
```

Each call runs in a **fresh sandbox** — no state is shared between calls.

**Parameters:**

| Parameter | Type | Default | Description |
|---|---|---|---|
| `code` | `str` | required | Code to execute |
| `language` | `str` | `"python"` | `"python"`, `"node"`, or `"bash"` |
| `timeout` | `int` | server default | Execution timeout in seconds |

**Returns `RunResult`:**

| Field | Type | Description |
|---|---|---|
| `stdout` | `str` | Standard output |
| `stderr` | `str` | Standard error |
| `exit_code` | `int` | Process exit code |
| `ok` | `bool` | `True` if exit code is 0 |
| `duration_ms` | `float` | Server-side execution time in ms |
| `request_id` | `str` | Unique request ID for debugging |
| `termination_reason` | `str` | `"success"`, `"timeout"`, `"oom_kill"` |

**Example:**

```python
result = sb.run("print(1 + 1)")
print(result.stdout)        # 2
print(result.ok)            # True
print(result.duration_ms)   # 1057.15
print(result.request_id)    # a22d5dad-1670-...
```

---

## Sessions (sync)

Sessions give you a **persistent VM** — variables, imports, and state survive between `run()` calls. Available on Pro tier.

```python
with sb.session() as sess:
    sess.run("x = 100")
    sess.run("x *= 3")
    result = sess.run("print(x)")
    print(result.stdout)  # 300
```

Use the context manager (`with`) to automatically close the session when done. Or manage it manually:

```python
sess = sb.session()
sess.run("import pandas as pd")
result = sess.run("print(pd.__version__)")
sess.close()  # releases the VM
```

---

## AsyncSandbox

Use `AsyncSandbox` when running multiple sandboxes concurrently — e.g. inside AI agents that spawn many parallel executions.

### Initialize

```python
from renderops import AsyncSandbox

sb = AsyncSandbox(api_key="ro_live_...")
```

### Run code

```python
import asyncio
from renderops import AsyncSandbox

async def main():
    sb = AsyncSandbox(api_key="ro_live_...")
    result = await sb.run("print('hello')", language="python")
    print(result.stdout)

asyncio.run(main())
```

### Run multiple sandboxes in parallel

```python
import asyncio
from renderops import AsyncSandbox

async def main():
    sb = AsyncSandbox(api_key="ro_live_...")

    r1, r2, r3 = await asyncio.gather(
        sb.run("print('sandbox 1')"),
        sb.run("print('sandbox 2')"),
        sb.run("print('sandbox 3')"),
    )
    print(r1.stdout)  # sandbox 1
    print(r2.stdout)  # sandbox 2
    print(r3.stdout)  # sandbox 3

asyncio.run(main())
```

All three run simultaneously instead of sequentially — total time ~1x instead of 3x.

### Async sessions

```python
async def main():
    sb = AsyncSandbox(api_key="ro_live_...")

    async with await sb.session() as sess:
        await sess.run("x = 100")
        await sess.run("x *= 3")
        result = await sess.run("print(x)")
        print(result.stdout)  # 300

asyncio.run(main())
```

---

## Supported languages

| Language | Value | Notes |
|---|---|---|
| Python | `"python"` | Persistent kernel — state survives across session calls |
| Node.js | `"node"` | Persistent kernel — state survives across session calls |
| Bash | `"bash"` | Fresh process per call |

---

## Error handling

All errors inherit from `SandboxError`.

```python
from renderops import Sandbox, AuthError, RateLimitError, ServerError, SandboxError

sb = Sandbox(api_key="ro_live_...")

try:
    result = sb.run("print('hello')")
except AuthError:
    print("Invalid or expired API key")
except RateLimitError:
    print("Rate limit exceeded — slow down requests")
except ServerError as e:
    print(f"Server error: {e}")
except SandboxError as e:
    print(f"Unexpected error: {e}")
```

**Exception reference:**

| Exception | HTTP status | Cause |
|---|---|---|
| `AuthError` | 401 | Invalid, deactivated, or expired API key |
| `RateLimitError` | 429 | Too many requests |
| `SessionNotFoundError` | 404 | Session ID does not exist |
| `ServerError` | 5xx | Server-side error |
| `APIError` | 4xx | Other client errors |
| `SandboxError` | — | Base class for all SDK errors |

---

## Tiers

| | Free | Pro |
|---|---|---|
| Stateless execution | Yes | Yes |
| Sessions | 1 session | 3 sessions |
| Max execution time | 10s | 60s |
| Rate limit | 2 req/s | 10 req/s |
| Parallel executions | Yes | Yes |
