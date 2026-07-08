"""Shared setup for the SDK-based test scripts.

Adds the local Python SDK to sys.path and builds a Renderops client from env vars:
  RENDEROPS_BASE_URL  (default: the Azure VM)
  RENDEROPS_API_KEY   (required, must be ACTIVE in Supabase)

Run any test from the repo root, e.g.:
    python3 testing/test_execute.py
"""

import os
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
_REPO = os.path.dirname(_HERE)
_SDK = os.path.join(_REPO, "backend", "sdk", "python")
if _SDK not in sys.path:
    sys.path.insert(0, _SDK)

from renderops import Renderops  # noqa: E402

BASE_URL = os.environ.get("RENDEROPS_BASE_URL", "http://20.228.220.165:8080")
API_KEY = os.environ.get("RENDEROPS_API_KEY")
if not API_KEY:
    raise RuntimeError("RENDEROPS_API_KEY is required for live SDK tests")

# ── tiny PASS/FAIL helpers (no pytest dependency) ──────────────────────────────
_passed = 0
_failed = 0


def make_client(timeout: int = 90) -> Renderops:
    return Renderops(api_key=API_KEY, base_url=BASE_URL, timeout=timeout)


def check(label: str, condition: bool, detail: str = "") -> None:
    global _passed, _failed
    if condition:
        _passed += 1
        print(f"  PASS: {label}")
    else:
        _failed += 1
        print(f"  FAIL: {label}" + (f"  ({detail})" if detail else ""))


def summary() -> int:
    print(f"\n{'=' * 40}\n  {_passed} passed, {_failed} failed\n{'=' * 40}")
    return 1 if _failed else 0
