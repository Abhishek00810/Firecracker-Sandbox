"""
Quick smoke test — requires the server running on localhost:8080.
Run: python test_sdk.py
"""
from sandbox import Sandbox

sb = Sandbox(base_url="http://localhost:8080", tenant_id="sdk-test")

# ── 1. single-shot ────────────────────────────────────────────────────────────
print("==> single-shot")
result = sb.run("print('hello from sdk')")
print(f"  output   : {result.output!r}")
print(f"  exit_code: {result.exit_code}")
print(f"  duration : {result.duration:.3f}s")
print(f"  ok       : {result.ok}")

# ── 2. session with persistent state ─────────────────────────────────────────
print("\n==> session: state persistence")
with sb.session() as sess:
    sess.run("x = 100")
    sess.run("x *= 3")
    r = sess.run("print(x)")
    print(f"  x = {r.output.strip()}")   # expect 300

# ── 3. session: runtime pip install + use ─────────────────────────────────────
print("\n==> session: runtime install + use")
with sb.session() as sess:
    r = sess.run(
        "import subprocess\n"
        "p = subprocess.run(['pip3','install','--break-system-packages','--no-cache-dir','cowsay'],"
        "capture_output=True,text=True)\n"
        "print('exit:', p.returncode)"
    )
    print(f"  pip install: {r.output.strip()}")

    r = sess.run("import cowsay; cowsay.cow('sdk works!')")
    print(r.output)
