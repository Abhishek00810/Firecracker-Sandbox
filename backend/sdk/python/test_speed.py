import time
from sandbox import Sandbox

sb = Sandbox()

print("creating session...")
start = time.time()
sess = sb.session()
print(f"session created: {time.time()-start:.2f}s")

start = time.time()
r = sess.run("print(1+1)")
print(f"first run: {time.time()-start:.2f}s  output: {r.output.strip()}")

start = time.time()
r = sess.run("print(2+2)")
print(f"second run: {time.time()-start:.2f}s  output: {r.output.strip()}")

sess.close()
