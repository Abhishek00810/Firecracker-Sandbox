from __future__ import annotations

from typing import Optional

import requests

from .exceptions import APIError, AuthError, RateLimitError, ServerError, SessionNotFoundError
from .models import NetworkConfig, Resources, RunResult


class Session:
    """Persistent execution session — variables survive between run() calls."""

    def __init__(self, session_id: str, tier: str, client: "Sandbox"):
        self.id = session_id
        self.tier = tier
        self._client = client

    def run(self, code: str, language: str = "python", timeout: Optional[int] = None) -> RunResult:
        """Run code inside this session. State is preserved across calls.

        timeout : int, optional
            Per-run timeout in seconds. Uses the server default if unset; capped at the tier max.
        """
        body: dict = {"code": code, "language": language}
        http_timeout: Optional[int] = None
        if timeout is not None:
            body["timeout"] = timeout
            http_timeout = timeout + 15  # keep the HTTP wait above the guest timeout

        resp = self._client._post(f"/session/{self.id}/run", body, timeout=http_timeout)
        return RunResult._from_session_run(resp)

    def exec(self, command: str, timeout: Optional[int] = None) -> RunResult:
        """Run a shell command inside this session's persistent workspace."""
        body: dict = {"command": command}
        http_timeout: Optional[int] = None
        if timeout is not None:
            body["timeout"] = timeout
            http_timeout = timeout + 15  # keep the HTTP wait above the guest timeout

        resp = self._client._post(f"/session/{self.id}/exec", body, timeout=http_timeout)
        return RunResult._from_session_run(resp)

    def close(self) -> None:
        """Destroy this session and release the VM."""
        self._client._delete(f"/session/{self.id}")

    def __enter__(self) -> "Session":
        return self

    def __exit__(self, *_) -> None:
        self.close()

    def __repr__(self) -> str:
        return f"Session(id={self.id!r}, tier={self.tier!r})"


class Sandbox:
    """
    Renderops sandbox client.

    Parameters
    ----------
    api_key : str
        Your Renderops API key e.g. "ro_live_..."
    base_url : str
        API base URL. Defaults to "http://localhost:8080".
    timeout : int
        HTTP request timeout in seconds. Defaults to 60.

    Example
    -------
    >>> from sandbox import Sandbox
    >>> sb = Sandbox(api_key="ro_live_...")
    >>> result = sb.run("print('hello')", language="python")
    >>> print(result.stdout)
    hello
    """

    def __init__(
        self,
        api_key: str,
        base_url: str = "http://localhost:8080",
        timeout: int = 60,
    ):
        if not api_key or not isinstance(api_key, str):
            raise ValueError("api_key must be a non-empty string")

        self.base_url = base_url.rstrip("/")
        self._timeout = timeout
        self._headers = {
            "Content-Type": "application/json",
            "Authorization": f"Bearer {api_key}",
        }

    # ── public API ──────────────────────────────────────────────────────────

    def run(self, code: str, language: str = "python", timeout: Optional[int] = None) -> RunResult:
        """
        Run code in a fresh sandbox. No state is preserved between calls.

        Parameters
        ----------
        code : str
            Code to execute.
        language : str
            "python", "node", or "bash". Defaults to "python".
        timeout : int, optional
            Execution timeout in seconds. Uses server default if not set.
        """
        body: dict = {"code": code, "language": language}
        http_timeout: Optional[int] = None
        if timeout is not None:
            body["timeout"] = timeout
            http_timeout = timeout + 15  # keep the HTTP wait above the guest timeout

        resp = self._post("/execute", body, timeout=http_timeout)
        return RunResult._from_execute(resp)

    def session(self, env: Optional[dict[str, str]] = None, tier: Optional[str] = None, resources: Optional[Resources] = None, network: Optional[NetworkConfig] = None, idle_timeout: Optional[int] = None, max_lifetime: Optional[int] = None) -> Session:
        """
        Create a persistent session. Variables survive between run() calls.
        Use as a context manager to auto-close:

            with sb.session() as sess:
                sess.run("x = 10")
                result = sess.run("print(x)")

        idle_timeout : int, optional
            Seconds of inactivity before the session is reaped. Defaults to the
            server default (5m); capped at max_lifetime.
        max_lifetime : int, optional
            Hard ceiling in seconds before the session is killed regardless of
            activity. Defaults to / capped at the server ceiling (24h).
        """
        body: dict = {}
        if env is not None:
            body["env"] = env
        if tier is not None:
            body["tier"] = tier
        if resources is not None:
            body["resources"] = resources.to_dict()
        if network is not None:
            body["network"] = network.to_dict()
        if idle_timeout is not None:
            body["idle_timeout_s"] = idle_timeout
        if max_lifetime is not None:
            body["max_lifetime_s"] = max_lifetime

        r = requests.post(
            f"{self.base_url}/session",
            json=body,
            headers=self._headers,
            timeout=self._timeout,
        )
        self._raise(r)
        data = r.json()
        return Session(
            session_id=data["session"]["session_id"],
            tier=data["session"]["tier"],
            client=self,
        )

    # ── internals ───────────────────────────────────────────────────────────

    def _post(self, path: str, body: dict, timeout: Optional[int] = None) -> dict:
        r = requests.post(
            f"{self.base_url}{path}",
            json=body,
            headers=self._headers,
            timeout=timeout or self._timeout,
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
