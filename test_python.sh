#!/bin/bash

set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
API_KEY="${API_KEY:-ro_live_9654c4d39bc996e7af312276c0bbb5eb}"

now_ms() {
  python3 -c 'import time; print(int(time.time() * 1000))'
}

json_get() {
  local expr="$1"
  python3 -c 'import json, sys; data = json.load(sys.stdin); value = data'"$expr"'; print("" if value is None else value)'
}

echo "==> [1] Create one Python session (single VM)"
start=$(now_ms)
session_resp=$(curl -s -X POST "$BASE_URL/session" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{}')
end=$(now_ms)

echo "$session_resp"

SESSION_ID=$(printf '%s' "$session_resp" | json_get '["session"]["session_id"]')
if [ -z "$SESSION_ID" ]; then
  echo "failed to create session"
  exit 1
fi

echo "    session_id     : $SESSION_ID"
echo "    client latency : $((end - start))ms"

echo ""
echo "==> [2] Python session run 1 (first hit on same VM)"
start=$(now_ms)
run1_resp=$(curl -s -X POST "$BASE_URL/session/$SESSION_ID/run" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"code":"x = globals().get(\"x\", 0) + 1\nprint(\"x\", x)","language":"python"}')
end=$(now_ms)

echo "$run1_resp"
echo "    client latency     : $((end - start))ms"
echo "    guest_duration_ms  : $(printf '%s' "$run1_resp" | json_get '["result"]["guest_duration_ms"]')"
echo "    stdout             : $(printf '%s' "$run1_resp" | json_get '["result"]["stdout"]')"
echo "    stderr             : $(printf '%s' "$run1_resp" | json_get '["result"]["stderr"]')"

echo ""
echo "==> [3] Python session run 2 (warm path, same VM)"
start=$(now_ms)
run2_resp=$(curl -s -X POST "$BASE_URL/session/$SESSION_ID/run" \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"code":"x = globals().get(\"x\", 0) + 1\nprint(\"x\", x)","language":"python"}')
end=$(now_ms)

echo "$run2_resp"
echo "    client latency     : $((end - start))ms"
echo "    guest_duration_ms  : $(printf '%s' "$run2_resp" | json_get '["result"]["guest_duration_ms"]')"
echo "    stdout             : $(printf '%s' "$run2_resp" | json_get '["result"]["stdout"]')"
echo "    stderr             : $(printf '%s' "$run2_resp" | json_get '["result"]["stderr"]')"

echo ""
echo "==> [4] Close session"
curl -s -X DELETE "$BASE_URL/session/$SESSION_ID" \
  -H "Authorization: Bearer $API_KEY" > /dev/null
echo "    done"
