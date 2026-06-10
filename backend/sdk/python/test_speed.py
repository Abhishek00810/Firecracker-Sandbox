import time
import os

from renderops import Sandbox


sb = Sandbox(
    api_key=os.environ["RENDEROPS_API_KEY"],
    base_url=os.environ.get("RENDEROPS_BASE_URL", "http://localhost:8080"),
)

print("creating session...")
start = time.time()
sess = sb.session(tier="pro")
print(f"session created: {time.time()-start:.2f}s")

start = time.time()
r = sess.run("print(1+1)")
print(f"first run: {time.time()-start:.2f}s  output: {r.stdout.strip()}")

start = time.time()
r = sess.run("print(2+2)")
print(f"second run: {time.time()-start:.2f}s  output: {r.stdout.strip()}")

sess.close()
