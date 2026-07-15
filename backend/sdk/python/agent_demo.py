#!/usr/bin/env python3
"""
agent_demo.py — an AI coding agent that runs entirely inside the sandbox.

Plain-English flow:
  1. Create a sandbox  → a real VM on the server.
  2. Give the AI a TASK + 4 tools (run_command, read_file, write_file, list_files).
  3. Loop: the AI picks a tool → we run it INSIDE the sandbox → send the result back.
  4. Stop when the AI says it's done. Destroy the sandbox.

The AI (Azure, e.g. gpt-5.1-codex-mini) is the brain; the sandbox is the hands.

Run:
    source /tmp/.az.env                       # AZURE_API_KEY / AZURE_BASE / AZURE_API_VERSION / AZURE_MODEL
    export RENDEROPS_API_KEY=ro_live_...
    export RENDEROPS_BASE_URL=http://localhost:8080
    export GITHUB_TOKEN=ghp_...               # optional, only for git/PR tasks
    export TASK="Write /workspace/fib.py printing the first 10 Fibonacci numbers, then run it."
    python agent_demo.py
"""
from __future__ import annotations

import base64
import json
import os
import shlex
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import requests  # noqa: E402
from renderops import Sandbox  # noqa: E402

# ── settings (read from environment variables) ─────────────────────────────────
RENDEROPS_API_KEY = os.environ.get("RENDEROPS_API_KEY")
BASE_URL = os.environ.get("RENDEROPS_BASE_URL", "http://localhost:8080")
GITHUB_TOKEN = os.environ.get("GITHUB_TOKEN")  # optional — only for git/PR tasks
TARGET_REPO = os.environ.get("TARGET_REPO", "https://github.com/Abhishek00810/Sandbox-test.git")
TASK = os.environ.get(
    "TASK",
    "Write /workspace/fib.py that prints the first 10 Fibonacci numbers, then run it with python3 and show the output.",
)

AZURE_BASE = os.environ.get("AZURE_BASE") or os.environ.get("AZURE_OPENAI_ENDPOINT")
AZURE_API_KEY = os.environ.get("AZURE_API_KEY") or os.environ.get("AZURE_OPENAI_API_KEY")
AZURE_API_VERSION = os.environ.get("AZURE_API_VERSION", "2025-04-01-preview")
AZURE_MODEL = os.environ.get("AZURE_MODEL", "gpt-5.1-codex-mini")

MAX_TURNS = int(os.environ.get("MAX_TURNS", "40"))
EXEC_TIMEOUT = int(os.environ.get("EXEC_TIMEOUT", "60"))
MAX_OUTPUT = 4000  # truncate tool output so we don't blow the token budget

_git_note = (
    "A GitHub token is exported as $GITHUB_TOKEN in your shell, so git (clone/push over https) "
    "and the GitHub REST API work. When the task needs it: create a branch, commit, push, open a "
    "PR via the GitHub API, and report the PR URL.\n"
    if GITHUB_TOKEN
    else "No GitHub token is configured; just do the task locally in /workspace.\n"
)
SYSTEM = (
    "You are an autonomous software engineer working INSIDE an isolated sandbox VM with a Linux "
    "workspace at /workspace. Your tools (run_command, read_file, write_file, list_files) all "
    "execute inside the sandbox; nothing runs on anyone's laptop.\n"
    + _git_note
    + f"Target repo (if relevant): {TARGET_REPO}\n"
    "Work methodically and be concise. When the task is complete, say so and stop."
)


def _truncate(s: str) -> str:
    return s if len(s) <= MAX_OUTPUT else s[:MAX_OUTPUT] + f"\n...[truncated {len(s) - MAX_OUTPUT} chars]"


# ── the 4 tools the AI can use — each one runs INSIDE the sandbox session ───────
def make_tools(sess):
    def run_command(command: str, timeout: int | None = None):
        r = sess.exec(command, timeout=timeout or EXEC_TIMEOUT)
        return f"exit_code={r.exit_code}\n--- stdout ---\n{r.stdout}\n--- stderr ---\n{r.stderr}"

    def read_file(path: str):
        r = sess.exec(f"cat -- {shlex.quote(path)}", timeout=20)
        return r.stdout if r.ok else f"error reading {path}: {r.stderr or r.stdout}"

    def write_file(path: str, content: str):
        b64 = base64.b64encode(content.encode()).decode()
        cmd = (
            f'mkdir -p "$(dirname -- {shlex.quote(path)})" && '
            f"printf %s {shlex.quote(b64)} | base64 -d > {shlex.quote(path)} && "
            f"echo wrote {shlex.quote(path)}"
        )
        r = sess.exec(cmd, timeout=20)
        return r.stdout if r.ok else f"error writing {path}: {r.stderr or r.stdout}"

    def list_files(path: str):
        r = sess.exec(f"ls -la -- {shlex.quote(path)}", timeout=20)
        return r.stdout if r.ok else f"error listing {path}: {r.stderr or r.stdout}"

    return {"run_command": run_command, "read_file": read_file, "write_file": write_file, "list_files": list_files}


