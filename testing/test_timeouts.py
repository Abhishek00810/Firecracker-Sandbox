"""Per-execution timeout behaviour via the SDK.

Verifies behaviourally (not by reading the limit field): a task that finishes
within the timeout succeeds; one that exceeds it is terminated.
"""

import sys
from _config import make_client, check, summary


def main() -> int:
    sb = make_client()
    print("== per-execution timeout ==")

    # finishes well under the requested timeout
    r = sb.run("import time; time.sleep(1); print('done')", language="python", timeout=10)
    check("sleep(1) under timeout=10 succeeds", r.stdout.strip() == "done", f"got {r.stdout!r} reason={r.termination_reason}")

    # exceeds the requested timeout -> should be terminated (not print 'done')
    r = sb.run("import time; time.sleep(8); print('done')", language="python", timeout=2)
    check(
        "sleep(8) over timeout=2 is terminated",
        r.stdout.strip() != "done",
        f"got stdout={r.stdout!r} reason={r.termination_reason} exit={r.exit_code}",
    )

    return summary()


if __name__ == "__main__":
    sys.exit(main())
