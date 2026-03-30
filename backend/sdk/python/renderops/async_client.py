from __future__ import annotations

from typing import Optional

import aiohttp

from .exceptions import APIError, AuthError, RateLimitError, ServerError, SessionNotFoundError
from .models import RunResult


class AsyncSession:
    """Persistent async execution session — variables survive between run() calls."""

    def __init__(self, session_id: str, tier: str, client: "AsyncSandbox"):
        self.id = session_id
        self.tier = tier
        self._client = client

    async def run(self, code: str, language: str = "python") -> RunResult:
        """Run code inside this session. State is preserved across calls."""
        resp = await self._client._post(
            f"/session/{self.id}/run",
            {"code": code, "language": language},
        )
        return RunResult._from_session_run(resp)

    async def close(self) -> None:
        """Destroy this session and release the VM."""
        await self._client._delete(f"/session/{self.id}")

    async def __aenter__(self) -> "AsyncSession":
        return self

    async def __aexit__(self, *_) -> None:
        await self.close()

    def __repr__(self) -> str:
        return f"AsyncSession(id={self.id!r}, tier={self.tier!r})"


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
        if timeout is not None:
            body["timeout"] = timeout

        resp = await self._post("/execute", body)
        return RunResult._from_execute(resp)

    async def session(self) -> AsyncSession:
        """
        Create a persistent async session. Variables survive between run() calls.
        Use as an async context manager to auto-close:

            async with await sb.session() as sess:
                await sess.run("x = 10")
                result = await sess.run("print(x)")
        """
        async with aiohttp.ClientSession(headers=self._headers, timeout=self._timeout) as http:
            async with http.post(f"{self.base_url}/session") as r:
                await self._raise(r)
                data = await r.json()
                return AsyncSession(
                    session_id=data["session"]["session_id"],
                    tier=data["session"]["tier"],
                    client=self,
                )

    # ── internals ───────────────────────────────────────────────────────────

    async def _post(self, path: str, body: dict) -> dict:
        async with aiohttp.ClientSession(headers=self._headers, timeout=self._timeout) as http:
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
