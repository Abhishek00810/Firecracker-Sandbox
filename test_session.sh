#!/bin/bash
set -e
BASE="http://localhost:8080"

echo "==> creating session..."
RESP=$(curl -s -X POST "$BASE/session" -H 'Content-Type: application/json')
echo "raw: $RESP"
SID=$(echo "$RESP" | python3 -c "import sys,json; print(json.load(sys.stdin)['session_id'])")
echo "session_id: $SID"
echo

run() {
  local label="$1"
  local code="$2"
  echo "==> $label"
  BODY=$(python3 -c "import json,sys; print(json.dumps({'code': sys.argv[1], 'language': 'python'}))" "$code")
  curl -s -X POST "$BASE/session/$SID/run" \
    -H 'Content-Type: application/json' \
    -d "$BODY" | python3 -m json.tool
  echo
}

# ── basic state persistence ────────────────────────────────────────────────────
run "[1/6] x = 100" "x = 100"
run "[2/6] x *= 3" "x *= 3"
run "[3/6] print(x)  # expect 300" "print(x)"

# ── runtime pip install (requires TAP networking) ─────────────────────────────
# cowsay is tiny (~20KB), pure Python, no native deps
run "[4/6] pip install cowsay at runtime" \
"import subprocess
r = subprocess.run(
    ['pip3', 'install', '--break-system-packages', '--no-cache-dir', 'cowsay'],
    capture_output=True, text=True
)
print((r.stdout + r.stderr)[-400:])
print('exit code:', r.returncode)"

run "[5/6] import freshly installed cowsay" \
"import cowsay
cowsay.cow('installed at runtime!')"

run "[6/6] library + variable state together" \
"import cowsay
cowsay.cow(f'x is still {x}')"
