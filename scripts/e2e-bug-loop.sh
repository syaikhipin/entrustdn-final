#!/usr/bin/env bash
# e2e-bug-loop.sh — one-shot feedback loop for the browser-flow bugs (#3-#9).
# Red-capable: drives the real backend paths the pages drive, asserts each
# bug's exact symptom. Usage: scripts/e2e-bug-loop.sh [backend]
set -uo pipefail
B="${1:-http://localhost:8080}"
PASS=0; FAIL=0
ck() { # ck <label> <got> <want-substr>
  if grep -q "$3" <<<"$2"; then PASS=$((PASS+1)); else FAIL=$((FAIL+1)); echo "FAIL: $1"; echo "  got: $(head -c 300 <<<"$2")"; fi
}
J() { python3 -c "import sys,json;d=json.load(sys.stdin);print(d$1)" 2>/dev/null; }
STAMP=$RANDOM

# --- consumers get created verified via register+log-verify flow ---
CREG=$(curl -s -X POST "$B/api/v1/register" -H 'Content-Type: application/json' -d "{\"email\":\"loop-c$STAMP@t.dev\",\"password\":\"pass-word-1\",\"display_name\":\"Loop Consumer $STAMP\",\"role\":\"data_consumer\",\"tos_version\":\"1.0\"}")
VTOK=$(grep "loop-c$STAMP" /tmp/thresh-backend.log | tail -1 | sed -n 's/.*token=\([^ ]*\).*/\1/p')
[ -n "$VTOK" ] && curl -s -X POST "$B/api/v1/verify" -H 'Content-Type: application/json' -d "{\"token\":\"$VTOK\"}" >/dev/null
CLOGIN=$(curl -s -X POST "$B/api/v1/login" -H 'Content-Type: application/json' -d "{\"email\":\"loop-c$STAMP@t.dev\",\"password\":\"pass-word-1\"}")
CTOK=$(J "['session']['token']" <<<"$CLOGIN")
ck "consumer login nonempty" "$CTOK" ".."

# --- #13: /me must carry the TOS record ---
ME=$(curl -s "$B/api/v1/me" -H "Authorization: Bearer $CTOK")
ck "#13 tos version present" "$ME" "accepted_version"

# --- org account (pending -> approve via admin) ---
OREG=$(curl -s -X POST "$B/api/v1/register" -H 'Content-Type: application/json' -d "{\"email\":\"loop-o$STAMP@t.dev\",\"password\":\"pass-word-1\",\"display_name\":\"Loop Org $STAMP\",\"role\":\"farmer_organization\",\"tos_version\":\"1.0\"}")
OVTOK=$(grep "loop-o$STAMP" /tmp/thresh-backend.log | tail -1 | sed -n 's/.*token=\([^ ]*\).*/\1/p')
[ -n "$OVTOK" ] && curl -s -X POST "$B/api/v1/verify" -H 'Content-Type: application/json' -d "{\"token\":\"$OVTOK\"}" >/dev/null
OLOGIN=$(curl -s -X POST "$B/api/v1/login" -H 'Content-Type: application/json' -d "{\"email\":\"loop-o$STAMP@t.dev\",\"password\":\"pass-word-1\"}")
OTOK=$(J "['session']['token']" <<<"$OLOGIN")
ADMIN_LOGIN=$(curl -s -X POST "$B/api/v1/login" -H 'Content-Type: application/json' -d '{"email":"admin@thresh.dev","password":"admin-dev-pass"}')
ATOK=$(J "['session']['token']" <<<"$ADMIN_LOGIN")
# Seed a price book for the live model + grant the consumer spend room.
curl -s -X POST "$B/api/v1/admin/pricing" -H "Authorization: Bearer $ATOK" -H 'Content-Type: application/json' \
  -d '{"inference":[{"model":"Qwen3.8-27B","input_micros_per_1k":2500,"cached_input_micros_per_1k":1250,"output_micros_per_1k":10000}],"data":{"cached_asset_micros_per_unit":1000,"unique_micros_per_unit":5000}}' >/dev/null
# Grant the consumer spend room (chat charges the budget).
CAID=$(J "['account']['id']" <<<"$CLOGIN")
curl -s -X POST "$B/api/v1/admin/credits/grant" -H "Authorization: Bearer $ATOK" -H 'Content-Type: application/json' \
  -d "{\"account_id\":\"$CAID\",\"amount_micros\":1000000,\"memo\":\"e2e loop grant\"}" >/dev/null
APPS=$(curl -s "$B/api/v1/admin/applications" -H "Authorization: Bearer $ATOK")
OID=$(grep -o '[a-f0-9-]\{36\}' <<<"$(J "['applications'][-1]['id']" <<<"$APPS" 2>/dev/null || echo "$APPS")" | head -1)
# safer: parse id of the newest application by display name
OID=$(python3 - <<PY
import json,urllib.request
req=urllib.request.Request("$B/api/v1/admin/applications",headers={"Authorization":"Bearer $ATOK"})
apps=json.load(urllib.request.urlopen(req))["applications"]
print(next(a["id"] for a in apps if "$STAMP" in a["display_name"]))
PY
)
curl -s -X POST "$B/api/v1/admin/applications/decide" -H "Authorization: Bearer $ATOK" -H 'Content-Type: application/json' -d "{\"account_id\":\"$OID\",\"decision\":\"approve\"}" >/dev/null
# Re-login the org now that it's approved — the pre-approval session may
# carry the pending status.
OTOK=$(curl -s -X POST "$B/api/v1/login" -H 'Content-Type: application/json' -d "{\"email\":\"loop-o$STAMP@t.dev\",\"password\":\"pass-word-1\"}" | J "['session']['token']")
ck "org login after approval" "$OTOK" "."

