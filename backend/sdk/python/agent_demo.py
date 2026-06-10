#!/usr/bin/env python3
"""
agent_demo.py — a real AI coding agent running entirely inside the sandbox.

This is NOT a script with hardcoded steps. An LLM (Claude) is given a task and a
set of tools; it decides what to do. Every tool call executes inside an isolated
Firecracker microVM via the renderops SDK — clone, read, edit, run tests, commit,
push, open a PR. The agent's reasoning runs here; its hands are the sandbox.

Run:
    export ANTHROPIC_API_KEY=sk-ant-...
    export RENDEROPS_API_KEY=ro_live_...
    export RENDEROPS_BASE_URL=http://localhost:8080      # tunnel/box
    export GITHUB_TOKEN=ghp_...                          # repo scope
    export TARGET_REPO=https://github.com/you/your-repo.git
    export TASK="Add a /health endpoint that returns {\"status\":\"ok\"} and a test, then open a PR."
    python agent_demo.py

Requires: pip install anthropic   (and the renderops SDK, which lives next to this file)
"""
from __future__ import annotations

import base64
import os
import shlex
import sys

# make the local `renderops` package importable regardless of CWD
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import anthropic  # noqa: E402
from renderops import Sandbox  # noqa: E402

# ── config ────────────────────────────────────────────────────────────────────
ANTHROPIC_API_KEY = os.environ.get("ANTHROPIC_API_KEY")
RENDEROPS_API_KEY = os.environ.get("RENDEROPS_API_KEY")
BASE_URL = os.environ.get("RENDEROPS_BASE_URL", "http://localhost:8080")
GITHUB_TOKEN = os.environ.get("GITHUB_TOKEN")
TARGET_REPO = os.environ.get("TARGET_REPO", "https://github.com/Abhishek00810/Sandbox-test.git")
TASK = os.environ.get(
    "TASK",
    "Add a file CONTRIBUTING.md with a short contribution guide, then open a PR.",
)
MODEL = os.environ.get("MODEL", "claude-sonnet-4-6")
MAX_TURNS = int(os.environ.get("MAX_TURNS", "40"))
EXEC_TIMEOUT = int(os.environ.get("EXEC_TIMEOUT", "60"))
MAX_OUTPUT = 4000  # truncate tool output to keep token use sane

SYSTEM = f"""You are an autonomous software engineer working INSIDE an isolated sandbox VM.
You have a fresh Linux workspace at /workspace. A GitHub token is already exported as
$GITHUB_TOKEN in your shell, so `git` (clone/push over https) and the GitHub REST API
both work without extra auth.

Your tools (run_command, read_file, write_file, list_files) all execute inside the sandbox.
Nothing runs on anyone's laptop. Work methodically:
  1. Clone the target repo into /workspace.
  2. Explore it (list_files / read_file) to understand structure and conventions.
  3. Make the change the task asks for.
  4. Verify it where reasonable (run tests / run the code).
  5. Create a new branch, commit, push it.
  6. Open a Pull Request via the GitHub API (curl with $GITHUB_TOKEN) against the
     repo's default branch, and report the PR URL.

Target repo: {TARGET_REPO}
Be concise in your reasoning. When the PR is open, state the PR URL and stop."""


def _truncate(s: str) -> str:
    if len(s) <= MAX_OUTPUT:
        return s
    return s[:MAX_OUTPUT] + f"\n...[truncated {len(s) - MAX_OUTPUT} chars]"


# ── tools (each runs inside the sandbox session) ───────────────────────────────
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
            f"mkdir -p \"$(dirname -- {shlex.quote(path)})\" && "
            f"printf %s {shlex.quote(b64)} | base64 -d > {shlex.quote(path)} && "
            f"echo wrote {shlex.quote(path)}"
        )
        r = sess.exec(cmd, timeout=20)
        return r.stdout if r.ok else f"error writing {path}: {r.stderr or r.stdout}"

    def list_files(path: str):
        r = sess.exec(f"ls -la -- {shlex.quote(path)}", timeout=20)
        return r.stdout if r.ok else f"error listing {path}: {r.stderr or r.stdout}"

    return {
        "run_command": run_command,
        "read_file": read_file,
        "write_file": write_file,
        "list_files": list_files,
    }


