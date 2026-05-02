#!/bin/bash

HOST="${1:-http://20.228.220.165:8080}"
KEY="ro_live_9654c4d39bc996e7af312276c0bbb5eb"

echo "==> Testing against: $HOST"
echo ""

echo "--- /health ---"
curl -s "$HOST/health" | python3 -m json.tool
echo ""

echo "--- /execute bash ---"
time curl -s -X POST "$HOST/execute" -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" -d '{"code":"echo hello from bash","language":"bash"}' | python3 -m json.tool
echo ""

echo "--- /session python stateful (create + run) ---"
SESSION_ID=$(curl -s -X POST "$HOST/session" -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" -d '{"language":"python"}' | python3 -c "import sys,json; print(json.load(sys.stdin)['session']['session_id'])")
echo "session_id: $SESSION_ID"
time curl -s -X POST "$HOST/session/$SESSION_ID/run" -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" -d '{"code":"x = sum(range(100000)); print(x)","language":"python"}' | python3 -m json.tool
curl -s -X DELETE "$HOST/session/$SESSION_ID" -H "Authorization: Bearer $KEY" > /dev/null
echo ""

echo "--- /session node stateful (create + run) ---"
NODE_SESSION_ID=$(curl -s -X POST "$HOST/session" -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" -d '{"language":"node"}' | python3 -c "import sys,json; print(json.load(sys.stdin)['session']['session_id'])")
echo "session_id: $NODE_SESSION_ID"
time curl -s -X POST "$HOST/session/$NODE_SESSION_ID/run" -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" -d '{"code":"const nums = Array.from({length: 100000}, (_, i) => i * i); console.log(JSON.stringify({sum: nums.reduce((a, b) => a + b, 0), count: nums.length}));","language":"node"}' | python3 -m json.tool
curl -s -X DELETE "$HOST/session/$NODE_SESSION_ID" -H "Authorization: Bearer $KEY" > /dev/null
echo ""

echo "--- /metrics ---"
curl -s "$HOST/metrics" | python3 -m json.tool
echo ""