# --- #5: the consumer endpoint stays consumer-gated for the org token ---
if curl -s "$B/api/v1/requests" -H "Authorization: Bearer $OTOK" | grep -q "for Data Consumers"; then
  PASS=$((PASS+1))
else FAIL=$((FAIL+1)); echo "FAIL: #5 consumer endpoint wrongly open to orgs"; fi

# --- consumer creates + clarifies a request (needs live LLM: real path).
# The model decides when a request is clarified — chat up to 4 turns.
REQ=$(curl -s -X POST "$B/api/v1/requests" -H "Authorization: Bearer $CTOK" -H 'Content-Type: application/json' -d '{"description":"Yield survey for winter cereal 2026, farm-level, Ireland","format":"csv","quality_bar":"answers must name a number","budget_micros":100000}')
RID=$(J "['request']['id']" <<<"$REQ")
ck "request created" "$RID" "[0-9a-f]"
CHAT=""; CLARIFIED=""
for i in 1 2 3 4; do
  CHAT=$(curl -s -m 90 -X POST "$B/api/v1/requests/$RID/chat" -H "Authorization: Bearer $CTOK" -H 'Content-Type: application/json' -d '{"message":"I need per-farm winter cereal yields for 2026 in Ireland, CSV, one row per farm, numeric tonnes/hectare. This is all the detail I have — treat it as clarified."}')
  CLARIFIED=$(J "['clarified']" <<<"$CHAT")
  [ "$CLARIFIED" = "True" ] && break
done
ck "#1 chat reply" "$CHAT" "reply"
ck "request clarified" "$CLARIFIED" "True"

# --- org list must include the clarified request ---
OREQS=$(curl -s "$B/api/v1/org/requests" -H "Authorization: Bearer $OTOK")
if grep -q "$RID" <<<"$OREQS"; then PASS=$((PASS+1)); else FAIL=$((FAIL+1)); echo "FAIL: #8 org view missing clarified request $RID"; fi

# --- #6: roster create must exist backend-side (already does); drive it ---
MEM=$(curl -s -X POST "$B/api/v1/members" -H "Authorization: Bearer $OTOK" -H 'Content-Type: application/json' -d '{"display_name":"Maire Ni Cheallaigh","contact":"email:maire@t.dev"}')
MID=$(J "['member']['id']" <<<"$MEM")
ck "#6 member created" "$MID" "[0-9a-f]"

# --- collection over the clarified request ---
COL=$(curl -s -X POST "$B/api/v1/collections" -H "Authorization: Bearer $OTOK" -H 'Content-Type: application/json' -d "{\"request_id\":\"$RID\",\"member_ids\":[\"$MID\"],\"questions\":[\"What was your 2026 winter cereal yield in tonnes per hectare?\"],\"deadline\":\"\"}")
CID=$(J "['collection']['id']" <<<"$COL")
ck "collection opened" "$CID" "[0-9a-f]"

# --- #7: start conversation, member replies, sync must ingest ---
CONV=$(curl -s -X POST "$B/api/v1/conversations" -H "Authorization: Bearer $OTOK" -H 'Content-Type: application/json' -d "{\"member_id\":\"$MID\",\"topic\":\"Gathering\",\"questions\":[\"What was your 2026 winter cereal yield in tonnes per hectare?\"],\"request_id\":\"$RID\"}")
CVT=$(J "['conversation']['resume_token']" <<<"$CONV")
ck "conversation carries resume token" "$CVT" "."
curl -s -X POST "$B/api/v1/member/reply" -H 'Content-Type: application/json' -d "{\"token\":\"$CVT\",\"message\":\"8.7 tonnes per hectare\"}" >/dev/null
SYNC=$(curl -s -m 30 -X POST "$B/api/v1/collections/$CID/sync" -H "Authorization: Bearer $OTOK" -d '{}' -H 'Content-Type: application/json')
if grep -q '"accepted"' <<<"$SYNC" && grep -q "8.7" <<<"$SYNC"; then PASS=$((PASS+1)); else FAIL=$((FAIL+1)); echo "FAIL: #7 sync did not ingest member answer"; echo "  got: $(head -c 400 <<<"$SYNC")"; fi

# --- #9: completion + consumer delivery download ---
if grep -q '"completed"' <<<"$SYNC"; then PASS=$((PASS+1)); else FAIL=$((FAIL+1)); echo "FAIL: #9 collection not completed after accepted answer"; fi
DL=$(curl -s -o /dev/null -w "%{http_code}:%{size_download}" "$B/api/v1/consumer/collections/$CID/download" -H "Authorization: Bearer $CTOK")
ck "#9 consumer download" "$DL" "200:"

echo "PASS=$PASS FAIL=$FAIL"
[ "$FAIL" -eq 0 ]
