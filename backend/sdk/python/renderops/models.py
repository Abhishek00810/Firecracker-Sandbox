from dataclasses import dataclass
from typing import Optional


@dataclass
class Resources:
    """Compute shape of a sandbox (also the billing basis): vCPU, RAM (MB), disk (GB).

    Omitted/None fields fall back to the default size on the server.
    """

    vcpus: Optional[int] = None
    memory_mb: Optional[int] = None
    disk_gb: Optional[int] = None

    def to_dict(self) -> dict:
        out: dict = {}
        if self.vcpus is not None:
            out["vcpus"] = self.vcpus
        if self.memory_mb is not None:
            out["memory_mb"] = self.memory_mb
        if self.disk_gb is not None:
            out["disk_gb"] = self.disk_gb
        return out


@dataclass
class NetworkConfig:
    """Network policy for a sandbox. internet=False blocks outbound egress
    (host<->guest control still works). allowed_domains / expose_ports are not
    supported yet."""

    internet: bool = True

    def to_dict(self) -> dict:
        return {"internet": self.internet}


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
