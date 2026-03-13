class SandboxError(Exception):
    """Base exception for all SDK errors."""


class APIError(SandboxError):
    """HTTP error from the sandbox API."""

    def __init__(self, status_code: int, message: str):
        self.status_code = status_code
        super().__init__(f"[{status_code}] {message}")


class RateLimitError(APIError):
    """429 rate limit exceeded."""


class SessionNotFoundError(APIError):
    """Session ID not found on the server."""
