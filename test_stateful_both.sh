#!/bin/bash

set -euo pipefail

BASE_URL="${BASE_URL:-http://20.228.220.165:8080}"
API_KEY="${API_KEY:-ro_live_9654c4d39bc996e7af312276c0bbb5eb}"

now_ms() {
  python3 -c 'import time; print(int(time.time() * 1000))'
}

json_get() {
  local expr="$1"
  python3 -c 'import json, sys; data = json.load(sys.stdin); value = data'"$expr"'; print("" if value is None else value)'
}

create_session() {
  curl -s -X POST "$BASE_URL/session" \
    -H "Authorization: Bearer $API_KEY" \
    -H "Content-Type: application/json" \
    -d '{}'
}

run_session() {
  local session_id="$1"
  local language="$2"
  local code="$3"
  curl -s -X POST "$BASE_URL/session/$session_id/run" \
    -H "Authorization: Bearer $API_KEY" \
    -H "Content-Type: application/json" \
    -d "{\"code\":$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$code"),\"language\":\"$language\"}"
}

close_session() {
  local session_id="$1"
  curl -s -X DELETE "$BASE_URL/session/$session_id" \
    -H "Authorization: Bearer $API_KEY" > /dev/null
}

print_run() {
  local label="$1"
  local resp="$2"
  local latency_ms="$3"
  echo "$resp"
  echo "    client latency     : ${latency_ms}ms"
  echo "    guest_duration_ms  : $(printf '%s' "$resp" | json_get '["result"]["guest_duration_ms"]')"
  echo "    stdout             : $(printf '%s' "$resp" | json_get '["result"]["stdout"]')"
  echo "    stderr             : $(printf '%s' "$resp" | json_get '["result"]["stderr"]')"
}

echo "==> [1] Create Node stateful session"
start=$(now_ms)
node_session_resp=$(create_session)
end=$(now_ms)
echo "$node_session_resp"
NODE_SESSION_ID=$(printf '%s' "$node_session_resp" | json_get '["session"]["session_id"]')
echo "    node_session_id : $NODE_SESSION_ID"
echo "    client latency  : $((end - start))ms"

echo ""
echo "==> [2] Node store value"
start=$(now_ms)
node_store_resp=$(run_session "$NODE_SESSION_ID" "node" 'globalThis.saved = { count: 7, note: "node-state" }; console.log("stored", JSON.stringify(globalThis.saved))')
end=$(now_ms)
print_run "node-store" "$node_store_resp" "$((end - start))"

echo ""
echo "==> [3] Node read value"
start=$(now_ms)
node_read_resp=$(run_session "$NODE_SESSION_ID" "node" 'console.log("read", JSON.stringify(globalThis.saved), "triple", globalThis.saved.count * 3)')
end=$(now_ms)
print_run "node-read" "$node_read_resp" "$((end - start))"

echo ""
echo "==> [4] Create Python stateful session"
start=$(now_ms)
python_session_resp=$(create_session)
end=$(now_ms)
echo "$python_session_resp"
PYTHON_SESSION_ID=$(printf '%s' "$python_session_resp" | json_get '["session"]["session_id"]')
echo "    python_session_id : $PYTHON_SESSION_ID"
echo "    client latency    : $((end - start))ms"

echo ""
echo "==> [5] Python store value"
start=$(now_ms)
python_store_resp=$(run_session "$PYTHON_SESSION_ID" "python" $'saved = {"count": 7, "note": "python-state"}\nprint("stored", saved)')
end=$(now_ms)
print_run "python-store" "$python_store_resp" "$((end - start))"

echo ""
echo "==> [6] Python read value"
start=$(now_ms)
python_read_resp=$(run_session "$PYTHON_SESSION_ID" "python" $'print("read", saved, "triple", saved["count"] * 3)')
end=$(now_ms)
print_run "python-read" "$python_read_resp" "$((end - start))"

echo ""
echo "==> [7] Close sessions"
close_session "$NODE_SESSION_ID"
close_session "$PYTHON_SESSION_ID"
echo "    done"
