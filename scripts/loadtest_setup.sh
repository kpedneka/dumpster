#!/usr/bin/env bash
# Provisions N sessions, each with its own knowledge base and an indexed
# test document, for scripts/loadtest_run.sh to drive concurrent search
# load against. Kept separate from loadtest_run.sh so the same provisioned
# KBs can be reused across multiple load-test runs without re-uploading and
# re-indexing every time.
#
# Built for the "re-examine the embedding sidecar's per-replica memory cost
# once the API is genuinely scaled out" dev board card -- api's CPU-based
# auto-scaling policy (terraform/ecs_api_autoscaling.tf) only has something
# real to measure once actual concurrent search traffic drives it to scale
# out, which needs real indexed content to search against.
#
# Usage:
#   BASE_URL=https://dumpster-staging.kpednekar.dev \
#   CONNECT_TO=dumpster-staging.kpednekar.dev:443:<current-alb-dns>:443 \
#     bash scripts/loadtest_setup.sh [num_clients] > loadtest_manifest.tsv
#
# num_clients defaults to 5. CONNECT_TO is optional -- pass it to reach a
# specific ALB directly (its DNS name is stable across an environment's
# lifetime and printed by `tofu output alb_dns_name`) without depending on
# the custom domain's CNAME being current, the same --connect-to pattern
# used throughout this project's manual staging verification. Each line of
# the manifest written to stdout is:
#   <Cookie header value>\t<kb_id>
#
# Requirements: curl, jq
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
CONNECT_TO="${CONNECT_TO:-}"
NUM_CLIENTS="${1:-5}"

CURL_OPTS=(-sk)
[[ -n "$CONNECT_TO" ]] && CURL_OPTS+=(--connect-to "$CONNECT_TO")

info() { printf '[INFO]  %s\n' "$*" >&2; }
fail() { printf '[FAIL]  %s\n' "$*" >&2; exit 1; }

TMP_DOC=$(mktemp /tmp/loadtest_doc_XXXX.txt)
trap 'rm -f "$TMP_DOC"' EXIT

# Fictional content, same reasoning as every other verification document
# used this session: a correct search answer proves genuine retrieval
# against this specific upload, not the model's own background knowledge.
cat > "$TMP_DOC" <<'EOF'
The Quennelfrost Archive is a fictional climate research station established
in 1974 on the invented island of Skalmyre, built specifically to study
long-period ocean current oscillations. Its founding director, Dr. Aldous
Renwick, pioneered a now-standard buoy-tethering technique still referenced
in the station's training materials.
EOF

for i in $(seq 1 "$NUM_CLIENTS"); do
  info "provisioning client $i/$NUM_CLIENTS"
  COOKIE_JAR=$(mktemp)

  curl "${CURL_OPTS[@]}" -c "$COOKIE_JAR" "$BASE_URL/kbs" > /dev/null
  SESSION_ID=$(grep -m1 'session_id' "$COOKIE_JAR" | awk '{print $NF}')
  [[ -n "$SESSION_ID" ]] || fail "client $i: no session_id cookie received"

  KB=$(curl "${CURL_OPTS[@]}" -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
    -X POST "$BASE_URL/kbs" -H "Content-Type: application/json" \
    -d "{\"name\":\"loadtest-client-$i\"}")
  KB_ID=$(echo "$KB" | jq -r '.id')
  [[ "$KB_ID" != "null" && -n "$KB_ID" ]] || fail "client $i: KB creation failed: $KB"

  DOC=$(curl "${CURL_OPTS[@]}" -b "$COOKIE_JAR" -c "$COOKIE_JAR" \
    -X POST "$BASE_URL/kbs/$KB_ID/documents" -F "file=@$TMP_DOC;type=text/plain")
  DOC_ID=$(echo "$DOC" | jq -r '.id')
  [[ "$DOC_ID" != "null" && -n "$DOC_ID" ]] || fail "client $i: upload failed: $DOC"

  # 3s x 60 = 180s, not the 60s this started with: a real run measured a
  # genuinely successful index taking ~69s once the worker had any real
  # backlog to work through (retries from an earlier failed attempt, or
  # just N clients provisioning concurrently) -- 60s was tuned against an
  # idle worker with no contention, not a realistic one.
  INDEXED=false
  STATUS="unknown"
  for _ in $(seq 1 60); do
    STATUS=$(curl "${CURL_OPTS[@]}" -b "$COOKIE_JAR" \
      "$BASE_URL/kbs/$KB_ID/documents/$DOC_ID" | jq -r '.status')
    if [[ "$STATUS" == "indexed" ]]; then
      INDEXED=true
      break
    fi
    [[ "$STATUS" == "failed" ]] && fail "client $i: document processing failed"
    sleep 3
  done
  $INDEXED || fail "client $i: document not indexed within 180s (last status: $STATUS)"

  printf 'session_id=%s\t%s\n' "$SESSION_ID" "$KB_ID"
  rm -f "$COOKIE_JAR"
  info "client $i ready: kb=$KB_ID"
done

info "done -- $NUM_CLIENTS clients provisioned"
