"""Stateless /execute tests via the SDK (sb.run)."""

import sys
from _config import make_client, check, summary


def main() -> int:
    sb = make_client()
    print("== stateless execute ==")

    r = sb.run("print(1 + 1)", language="python")
    check("python prints 2", r.stdout.strip() == "2", f"got {r.stdout!r}")
    check("python exit 0", r.ok, f"exit={r.exit_code}")

    r = sb.run("console.log('hi from node')", language="node")
    check("node prints", r.stdout.strip() == "hi from node", f"got {r.stdout!r}")

    r = sb.run("echo shell-works", language="bash")
    check("bash prints", r.stdout.strip() == "shell-works", f"got {r.stdout!r}")

    # a non-zero exit should surface
    r = sb.run("import sys; sys.exit(3)", language="python")
    check("nonzero exit surfaces", r.exit_code == 3, f"exit={r.exit_code}")

    return summary()


if __name__ == "__main__":
    sys.exit(main())
