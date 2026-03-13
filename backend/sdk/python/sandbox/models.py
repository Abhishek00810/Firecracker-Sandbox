from dataclasses import dataclass


@dataclass
class RunResult:
    output: str
    exit_code: int
    duration: float
    termination_reason: str

    @property
    def ok(self) -> bool:
        return self.exit_code == 0

    def __str__(self) -> str:
        return self.output

    @classmethod
    def _from_dict(cls, d: dict) -> "RunResult":
        return cls(
            output=d.get("output", ""),
            exit_code=int(d.get("exit_code", 0)),
            duration=float(d.get("duration", 0.0)),
            termination_reason=d.get("termination_reason", ""),
        )