# how we describe those 4 tools to the AI
_PARAMS = {
    "run_command": {"type": "object", "properties": {"command": {"type": "string"}, "timeout": {"type": "integer"}}, "required": ["command"]},
    "read_file": {"type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"]},
    "write_file": {"type": "object", "properties": {"path": {"type": "string"}, "content": {"type": "string"}}, "required": ["path", "content"]},
    "list_files": {"type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"]},
}
_DESC = {
    "run_command": "Run a shell command in the sandbox (git, ls, cat, python, pytest, curl…). Returns exit_code, stdout, stderr.",
    "read_file": "Read a text file in the sandbox and return its contents.",
    "write_file": "Create or overwrite a file in the sandbox with the given text.",
    "list_files": "List a directory in the sandbox (ls -la).",
}


def _preview(args: dict) -> str:
    return str(args.get("command") or args.get("path") or "")[:120]


# ── the agent loop (Azure OpenAI Responses API) ────────────────────────────────
def run_agent(task: str, tools: dict):
    url = f"{AZURE_BASE}/openai/responses?api-version={AZURE_API_VERSION}"
    headers = {"Content-Type": "application/json", "Authorization": f"Bearer {AZURE_API_KEY}"}
    schema = [{"type": "function", "name": n, "description": _DESC[n], "parameters": _PARAMS[n]} for n in tools]

    input_items = [{"role": "user", "content": task}]
    prev_id = None
    for turn in range(1, MAX_TURNS + 1):
        body = {"model": AZURE_MODEL, "input": input_items, "tools": schema, "max_output_tokens": 8192}
        if prev_id:
            body["previous_response_id"] = prev_id          # "remember the conversation so far"
        else:
            body["instructions"] = SYSTEM                   # system prompt on the first turn

        data = requests.post(url, headers=headers, json=body, timeout=300).json()
        if "error" in data and data["error"]:
            sys.exit(f"\nAzure error: {data['error']}")
        prev_id = data["id"]

        # read what the AI said + which tools it wants to run
        calls = []
        for item in data.get("output", []):
            if item.get("type") == "message":
                for c in item.get("content", []):
                    if c.get("type") == "output_text" and c.get("text", "").strip():
                        print(f"\n[agent] {c['text'].strip()}")
            elif item.get("type") == "function_call":
                calls.append(item)

        if not calls:                                       # no tool calls → the AI is done
            print("\n== agent finished ==")
            return

        # run each requested tool INSIDE the sandbox, then hand the results back
        next_input = []
        for call in calls:
            args = json.loads(call.get("arguments") or "{}")
            print(f"   → turn {turn}: {call['name']}({_preview(args)})")
            fn = tools.get(call["name"])
            try:
                out = fn(**args) if fn else f"unknown tool {call['name']}"
            except Exception as e:
                out = f"tool error: {e}"
            next_input.append({"type": "function_call_output", "call_id": call["call_id"], "output": _truncate(out)})
        input_items = next_input
    print("\n== hit MAX_TURNS without finishing ==")


# ── a simple startup diagnostic: show config + check we can reach the sandbox ───
def diagnostic():
    print("── diagnostic ──")
    print(f"  sandbox url   : {BASE_URL}")
    print(f"  sandbox key   : {'set' if RENDEROPS_API_KEY else 'MISSING'}")
    print(f"  azure base    : {AZURE_BASE or 'MISSING'}")
    print(f"  azure key     : {'set' if AZURE_API_KEY else 'MISSING'}")
    print(f"  azure model   : {AZURE_MODEL}")
    print(f"  github token  : {'set' if GITHUB_TOKEN else 'not set (local tasks only)'}")

    missing = [n for n, v in [("RENDEROPS_API_KEY", RENDEROPS_API_KEY), ("AZURE_BASE", AZURE_BASE), ("AZURE_API_KEY", AZURE_API_KEY)] if not v]
    if missing:
        sys.exit(f"  → MISSING required settings: {', '.join(missing)}")

    try:
        code = requests.get(f"{BASE_URL}/health", timeout=5).status_code
        print(f"  sandbox health: HTTP {code}" + ("  ✓" if code == 200 else "  (unexpected)"))
    except Exception as e:
        sys.exit(f"  sandbox NOT reachable at {BASE_URL}: {e}\n  → is the SSH tunnel up / are you on the box?")
    print("  → ready ✓\n")


def main():
    diagnostic()
    print(f"task: {TASK}\n")

    sb = Sandbox(api_key=RENDEROPS_API_KEY, base_url=BASE_URL, timeout=EXEC_TIMEOUT + 30)
    env = {"GITHUB_TOKEN": GITHUB_TOKEN} if GITHUB_TOKEN else None
    print("creating sandbox (Pro)…")
    sess = sb.session(env=env)
    print(f"sandbox ready: {sess.id}\n")

    try:
        run_agent(TASK, make_tools(sess))
    finally:
        sess.close()
        print("\nsandbox destroyed.")


if __name__ == "__main__":
    main()