TOOL_SCHEMAS = [
    {
        "name": "run_command",
        "description": "Run a shell command in the sandbox workspace (git, ls, cat, pytest, python, curl, …). Returns exit_code, stdout, stderr.",
        "input_schema": {
            "type": "object",
            "properties": {
                "command": {"type": "string"},
                "timeout": {"type": "integer", "description": "seconds (optional)"},
            },
            "required": ["command"],
        },
    },
    {
        "name": "read_file",
        "description": "Read a text file in the sandbox and return its contents.",
        "input_schema": {
            "type": "object",
            "properties": {"path": {"type": "string"}},
            "required": ["path"],
        },
    },
    {
        "name": "write_file",
        "description": "Create or overwrite a file in the sandbox with the given text content.",
        "input_schema": {
            "type": "object",
            "properties": {"path": {"type": "string"}, "content": {"type": "string"}},
            "required": ["path", "content"],
        },
    },
    {
        "name": "list_files",
        "description": "List a directory in the sandbox (ls -la).",
        "input_schema": {
            "type": "object",
            "properties": {"path": {"type": "string"}},
            "required": ["path"],
        },
    },
]


def require(name: str, value):
    if not value:
        sys.exit(f"missing required env var: {name}")


def main():
    require("ANTHROPIC_API_KEY", ANTHROPIC_API_KEY)
    require("RENDEROPS_API_KEY", RENDEROPS_API_KEY)
    require("GITHUB_TOKEN", GITHUB_TOKEN)

    print(f"== task ==\n{TASK}\n")
    print(f"== repo == {TARGET_REPO}")
    print(f"== model == {MODEL}\n")

    sb = Sandbox(api_key=RENDEROPS_API_KEY, base_url=BASE_URL, timeout=EXEC_TIMEOUT + 30)
    llm = anthropic.Anthropic(api_key=ANTHROPIC_API_KEY)

    print("== creating sandbox (Pro, with GITHUB_TOKEN) ==")
    sess = sb.session(env={"GITHUB_TOKEN": GITHUB_TOKEN}, tier="pro")
    print(f"   sandbox: {sess.id}\n")
    tools = make_tools(sess)

    messages = [{"role": "user", "content": f"Task: {TASK}"}]
    try:
        for turn in range(1, MAX_TURNS + 1):
            resp = llm.messages.create(
                model=MODEL,
                max_tokens=4096,
                system=SYSTEM,
                tools=TOOL_SCHEMAS,
                messages=messages,
            )

            for block in resp.content:
                if block.type == "text" and block.text.strip():
                    print(f"\n[agent] {block.text.strip()}")

            if resp.stop_reason != "tool_use":
                print("\n== agent finished ==")
                break

            messages.append({"role": "assistant", "content": resp.content})
            results = []
            for block in resp.content:
                if block.type != "tool_use":
                    continue
                fn = tools.get(block.name)
                arg_preview = block.input.get("command") or block.input.get("path") or ""
                print(f"   → {block.name}({arg_preview})")
                try:
                    out = fn(**block.input) if fn else f"unknown tool {block.name}"
                except Exception as e:  # surface tool errors back to the agent
                    out = f"tool error: {e}"
                results.append(
                    {"type": "tool_result", "tool_use_id": block.id, "content": _truncate(out)}
                )
            messages.append({"role": "user", "content": results})
        else:
            print("\n== hit MAX_TURNS without finishing ==")
    finally:
        sess.close()
        print("\n== sandbox destroyed ==")


if __name__ == "__main__":
    main()
