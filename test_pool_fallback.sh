#!/bin/bash
BASE="http://20.228.220.165:8080"
AUTH="Authorization: Bearer ro_live_a136a05ac4d077d513584a2ad89a4202"
TOTAL=16

now_ms() { python3 -c "import time; print(int(time.time()*1000))"; }

SIDS_FILE="/tmp/pool_fallback_sids.$$"

create_session() {
  local idx=$1
  local tmpbody=$(mktemp)
  local tmptime=$(mktemp)

  curl -s -X POST "$BASE/session" \
    -H "Content-Type: application/json" \
    -H "$AUTH" \
    -o "$tmpbody" \
    -w "%{time_connect} %{time_starttransfer} %{time_total}" > "$tmptime"

  local times connect_ms ttfb_ms total_ms server_ms
  times=$(cat "$tmptime"); rm -f "$tmptime"
  connect_ms=$(python3  -c "t='$times'.split(); print(int(float(t[0])*1000))")
  ttfb_ms=$(python3     -c "t='$times'.split(); print(int(float(t[1])*1000))")
  total_ms=$(python3    -c "t='$times'.split(); print(int(float(t[2])*1000))")
  server_ms=$(python3   -c "t='$times'.split(); print(int((float(t[1])-float(t[0]))*1000))")

  local body sid err
  body=$(cat "$tmpbody"); rm -f "$tmpbody"
  sid=$(echo "$body" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('session',{}).get('session_id',''))" 2>/dev/null)

  if [ -n "$sid" ]; then
    echo "[${idx}] OK  total=${total_ms}ms  connect=${connect_ms}ms  server=${server_ms}ms  sid=${sid}"
    echo "$sid" >> "$SIDS_FILE"
  else
    err=$(echo "$body" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('message','unknown'))" 2>/dev/null)
    echo "[${idx}] FAIL total=${total_ms}ms: ${err:-$body}"
  fi
}

echo "==> warming auth cache..."
curl -s -X POST "$BASE/session" -H "Content-Type: application/json" -H "$AUTH" | python3 -c "import sys,json; d=json.load(sys.stdin); print('cache warm, sid:', d.get('session',{}).get('session_id','?'))" 2>/dev/null
sleep 1

echo "==> firing $TOTAL concurrent session creates..."
rm -f "$SIDS_FILE"
BATCH_START=$(now_ms)

for i in $(seq 1 $TOTAL); do
  create_session "$i" &
done

wait
BATCH_END=$(now_ms)
BATCH_MS=$((BATCH_END - BATCH_START))
echo ""
echo "==> all $TOTAL done in ${BATCH_MS}ms (wall clock)"
echo "==> cleaning up sessions..."

if [ -f "$SIDS_FILE" ]; then
  while IFS= read -r sid; do
    curl -s -X DELETE "$BASE/session/$sid" -H "$AUTH" > /dev/null
    echo "    destroyed $sid"
  done < "$SIDS_FILE"
  rm -f "$SIDS_FILE"
else
  echo "    no sessions to clean up"
fi

echo "==> done"
