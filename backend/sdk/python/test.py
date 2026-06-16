#!/usr/bin/env python3
"""
test.py — exercises the renderops SDK against a running server, focused on the
new Resources (vcpu / ram / disk) feature plus basic session exec/run.

Run:
    export RENDEROPS_API_KEY=ro_live_...
    export RENDEROPS_BASE_URL=http://localhost:8080   # tunnel to the box, or on the box
    python test.py
"""
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from renderops import Sandbox, Resources, NetworkConfig, APIError  # noqa: E402

API_KEY = os.environ.get("RENDEROPS_API_KEY")
BASE_URL = os.environ.get("RENDEROPS_BASE_URL", "http://localhost:8080")
if not API_KEY:
    sys.exit("set RENDEROPS_API_KEY")

sb = Sandbox(api_key=API_KEY, base_url=BASE_URL)

passed = failed = 0


def check(name, cond, detail=""):
    global passed, failed
    if cond:
        passed += 1
        print(f"  PASS  {name}  {detail}")
    else:
        failed += 1
        print(f"  FAIL  {name}  {detail}")


print(f"== target: {BASE_URL} ==\n")

print("== 1. stateless run ==")
r = sb.run("print(6*7)", language="python")
check("execute returns 42", r.stdout.strip() == "42", f"(stdout={r.stdout!r})")

print("== 2. session with explicit allowed Resources(1 vCPU / 256MB) ==")
with sb.session(resources=Resources(vcpus=1, memory_mb=256, disk_gb=0)) as s:
    check("session created", bool(s.id), f"(id={s.id})")
    er = s.exec("echo hi; echo nproc=$(nproc); echo mem_mb=$(free -m | awk '/Mem:/{print $2}')")
    check("exec ok", er.ok, f"(exit={er.exit_code})")
    print("    guest sees:", " | ".join(er.stdout.split()))

print("== 3. session with NO resources (server default) ==")
with sb.session() as s:
    check("default session created", bool(s.id), f"(id={s.id})")
    rr = s.run("print(2 + 2)", language="python")
    check("run returns 4", rr.stdout.strip() == "4", f"(stdout={rr.stdout!r})")

print("== 4. unsupported Resources -> rejected with 400 ==")
try:
    bad = sb.session(resources=Resources(vcpus=99, memory_mb=99999, disk_gb=500))
    bad.close()
    check("unsupported size rejected", False, "(server ACCEPTED a bad size!)")
except APIError as e:
    check("unsupported size rejected", e.status_code == 400, f"(got {e.status_code}: {str(e)[:90]})")

_NET = "python3 -c \"import urllib.request as u; u.urlopen('https://example.com', timeout=8); print('NET_OK')\" 2>&1 || echo NET_BLOCKED"

print("== 5. session with internet OFF -> egress blocked ==")
with sb.session(network=NetworkConfig(internet=False)) as s:
    check("no-internet session created", bool(s.id), f"(id={s.id})")
    er = s.exec(_NET, timeout=20)
    check("egress blocked", "NET_OK" not in er.stdout, f"(out={er.stdout.strip()[:80]!r})")

print("== 6. session with internet ON -> egress works ==")
with sb.session(network=NetworkConfig(internet=True)) as s:
    er = s.exec(_NET, timeout=20)
    check("egress works", "NET_OK" in er.stdout, f"(out={er.stdout.strip()[:80]!r})")

print(f"\n== {passed} passed, {failed} failed ==")
sys.exit(1 if failed else 0)
