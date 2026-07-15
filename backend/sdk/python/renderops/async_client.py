from __future__ import annotations

from typing import Optional

import aiohttp

from .exceptions import APIError, AuthError, RateLimitError, ServerError, SessionNotFoundError
from .models import NetworkConfig, Resources, RunResult


class AsyncSession:
    """Persistent async execution session — variables survive between run() calls."""

    def __init__(self, session_id: str, billing_model: str, client: "AsyncSandbox"):
        self.id = session_id
        self.billing_model = billing_model
        self._client = client

    async def run(self, code: str, language: str = "python", timeout: Optional[int] = None) -> RunResult:
        """Run code inside this session. State is preserved across calls.

        timeout : int, optional
            Per-run timeout in seconds. Uses the server policy default if unset.
        """
        body: dict = {"code": code, "language": language}
        http_timeout: Optional[int] = None
        if timeout is not None:
            body["timeout"] = timeout
            http_timeout = timeout + 15  # keep the HTTP wait above the guest timeout

        resp = await self._client._post(f"/session/{self.id}/run", body, timeout=http_timeout)
        return RunResult._from_session_run(resp)

    async def exec(self, command: str, timeout: Optional[int] = None) -> RunResult:
        """Run a shell command inside this session's persistent workspace."""
        body: dict = {"command": command}
        http_timeout: Optional[int] = None
        if timeout is not None:
            body["timeout"] = timeout
            http_timeout = timeout + 15  # keep the HTTP wait above the guest timeout

        resp = await self._client._post(f"/session/{self.id}/exec", body, timeout=http_timeout)
        return RunResult._from_session_run(resp)

    async def close(self) -> None:
        """Destroy this session and release the VM."""
        await self._client._delete(f"/session/{self.id}")

    async def __aenter__(self) -> "AsyncSession":
        return self

    async def __aexit__(self, *_) -> None:
        await self.close()

    def __repr__(self) -> str:
        return f"AsyncSession(id={self.id!r}, billing_model={self.billing_model!r})"


class AsyncSandbox:
    """
    Async Renderops sandbox client. Use this when running multiple sandboxes
    concurrently — e.g. inside AI agents that spawn many parallel executions.

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
    >>> import asyncio
    >>> from sandbox import AsyncSandbox
    >>>
    >>> async def main():
    ...     sb = AsyncSandbox(api_key="ro_live_...")
    ...     result = await sb.run("print('hello')", language="python")
    ...     print(result.stdout)
    ...
    >>> asyncio.run(main())
    hello

    Running multiple sandboxes in parallel:
    >>> async def main():
    ...     sb = AsyncSandbox(api_key="ro_live_...")
    ...     r1, r2, r3 = await asyncio.gather(
    ...         sb.run("print(1)"),
    ...         sb.run("print(2)"),
    ...         sb.run("print(3)"),
    ...     )
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
        self._timeout = aiohttp.ClientTimeout(total=timeout)
        self._headers = {
            "Content-Type": "application/json",
            "Authorization": f"Bearer {api_key}",
        }

    # ── public API ──────────────────────────────────────────────────────────

    async def run(self, code: str, language: str = "python", timeout: Optional[int] = None) -> RunResult:
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

        resp = await self._post("/execute", body, timeout=http_timeout)
        return RunResult._from_execute(resp)

    async def session(self, env: Optional[dict[str, str]] = None, resources: Optional[Resources] = None, network: Optional[NetworkConfig] = None, idle_timeout: Optional[int] = None, max_lifetime: Optional[int] = None) -> AsyncSession:
        """
        Create a persistent async session. Variables survive between run() calls.
        Use as an async context manager to auto-close:

            async with await sb.session() as sess:
                await sess.run("x = 10")
                result = await sess.run("print(x)")

        idle_timeout : int, optional
            Seconds of inactivity before the session is reaped (default 5m, capped at max_lifetime).
        max_lifetime : int, optional
            Hard ceiling in seconds before the session is killed (default/cap 24h).
        """
        body: dict = {}
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

        async with aiohttp.ClientSession(headers=self._headers, timeout=self._timeout) as http:
            async with http.post(f"{self.base_url}/session", json=body) as r:
                await self._raise(r)
                data = await r.json()
                return AsyncSession(
                    session_id=data["session"]["session_id"],
                    billing_model=data["session"]["billing_model"],
                    client=self,
                )

    # ── internals ───────────────────────────────────────────────────────────

    async def _post(self, path: str, body: dict, timeout: Optional[int] = None) -> dict:
        t = aiohttp.ClientTimeout(total=timeout) if timeout is not None else self._timeout
        async with aiohttp.ClientSession(headers=self._headers, timeout=t) as http:
            async with http.post(f"{self.base_url}{path}", json=body) as r:
                await self._raise(r)
                return await r.json()

    async def _delete(self, path: str) -> None:
        async with aiohttp.ClientSession(headers=self._headers, timeout=self._timeout) as http:
            async with http.delete(f"{self.base_url}{path}") as r:
                await self._raise(r)

    @staticmethod
    async def _raise(r: aiohttp.ClientResponse) -> None:
        if r.status < 400:
            return
        text = await r.text()
        if r.status == 401:
            raise AuthError(401, text)
        if r.status == 429:
            raise RateLimitError(429, text)
        if r.status == 404:
            raise SessionNotFoundError(404, text)
        if r.status >= 500:
            raise ServerError(r.status, text)
        raise APIError(r.status, text)
