#!/usr/bin/env bash
# End-to-end smoke test: anonymous session → upload (text + PDF) → indexed →
# search → account status.
#
# Usage:
#   BASE_URL=https://dumpster-staging.kpednekar.dev bash scripts/smoke_test.sh
#
# Optional:
#   CURL_RESOLVE=host:443:ip   pin the hostname to an IP (curl --resolve), for
#                              CI runs where DNS still caches the previous
#                              CloudFront distribution
#   TEXT_INDEX_TIMEOUT=180     seconds to wait for the text document
#   PDF_INDEX_TIMEOUT=600      seconds to wait for the PDF, which also runs
#                              region classification (an AWS Batch job)
#
# Requirements: curl, jq
# Exits non-zero on any failure, naming the step, HTTP status and response.

set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
TEXT_INDEX_TIMEOUT="${TEXT_INDEX_TIMEOUT:-180}"
PDF_INDEX_TIMEOUT="${PDF_INDEX_TIMEOUT:-600}"
PDF_FIXTURE="$(cd "$(dirname "$0")" && pwd)/testdata/smoke.pdf"
COOKIE_JAR=$(mktemp)
TMP_DOC=$(mktemp /tmp/smoke_doc_XXXX.txt)
RESP_BODY=$(mktemp)
trap 'rm -f "$COOKIE_JAR" "$TMP_DOC" "$RESP_BODY"' EXIT

CURL_OPTS=(-sS -b "$COOKIE_JAR" -c "$COOKIE_JAR")
[[ -n "${CURL_RESOLVE:-}" ]] && CURL_OPTS+=(--resolve "$CURL_RESOLVE")

info()  { printf '[INFO]  %s\n' "$*"; }
ok()    { printf '[ OK ]  %s\n' "$*"; }
fail()  { printf '[FAIL]  %s\n' "$*" >&2; exit 1; }

# request STEP METHOD PATH [curl args...]: sends the request, leaves the
# response body in $BODY, and fails with the step name, HTTP status and body
# on a transport error or a non-2xx response.
request() {
  local step=$1 method=$2 path=$3
  shift 3
  local code
  code=$(curl "${CURL_OPTS[@]}" -o "$RESP_BODY" -w '%{http_code}' -X "$method" "$BASE_URL$path" "$@") \
    || fail "$step: request to $path failed (curl exit $?)"
  BODY=$(cat "$RESP_BODY")
  [[ "$code" == 2* ]] || fail "$step: HTTP $code from $method $path: ${BODY:0:500}"
}

# wait_indexed LABEL DOC_ID TIMEOUT_SECONDS
wait_indexed() {
  local label=$1 doc_id=$2 timeout=$3 status="unknown" waited=0
  while (( waited < timeout )); do
    request "wait for $label" GET "/api/kbs/$KB_ID/documents/$doc_id"
    status=$(jq -r '.status' <<<"$BODY")
    [[ "$status" == "indexed" ]] && { ok "$label indexed (${waited}s)"; return; }
    [[ "$status" == "failed" ]] && fail "wait for $label: processing failed: ${BODY:0:500}"
    sleep 5
    waited=$((waited + 5))
  done
  fail "wait for $label: not indexed within ${timeout}s (last status: $status)"
}

# search LABEL QUERY: /search responds as Server-Sent Events, not one JSON
# body: retrieved_files/delta frames ending in exactly one done (success) or
# error (failure) frame -- see searchhandler.go's writeSSE. Only that final
# frame's data line carries the full summary.
search() {
  local label=$1 query=$2
  request "search ($label)" POST "/api/kbs/$KB_ID/search" \
    -H "Content-Type: application/json" -d "$(jq -n --arg q "$query" '{query: $q}')"
  local error_data done_data
  error_data=$( (grep -A1 '^event: error' <<<"$BODY" | grep '^data: ' | sed 's/^data: //') || true )
  [[ -z "$error_data" ]] || fail "search ($label): error event: $error_data"
  done_data=$( (grep -A1 '^event: done' <<<"$BODY" | grep '^data: ' | sed 's/^data: //') || true )
  SUMMARY=$(jq -r '.summary // empty' <<<"$done_data" 2>/dev/null || true)
  [[ -n "$SUMMARY" ]] || fail "search ($label): no summary in response: ${BODY:0:500}"
  ok "search ($label): ${SUMMARY:0:100}..."
}

