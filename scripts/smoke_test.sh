#!/usr/bin/env bash
# End-to-end smoke test: anonymous session → upload → search → verify cleanup.
#
# Usage:
#   BASE_URL=https://dumpster-staging.kpednekar.dev bash scripts/smoke_test.sh
#
# Requirements: curl, jq
# The script exits non-zero on any failure.

set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
COOKIE_JAR=$(mktemp)
TMP_DOC=$(mktemp /tmp/smoke_doc_XXXX.txt)
trap 'rm -f "$COOKIE_JAR" "$TMP_DOC"' EXIT

info()  { printf '[INFO]  %s\n' "$*"; }
ok()    { printf '[ OK ]  %s\n' "$*"; }
fail()  { printf '[FAIL]  %s\n' "$*" >&2; exit 1; }

# ── 1. Health check ───────────────────────────────────────────────────────────
info "Health check..."
status=$(curl -sf "$BASE_URL/healthz" | jq -r '.status')
[[ "$status" == "ok" ]] || fail "healthz returned: $status"
ok "healthz: $status"

# ── 2. Session cookie ─────────────────────────────────────────────────────────
info "Requesting session cookie..."
curl -sf -c "$COOKIE_JAR" "$BASE_URL/api/kbs" > /dev/null
SESSION_ID=$(grep -m1 'session_id' "$COOKIE_JAR" | awk '{print $NF}' || true)
[[ -n "$SESSION_ID" ]] || fail "no session_id cookie received"
ok "session_id: $SESSION_ID"

# ── 3. Create knowledge base ──────────────────────────────────────────────────
info "Creating knowledge base..."
KB=$(curl -sf -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
  -X POST "$BASE_URL/api/kbs" \
  -H "Content-Type: application/json" \
  -d '{"name":"smoke-test-kb"}')
KB_ID=$(echo "$KB" | jq -r '.id')
[[ "$KB_ID" != "null" && -n "$KB_ID" ]] || fail "KB creation failed: $KB"
ok "knowledge base: $KB_ID"

# ── 4. Upload a document ──────────────────────────────────────────────────────
info "Uploading document..."
cat > "$TMP_DOC" <<'EOF'
The Eiffel Tower is a wrought-iron lattice tower on the Champ de Mars in Paris, France.
It was constructed from 1887 to 1889 as the centerpiece of the 1889 World's Fair.
The tower stands at 330 metres (1,083 ft) tall and was the tallest man-made structure
in the world for 41 years until the Chrysler Building was built in 1930.
EOF

DOC=$(curl -sf -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
  -X POST "$BASE_URL/api/kbs/$KB_ID/documents" \
  -F "file=@$TMP_DOC;type=text/plain")
DOC_ID=$(echo "$DOC" | jq -r '.id')
[[ "$DOC_ID" != "null" && -n "$DOC_ID" ]] || fail "document upload failed: $DOC"
ok "document: $DOC_ID"

# ── 5. Wait for indexing ──────────────────────────────────────────────────────
# ~90s observed against real staging (AWS), even for a one-sentence document
# -- fixed pipeline latency, not doc-size-driven. 60s (this loop's original
# local-dev-era budget) reliably timed out before indexing ever finished.
info "Waiting for document to be indexed (up to 3 minutes)..."
INDEXED=false
DOC_STATUS="unknown"
for _ in $(seq 1 36); do
  DOC_STATUS=$(curl -sf -b "$COOKIE_JAR" \
    "$BASE_URL/api/kbs/$KB_ID/documents/$DOC_ID" | jq -r '.status')
  if [[ "$DOC_STATUS" == "indexed" ]]; then
    INDEXED=true
    break
  fi
  [[ "$DOC_STATUS" == "failed" ]] && fail "document processing failed"
  sleep 5
done
$INDEXED || fail "document not indexed within 3 minutes (last status: $DOC_STATUS)"
ok "document indexed"

# ── 6. Search ─────────────────────────────────────────────────────────────────
# /search responds as Server-Sent Events, not one JSON body: a stream of
# retrieved_files/delta frames ending in exactly one done (success) or error
# (failure) frame -- see searchhandler.go's writeSSE. Only that final
# frame's data line carries the full summary.
info "Searching..."
STREAM=$(curl -sf -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
  -X POST "$BASE_URL/api/kbs/$KB_ID/search" \
  -H "Content-Type: application/json" \
  -d '{"query":"How tall is the Eiffel Tower?"}')
ERROR_DATA=$( (echo "$STREAM" | grep -A1 '^event: error' | grep '^data: ' | sed 's/^data: //') || true )
[[ -z "$ERROR_DATA" ]] || fail "search returned an error event: $ERROR_DATA"
DONE_DATA=$( (echo "$STREAM" | grep -A1 '^event: done' | grep '^data: ' | sed 's/^data: //') || true )
SUMMARY=$(echo "$DONE_DATA" | jq -r '.summary // empty')
[[ -n "$SUMMARY" ]] || fail "search returned no summary: $STREAM"
ok "search summary: ${SUMMARY:0:100}..."

# ── 7. Account status ─────────────────────────────────────────────────────────
info "Checking account status..."
ACCT=$(curl -sf -b "$COOKIE_JAR" "$BASE_URL/api/account/status")
WARNING=$(echo "$ACCT" | jq -r '.warning_active')
DELETES_AT=$(echo "$ACCT" | jq -r '.deletes_at')
ok "warning_active=$WARNING  deletes_at=$DELETES_AT"

echo ""
ok "All automated steps passed."

# ── Cleanup verification (manual / staging-only) ──────────────────────────────
cat <<INSTRUCTIONS

Cleanup verification (requires accelerated timeouts or waiting for idle expiry):
  Run in staging with SWEEP_INTERVAL=1m and a reduced IdleTimeout to confirm
  the sweep fires, then check:

  1. Worker logs show:
       level=INFO msg="sweep complete" warned=0 deleted=1 errors=0

  2. Session row gone from Postgres:
       psql \$DATABASE_URL -c "SELECT id FROM sessions WHERE id='$SESSION_ID'"
     (expect 0 rows)

  3. S3 objects gone — check the AWS console or list with:
       aws s3 ls s3://\$DOCUMENTS_BUCKET/documents/$SESSION_ID/
     (no objects matching the deleted session's document keys)

INSTRUCTIONS
