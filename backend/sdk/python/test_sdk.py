import asyncio
from renderops import Sandbox, AsyncSandbox

API_KEY = "ro_live_ac2a8d7c7c5008b17b2d24e4d34390b4"

# ── sync ──────────────────────────────────────────────────────────────────────

print("==> sync: single-shot")
sb = Sandbox(api_key=API_KEY)
result = sb.run("print(1+1)", language="python")
print("stdout:", result.stdout)
print("ok:", result.ok)
print("duration_ms:", result.duration_ms)
print("request_id:", result.request_id)

print("\n==> sync: session persistent state")
with sb.session() as sess:
    sess.run("x = 100")
    sess.run("x *= 3")
    r = sess.run("print(x)")
    print("x =", r.stdout.strip())

# ── async ─────────────────────────────────────────────────────────────────────

async def test_async():
    sb = AsyncSandbox(api_key=API_KEY)

    print("\n==> async: single-shot")
    result = await sb.run("print(1+1)", language="python")
    print("stdout:", result.stdout)
    print("ok:", result.ok)
    print("duration_ms:", result.duration_ms)

    print("\n==> async: 3 sandboxes in parallel")
    r1, r2, r3 = await asyncio.gather(
        sb.run("print('sandbox 1')"),
        sb.run("print('sandbox 2')"),
        sb.run("print('sandbox 3')"),
    )
    print("r1:", r1.stdout.strip())
    print("r2:", r2.stdout.strip())
    print("r3:", r3.stdout.strip())

    print("\n==> async: session persistent state")
    async with await sb.session() as sess:
        await sess.run("x = 100")
        await sess.run("x *= 3")
        r = await sess.run("print(x)")
        print("x =", r.stdout.strip())

asyncio.run(test_async())
