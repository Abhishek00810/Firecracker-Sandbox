from __future__ import annotations

import requests

from .exceptions import APIError, RateLimitError, SessionNotFoundError
from .models import RunResult


class Session:
    """Persistent execution session — variables survive between run() calls."""

    def __init__(self, session_id: str, tier: str, client: "Sandbox"):
        self.id = session_id
        self.tier = tier
        self._client = client

    def run(self, code: str, language: str = "python") -> RunResult:
        resp = self._client._post(
            f"/session/{self.id}/run",
            {"code": code, "language": language},
        )
        return RunResult._from_dict(resp)

    def close(self) -> None:
        self._client._delete(f"/session/{self.id}")

    # context manager support
    def __enter__(self) -> "Session":
        return self

    def __exit__(self, *_) -> None:
        self.close()

    def __repr__(self) -> str:
        return f"Session(id={self.id!r}, tier={self.tier!r})"

class Sandbox:
    """
    Client for the Nothing sandbox API.

    Parameters
    ----------
    base_url : str
        e.g. "http://localhost:8080"
    tier : str
        "free" or "premium"
    tenant_id : str
        Identifier used for per-tenant rate limiting.
    timeout : int
        Request timeout in seconds (default 60).
    """

    def __init__(
        self,
        base_url: str = "http://localhost:8080",
        tier: str = "free",
        tenant_id: str = "default",
        timeout: int = 60,
    ):
        self.base_url = base_url.rstrip("/")
        self._timeout = timeout
        self._headers = {
            "Content-Type": "application/json",
            "X-Tenant-Tier": tier,
            "X-Tenant-ID": tenant_id,
        }

    # ── public API ──────────────────────────────────────────────────────────

    def run(self, code: str, language: str = "python") -> RunResult:
        """Single-shot execution — no persistent state."""
        resp = self._post("/execute", {"code": code, "language": language})
        return RunResult._from_dict(resp["output"])

    def session(self, tier: str | None = None) -> Session:
        """Create a new persistent session."""
        headers = dict(self._headers)
        if tier is not None:
            headers["X-Tenant-Tier"] = tier

        r = requests.post(
            f"{self.base_url}/session",
            headers=headers,
            timeout=self._timeout,
        )
        self._raise(r)
        data = r.json()
        return Session(
            session_id=data["session_id"],
            tier=data["tier"],
            client=self,
        )

    # ── internals ───────────────────────────────────────────────────────────

    def _post(self, path: str, body: dict) -> dict:
        r = requests.post(
            f"{self.base_url}{path}",
            json=body,
            headers=self._headers,
            timeout=self._timeout,
        )
        self._raise(r)
        return r.json()

    def _delete(self, path: str) -> None:
        r = requests.delete(
            f"{self.base_url}{path}",
            headers=self._headers,
            timeout=self._timeout,
        )
        self._raise(r)

    @staticmethod
    def _raise(r: requests.Response) -> None:
        if r.status_code == 429:
            raise RateLimitError(429, r.text)
        if r.status_code == 404:
            raise SessionNotFoundError(404, r.text)
        if r.status_code >= 400:
            raise APIError(r.status_code, r.text)