# ── 1. Health check ───────────────────────────────────────────────────────────
info "Health check..."
request "health check" GET "/healthz"
[[ "$(jq -r '.status' <<<"$BODY")" == "ok" ]] || fail "health check: unexpected body: $BODY"
ok "healthz: ok"

# ── 2. Session cookie ─────────────────────────────────────────────────────────
info "Requesting session cookie..."
request "session" GET "/api/kbs"
SESSION_ID=$(grep -m1 'session_id' "$COOKIE_JAR" | awk '{print $NF}' || true)
[[ -n "$SESSION_ID" ]] || fail "session: no session_id cookie received"
ok "session_id: $SESSION_ID"

# ── 3. Create knowledge base ──────────────────────────────────────────────────
info "Creating knowledge base..."
request "create knowledge base" POST "/api/kbs" \
  -H "Content-Type: application/json" -d '{"name":"smoke-test-kb"}'
KB_ID=$(jq -r '.id' <<<"$BODY")
[[ "$KB_ID" != "null" && -n "$KB_ID" ]] || fail "create knowledge base: no id in response: $BODY"
ok "knowledge base: $KB_ID"

# ── 4. Upload a text document and a PDF ───────────────────────────────────────
# The PDF has a text layer, so it goes through region classification (layout
# analysis on AWS Batch) rather than plain document indexing.
info "Uploading documents..."
cat > "$TMP_DOC" <<'EOF'
The Eiffel Tower is a wrought-iron lattice tower on the Champ de Mars in Paris, France.
It was constructed from 1887 to 1889 as the centerpiece of the 1889 World's Fair.
The tower stands at 330 metres (1,083 ft) tall and was the tallest man-made structure
in the world for 41 years until the Chrysler Building was built in 1930.
EOF
request "upload text document" POST "/api/kbs/$KB_ID/documents" -F "file=@$TMP_DOC;type=text/plain"
TEXT_DOC_ID=$(jq -r '.id' <<<"$BODY")
[[ "$TEXT_DOC_ID" != "null" && -n "$TEXT_DOC_ID" ]] || fail "upload text document: no id in response: $BODY"
ok "text document: $TEXT_DOC_ID"

request "upload PDF" POST "/api/kbs/$KB_ID/documents" -F "file=@$PDF_FIXTURE;type=application/pdf"
PDF_DOC_ID=$(jq -r '.id' <<<"$BODY")
[[ "$PDF_DOC_ID" != "null" && -n "$PDF_DOC_ID" ]] || fail "upload PDF: no id in response: $BODY"
ok "PDF: $PDF_DOC_ID"

# ── 5. Wait for indexing ──────────────────────────────────────────────────────
# ~90s observed for the text document against real staging, even for a few
# sentences -- fixed pipeline latency, not size-driven. The PDF also waits on
# a Batch job, which can need a cold compute environment to scale up first.
info "Waiting for documents to be indexed..."
wait_indexed "text document" "$TEXT_DOC_ID" "$TEXT_INDEX_TIMEOUT"
wait_indexed "PDF" "$PDF_DOC_ID" "$PDF_INDEX_TIMEOUT"

# ── 6. Search ─────────────────────────────────────────────────────────────────
info "Searching..."
search "text document" "How tall is the Eiffel Tower?"
search "PDF" "When was the Statue of Liberty dedicated?"

# ── 7. Account status ─────────────────────────────────────────────────────────
info "Checking account status..."
request "account status" GET "/api/account/status"
ok "warning_active=$(jq -r '.warning_active' <<<"$BODY")  deletes_at=$(jq -r '.deletes_at' <<<"$BODY")"

echo ""
ok "All automated steps passed."

# ── Cleanup verification (manual) ─────────────────────────────────────────────
cat <<INSTRUCTIONS

Cleanup verification (manual): this session expires after 6h idle (24h at
most), and cmd/cleanup's daily run deletes it and its documents. After that:

  1. Session row gone from Postgres:
       psql \$DATABASE_URL -c "SELECT id FROM sessions WHERE id='$SESSION_ID'"
     (expect 0 rows)

  2. S3 objects gone:
       aws s3 ls s3://\$DOCUMENTS_BUCKET/documents/$SESSION_ID/
     (no objects)

INSTRUCTIONS
