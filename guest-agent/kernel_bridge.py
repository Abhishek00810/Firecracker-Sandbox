#!/usr/bin/env python3
"""
kernel_bridge.py — persistent Jupyter kernel proxy

One process per language, lives for the session lifetime.
Guest agent (Go) talks to this via stdin/stdout pipes.

stdin  → {"code": "x = 42", "timeout": 30}
stdout ← {"stdout": "42\n", "stderr": "", "exit_code": 0, "duration": 0.04}
"""

import sys
import os
import json
import time
import re
import traceback

def log(msg):
    sys.stderr.write(f"[kernel_bridge] {msg}\n")
    sys.stderr.flush()

log("importing jupyter_client...")
try:
    import jupyter_client
    log("jupyter_client imported ok")
except Exception as e:
    log(f"FAILED to import jupyter_client: {e}")
    sys.exit(1)

KERNEL_NAMES = {
    "python": "python3",
    "node":   "javascript",  # requires ijavascript
}

def strip_ansi(text):
    return re.sub(r"\x1b\[[0-9;]*[mK]", "", text)

def start_kernel(language):
    kernel_name = KERNEL_NAMES.get(language)
    if not kernel_name:
        print(json.dumps({
            "stdout": "", "stderr": f"unsupported language: {language}",
            "exit_code": 1, "duration": 0.0
        }), flush=True)
        sys.exit(1)

    # use IPC transport (Unix domain sockets) instead of TCP
    # avoids needing loopback interface (127.0.0.1) inside the VM
    os.makedirs("/tmp/jupyter", exist_ok=True)
    os.environ["JUPYTER_RUNTIME_DIR"] = "/tmp/jupyter"
    log(f"JUPYTER_RUNTIME_DIR=/tmp/jupyter")

    # log available kernel specs
    try:
        specs = jupyter_client.kernelspec.find_kernel_specs()
        log(f"available kernel specs: {list(specs.keys())}")
    except Exception as e:
        log(f"could not list kernel specs: {e}")

    log(f"creating KernelManager kernel_name={kernel_name} transport=ipc")
    try:
        km = jupyter_client.KernelManager(kernel_name=kernel_name, transport='ipc')
    except Exception as e:
        log(f"KernelManager creation failed: {e}\n{traceback.format_exc()}")
        sys.exit(1)

    log("starting kernel process...")
    try:
        km.start_kernel()
        log(f"kernel process started pid={km.kernel.pid if hasattr(km, 'kernel') else 'unknown'}")
    except Exception as e:
        log(f"start_kernel failed: {e}\n{traceback.format_exc()}")
        sys.exit(1)

    log("creating client and starting channels...")
    kc = km.client()
    kc.start_channels()

    log("waiting for kernel to be ready (timeout=30s)...")
    try:
        kc.wait_for_ready(timeout=30)
        log("kernel is ready!")
    except RuntimeError as e:
        log(f"wait_for_ready failed: {e}")
        # log kernel process status
        try:
            alive = km.is_alive()
            log(f"kernel still alive: {alive}")
        except Exception as e2:
            log(f"could not check kernel alive: {e2}")
        print(json.dumps({
            "stdout": "", "stderr": f"kernel failed to start: {e}",
            "exit_code": 1, "duration": 0.0
        }), flush=True)
        sys.exit(1)

    return km, kc

def execute(kc, km, code, timeout):
    start = time.time()
    log(f"executing code len={len(code)} timeout={timeout}")
    msg_id = kc.execute(code, allow_stdin=False)

    stdout_parts = []
    stderr_parts = []
    exit_code = 0
    deadline = time.time() + timeout

    while True:
        remaining = deadline - time.time()
        if remaining <= 0:
            km.interrupt_kernel()
            try:
                while True:
                    msg = kc.get_iopub_msg(timeout=2)
                    if (msg["msg_type"] == "status" and
                            msg["content"]["execution_state"] == "idle" and
                            msg["parent_header"].get("msg_id") == msg_id):
                        break
            except Exception:
                pass
            exit_code = -1
            stderr_parts.append(f"\n[Timeout after {timeout}s]")
            break

        try:
            msg = kc.get_iopub_msg(timeout=min(remaining, 1.0))
        except Exception:
            continue

        if msg["parent_header"].get("msg_id") != msg_id:
            continue

        msg_type = msg["msg_type"]
        content  = msg["content"]

        if msg_type == "stream":
            if content["name"] == "stdout":
                stdout_parts.append(content["text"])
            elif content["name"] == "stderr":
                stderr_parts.append(content["text"])

        elif msg_type == "execute_result":
            text = content["data"].get("text/plain", "")
            if text:
                stdout_parts.append(text + "\n")

        elif msg_type == "error":
            tb = strip_ansi("\n".join(content["traceback"]))
            stderr_parts.append(tb)
            exit_code = 1

        elif msg_type == "status" and content["execution_state"] == "idle":
            log(f"execution done exit_code={exit_code}")
            break

    return {
        "stdout":    "".join(stdout_parts),
        "stderr":    "".join(stderr_parts),
        "exit_code": exit_code,
        "duration":  time.time() - start,
    }

def main():
    if len(sys.argv) < 2:
        log("usage: kernel_bridge.py <language>")
        sys.exit(1)

    language = sys.argv[1]
    log(f"starting for language={language}")
    log(f"python={sys.executable} version={sys.version}")
    log(f"pid={os.getpid()}")

    km, kc = start_kernel(language)

    log("entering request loop, reading from stdin...")
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue

        try:
            req = json.loads(line)
        except json.JSONDecodeError as e:
            print(json.dumps({
                "stdout": "", "stderr": f"invalid json: {e}",
                "exit_code": 1, "duration": 0.0
            }), flush=True)
            continue

        code    = req.get("code", "")
        timeout = min(int(req.get("timeout", 30)), 30)

        result = execute(kc, km, code, timeout)
        print(json.dumps(result), flush=True)

    kc.stop_channels()
    km.shutdown_kernel()

if __name__ == "__main__":
    main()
