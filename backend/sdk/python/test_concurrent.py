import threading
import time
import os

from renderops import Sandbox


sb = Sandbox(
    api_key=os.environ["RENDEROPS_API_KEY"],
    base_url=os.environ.get("RENDEROPS_BASE_URL", "http://localhost:8080"),
)
results = {}


def create_session(name):
    start = time.time()
    sess = sb.session(tier="pro")
    t1 = time.time() - start
    r = sess.run("print(1+1)")
    t2 = time.time() - start
    results[name] = {"created": round(t1, 2), "first_run": round(t2, 2), "output": r.stdout.strip()}
    sess.close()


t1 = threading.Thread(target=create_session, args=("vm1",))
t2 = threading.Thread(target=create_session, args=("vm2",))

overall_start = time.time()
t1.start()
t2.start()
t1.join()
t2.join()

print(f"total wall time: {time.time()-overall_start:.2f}s")
for name, r in results.items():
    print(f"{name}: created={r['created']}s  first_run={r['first_run']}s  output={r['output']}")
