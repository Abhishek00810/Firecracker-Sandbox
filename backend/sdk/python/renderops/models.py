from dataclasses import dataclass


@dataclass
class RunResult:
    stdout: str
    stderr: str
    exit_code: int
    duration_ms: float
    request_id: str
    termination_reason: str

    @property
    def ok(self) -> bool:
        """True if the process exited with code 0."""
        return self.exit_code == 0

    def __str__(self) -> str:
        return self.stdout

    @classmethod
    def _from_execute(cls, resp: dict) -> "RunResult":
        result = resp.get("result") or {}
        return cls(
            stdout=result.get("stdout", ""),
            stderr=result.get("stderr", ""),
            exit_code=int(result.get("exit_code", 0)),
            duration_ms=float(result.get("duration_ms", 0.0)),
            request_id=resp.get("request_id", ""),
            termination_reason=result.get("termination_reason", ""),
        )

    @classmethod
    def _from_session_run(cls, resp: dict) -> "RunResult":
        result = resp.get("result") or {}
        return cls(
            stdout=result.get("stdout", ""),
            stderr=result.get("stderr", ""),
            exit_code=int(result.get("exit_code", 0)),
            duration_ms=float(result.get("duration_ms", 0.0)),
            request_id=resp.get("request_id", ""),
            termination_reason=result.get("termination_reason", ""),
        )
