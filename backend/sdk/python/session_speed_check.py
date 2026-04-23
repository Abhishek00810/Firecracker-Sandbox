import time

from renderops import Sandbox


def main() -> None:
    sb = Sandbox(api_key="ro_live_9654c4d39bc996e7af312276c0bbb5eb")

    t = time.time()
    sess = sb.session()
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
