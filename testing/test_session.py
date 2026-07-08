"""Stateful session tests via the SDK (sb.session + sess.run/exec)."""

import sys
from _config import make_client, check, summary


def main() -> int:
    sb = make_client()
    print("== stateful session ==")

    with sb.create(idle_timeout=600, max_lifetime=3600) as sess:
        # in-memory state persists across run() calls
        sess.run("x = 100")
        sess.run("x *= 3")
        r = sess.run("print(x)")
        check("state persists across runs (x==300)", r.stdout.strip() == "300", f"got {r.stdout!r}")

        # shell exec in the same session
        r = sess.exec("echo exec-works")
        check("session exec works", r.stdout.strip() == "exec-works", f"got {r.stdout!r}")

        # files written on disk are visible to later calls in the same session
        sess.exec("echo persisted > /tmp/marker.txt")
        r = sess.exec("cat /tmp/marker.txt")
        check("file visible within session", r.stdout.strip() == "persisted", f"got {r.stdout!r}")

    print("== second independent session is clean ==")
    with sb.create() as sess:
        r = sess.run("print('x' in dir())")
        check("fresh session has no leaked state", r.stdout.strip() == "False", f"got {r.stdout!r}")

    return summary()


if __name__ == "__main__":
    sys.exit(main())
