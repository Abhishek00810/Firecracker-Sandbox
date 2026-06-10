import os

from renderops import Sandbox


api_key = os.environ["RENDEROPS_API_KEY"]
base_url = os.environ.get("RENDEROPS_BASE_URL", "http://localhost:8080")
github_token = os.environ.get("GITHUB_TOKEN")

sb = Sandbox(api_key=api_key, base_url=base_url)

# Single shot: no state is preserved between calls.
r = sb.run("print('hello from single shot')")
print(r.stdout)

# Session: Python state persists across run() calls.
with sb.session(tier="pro") as sess:
    sess.run("x = 10")
    sess.run("y = 20")
    r = sess.run("print(x + y)")
    print(r.stdout)  # 30

# Agent-style shell workflow: env injection plus shell commands.
env = {"GITHUB_TOKEN": github_token} if github_token else None
with sb.session(env=env, tier="pro") as sess:
    r = sess.exec("pwd && python --version", timeout=30)
    print(r.stdout)
