import time
import os

from renderops import Sandbox


def main() -> None:
    sb = Sandbox(
        api_key=os.environ["RENDEROPS_API_KEY"],
        base_url=os.environ.get("RENDEROPS_BASE_URL", "http://localhost:8080"),
    )

    t = time.time()
    sess = sb.session(tier="pro")
    print(f"session created: {time.time() - t:.2f}s id={sess.id}")

    t = time.time()
    r = sess.run("print(1+1)", language="python")
    print(f"first run: {time.time() - t:.2f}s output={r.stdout.strip()} stderr={r.stderr.strip()}")

    t = time.time()
    r = sess.run("print(2+2)", language="python")
    print(f"second run: {time.time() - t:.2f}s output={r.stdout.strip()} stderr={r.stderr.strip()}")

    sess.close()


if __name__ == "__main__":
    main()
