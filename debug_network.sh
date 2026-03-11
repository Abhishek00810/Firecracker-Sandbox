#!/bin/bash
BASE="http://localhost:8080"
SID="$1"

if [ -z "$SID" ]; then
  echo "creating new session..."
  SID=$(curl -s -X POST "$BASE/session" -H 'Content-Type: application/json' | python3 -c "import sys,json; print(json.load(sys.stdin)['session_id'])")
  echo "session_id: $SID"
fi

run() {
  local label="$1"
  local code="$2"
  echo "==> $label"
  BODY=$(python3 -c "import json,sys; print(json.dumps({'code': sys.argv[1], 'language': 'python'}))" "$code")
  curl -s -X POST "$BASE/session/$SID/run" \
    -H 'Content-Type: application/json' \
    -d "$BODY" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['output'])"
  echo
}

run "ip addr" \
  "import subprocess
r = subprocess.run(['/sbin/ip', 'addr'], capture_output=True, text=True)
print(r.stdout + r.stderr)"

run "ip route" \
  "import subprocess
r = subprocess.run(['/sbin/ip', 'route'], capture_output=True, text=True)
print(r.stdout + r.stderr)"

run "ping gateway 172.16.6.1" \
  "import subprocess
r = subprocess.run(['ping', '-c', '2', '-W', '2', '172.16.6.1'], capture_output=True, text=True)
print(r.stdout + r.stderr)"

run "/proc/cmdline" \
  "import builtins
print(builtins.open('/proc/cmdline').read())"
