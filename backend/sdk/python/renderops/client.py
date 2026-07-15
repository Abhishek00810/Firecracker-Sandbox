from __future__ import annotations

from typing import Any, Optional

import requests

from .exceptions import APIError, AuthError, RateLimitError, ServerError, SessionNotFoundError
from .models import NetworkConfig, Resources, RunResult


class Sandbox:
    """A single sandbox environment. State (files, packages, variables) persists across
    run()/exec() calls. Obtain one via Renderops.create(...) or Renderops.connect(id) —
    don't construct directly.

    Attributes
    ----------
    id : str          the sandbox id
    name : str        human-readable label ("what this sandbox is for")
    state : str       "active" | "paused" | "destroyed"
    billing_model : str
    metadata : dict   user labels (purpose/owner/etc.)
    """

    def __init__(self, client: "Renderops", id: str, name: Optional[str] = None,
                 state: Optional[str] = None, billing_model: Optional[str] = None,
                 metadata: Optional[dict] = None):
        self._client = client
        self.id = id
        self.name = name
        self.state = state
        self.billing_model = billing_model
        self.metadata = metadata or {}

    # ── execution ───────────────────────────────────────────────────────────
    def run(self, code: str, language: str = "python", timeout: Optional[int] = None) -> RunResult:
        """Run code in this sandbox. State persists across calls. Auto-resumes if paused."""
        body: dict = {"code": code, "language": language}
        http_timeout: Optional[int] = None
        if timeout is not None:
            body["timeout"] = timeout
            http_timeout = timeout + 15
        resp = self._client._post(f"/session/{self.id}/run", body, timeout=http_timeout)
        self.state = "active"
        return RunResult._from_session_run(resp)

    def exec(self, command: str, timeout: Optional[int] = None) -> RunResult:
        """Run a shell command in this sandbox. Auto-resumes if paused."""
        body: dict = {"command": command}
        http_timeout: Optional[int] = None
        if timeout is not None:
            body["timeout"] = timeout
            http_timeout = timeout + 15
        resp = self._client._post(f"/session/{self.id}/exec", body, timeout=http_timeout)
        self.state = "active"
        return RunResult._from_session_run(resp)

    # ── lifecycle (auto-pause is the renderops differentiator) ───────────────
    def pause(self) -> "Sandbox":
        """Snapshot to disk and free RAM+slot. Resumes on the next run()/exec() or resume()."""
        self._client._post(f"/session/{self.id}/pause", {})
        self.state = "paused"
        return self

    def resume(self) -> "Sandbox":
        """Restore a paused sandbox to running state."""
        self._client._post(f"/session/{self.id}/resume", {})
        self.state = "active"
        return self

    def destroy(self) -> None:
        """Destroy the sandbox and release all resources."""
        self._client._delete(f"/session/{self.id}")
        self.state = "destroyed"

    close = destroy  # backward-compat alias

    def __enter__(self) -> "Sandbox":
        return self

    def __exit__(self, *_) -> None:
        self.destroy()

    def __repr__(self) -> str:
        return f"Sandbox(id={self.id!r}, name={self.name!r}, state={self.state!r})"


class Renderops:
    """Renderops client — create, connect to, and list sandboxes.

    Example
    -------
    >>> from renderops import Renderops
    >>> ro = Renderops(api_key="ro_live_...")
    >>> box = ro.create(name="pandas-etl", metadata={"job": "etl"})
    >>> print(box.run("print(1 + 1)").stdout)   # 2
    >>> box.pause()
    >>> for b in ro.list():
    ...     print(b.name, b.state)
    """

    def __init__(self, api_key: str, base_url: str = "http://localhost:8080", timeout: int = 60):
        if not api_key or not isinstance(api_key, str):
            raise ValueError("api_key must be a non-empty string")
        self.base_url = base_url.rstrip("/")
        self._timeout = timeout
        self._headers = {"Content-Type": "application/json", "Authorization": f"Bearer {api_key}"}

    # ── sandboxes ────────────────────────────────────────────────────────────
    def create(self, name: Optional[str] = None, metadata: Optional[dict[str, Any]] = None,
               env: Optional[dict[str, str]] = None, resources: Optional[Resources] = None,
               network: Optional[NetworkConfig] = None, idle_timeout: Optional[int] = None,
               max_lifetime: Optional[int] = None) -> Sandbox:
        """Create a sandbox. `name` describes what it's for; `metadata` adds labels for list()."""
        body: dict = {}
        if name is not None:
            body["name"] = name
        if metadata is not None:
            body["metadata"] = metadata
        if env is not None:
            body["env"] = env
        if resources is not None:
            body["resources"] = resources.to_dict()
        if network is not None:
            body["network"] = network.to_dict()
        if idle_timeout is not None:
            body["idle_timeout_s"] = idle_timeout
        if max_lifetime is not None:
            body["max_lifetime_s"] = max_lifetime
        data = self._post("/session", body)
        s = data["session"]
        return Sandbox(self, id=s["session_id"], name=name, state=s.get("state"),
                       billing_model=s.get("billing_model"), metadata=metadata)

    def connect(self, sandbox_id: str) -> Sandbox:
        """Attach to an existing sandbox by id (e.g. to resume a paused one)."""
        data = self._get(f"/session/{sandbox_id}")
        s = data.get("session", {}) or {}
        return Sandbox(self, id=sandbox_id, name=s.get("name"), state=s.get("state"), billing_model=s.get("billing_model"))

    def list(self) -> list[Sandbox]:
        """List your sandboxes (name, state, metadata) — everything you have running/paused."""
        data = self._get("/session")
        out: list[Sandbox] = []
        for it in data.get("sandboxes", []):
            out.append(Sandbox(self, id=it["id"], name=it.get("name"), state=it.get("state"),
                               billing_model=it.get("billing_model"), metadata=it.get("metadata")))
        return out

    def run(self, code: str, language: str = "python", timeout: Optional[int] = None) -> RunResult:
        """One-off run in a fresh sandbox (no persistence). Use create() for a persistent one."""
        body: dict = {"code": code, "language": language}
        http_timeout: Optional[int] = None
        if timeout is not None:
            body["timeout"] = timeout
            http_timeout = timeout + 15
        return RunResult._from_execute(self._post("/execute", body, timeout=http_timeout))

    # ── internals ────────────────────────────────────────────────────────────
    def _post(self, path: str, body: dict, timeout: Optional[int] = None) -> dict:
        r = requests.post(f"{self.base_url}{path}", json=body, headers=self._headers, timeout=timeout or self._timeout)
        self._raise(r)
        return r.json() if r.content else {}

    def _get(self, path: str, timeout: Optional[int] = None) -> dict:
        r = requests.get(f"{self.base_url}{path}", headers=self._headers, timeout=timeout or self._timeout)
        self._raise(r)
        return r.json() if r.content else {}

    def _delete(self, path: str) -> None:
        r = requests.delete(f"{self.base_url}{path}", headers=self._headers, timeout=self._timeout)
        self._raise(r)

    @staticmethod
    def _raise(r: requests.Response) -> None:
        if r.status_code < 400:
            return
        if r.status_code == 401:
            raise AuthError(401, r.text)
        if r.status_code == 429:
            raise RateLimitError(429, r.text)
        if r.status_code == 404:
            raise SessionNotFoundError(404, r.text)
        if r.status_code >= 500:
            raise ServerError(r.status_code, r.text)
        raise APIError(r.status_code, r.text)


# ── backward-compat alias ────────────────────────────────────────────────────
# "Session" was the old name for a persistent sandbox → it's now "Sandbox".
Session = Sandbox
