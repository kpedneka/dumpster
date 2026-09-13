#!/usr/bin/env bash
# Drives sustained concurrent search load against the KBs
# scripts/loadtest_setup.sh provisioned -- one background curl loop per
# (client, concurrency slot), all firing for the same duration. Built to
# genuinely trip api's CPU-based auto-scaling policy
# (terraform/ecs_api_autoscaling.tf: target-tracking on
# ECSServiceAverageCPUUtilization, 60% target, 2x 60s evaluation periods),
# not just to smoke-test the search path -- see the "re-examine the
# embedding sidecar's per-replica memory cost" dev board card this exists
# for.
#
# curl in a loop, not `hey`: hey has no way to target a specific IP/ALB
# directly the way curl's --connect-to does (no -k/insecure-TLS flag
# either), so it would need the custom domain's CNAME to already point at
# the current ALB -- a real, recurring staleness problem in this project
# every time an environment gets torn down and recreated. Plain curl loops
# avoid that dependency entirely, at the cost of some process-spawn
# overhead per request -- immaterial here since each request's own cost
# (an embedding computation, then a full LLM SSE stream) dwarfs curl's
# startup time.
#
# Usage:
#   BASE_URL=https://dumpster-staging.kpednekar.dev \
#   CONNECT_TO=dumpster-staging.kpednekar.dev:443:<current-alb-dns>:443 \
#     bash scripts/loadtest_run.sh loadtest_manifest.tsv [duration_seconds] [concurrency_per_client]
#
# duration_seconds defaults to 180 (3m -- comfortably past the scale-out
# alarm's 2x 60s evaluation window with margin). concurrency_per_client
# defaults to 10. Most of a single search request's wall-clock time is
# spent waiting on the Anthropic API's SSE stream, not doing local CPU
# work (only the initial query embedding is CPU-bound) -- so tripping a
# sustained 60% CPU target needs enough *concurrent in-flight* requests to
# overlap many embedding computations, not just a high total request
# count. If the first run doesn't trip the alarm, raise concurrency_per_client
# and rerun rather than guessing a number up front -- watch the real
# metric live in another terminal:
#   aws cloudwatch get-metric-statistics --namespace AWS/ECS --metric-name CPUUtilization \
#     --dimensions Name=ClusterName,Value=dumpster-staging Name=ServiceName,Value=api \
#     --start-time "$(date -u -v-10M +%Y-%m-%dT%H:%M:%S)" --end-time "$(date -u +%Y-%m-%dT%H:%M:%S)" \
#     --period 60 --statistics Average --region us-east-1
#
# Requirements: curl
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
CONNECT_TO="${CONNECT_TO:-}"
MANIFEST="${1:?usage: loadtest_run.sh <manifest.tsv> [duration_seconds] [concurrency_per_client]}"
DURATION_SECONDS="${2:-180}"
CONCURRENCY="${3:-10}"
QUERY="${QUERY:-What technique did Dr. Aldous Renwick pioneer?}"

CURL_OPTS=(-sk -o /dev/null)
[[ -n "$CONNECT_TO" ]] && CURL_OPTS+=(--connect-to "$CONNECT_TO")

info() { printf '[INFO]  %s\n' "$*" >&2; }

REQ_BODY=$(printf '{"query":"%s"}' "$QUERY")

COUNT_DIR=$(mktemp -d)
trap 'rm -rf "$COUNT_DIR"' EXIT

# Each worker writes its own request count to a dedicated file rather than
# returning it via `wait $pid` -- wait only surfaces a background job's
# exit status, not anything it wrote to stdout, so that would silently
# always sum to zero.
worker() {
  local cookie="$1" kb="$2" end_time="$3" count_file="$4"
  local count=0
  while [[ "$(date +%s)" -lt "$end_time" ]]; do
    curl "${CURL_OPTS[@]}" -H "Cookie: $cookie" -H "Content-Type: application/json" \
      -X POST "$BASE_URL/kbs/$kb/search" -d "$REQ_BODY" || true
    count=$((count + 1))
  done
  echo "$count" >"$count_file"
}

[[ -f "$MANIFEST" ]] || { info "manifest not found: $MANIFEST"; exit 1; }
NUM_CLIENTS=$(wc -l < "$MANIFEST" | tr -d ' ')
END_TIME=$(($(date +%s) + DURATION_SECONDS))
info "starting load: $NUM_CLIENTS clients x $CONCURRENCY concurrency = $((NUM_CLIENTS * CONCURRENCY)) concurrent workers, for ${DURATION_SECONDS}s"

PIDS=()
WORKER_ID=0
while IFS=$'\t' read -r cookie kb; do
  for _ in $(seq 1 "$CONCURRENCY"); do
    WORKER_ID=$((WORKER_ID + 1))
    worker "$cookie" "$kb" "$END_TIME" "$COUNT_DIR/$WORKER_ID" &
    PIDS+=("$!")
  done
done <"$MANIFEST"

info "${#PIDS[@]} workers launched, running for ${DURATION_SECONDS}s..."
wait "${PIDS[@]}"
TOTAL=0
for f in "$COUNT_DIR"/*; do
  TOTAL=$((TOTAL + $(cat "$f")))
done
info "load test complete -- $TOTAL total requests sent"
