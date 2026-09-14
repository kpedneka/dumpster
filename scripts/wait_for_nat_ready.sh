#!/usr/bin/env bash
# Waits for the NAT instance's outbound MASQUERADE rule to actually be
# configured (see ecs_networking.tf's aws_instance.nat user_data) before
# letting anything downstream rely on it. `tofu apply` only waits for the
# EC2 API to report the instance exists, not for cloud-init/user_data to
# finish installing and configuring iptables -- real wall-clock time after
# that. Without this wait, ECS tasks in the private subnets can start
# before the NAT path is live and fail with
# ResourceInitializationError: unable to pull secrets... connection issue
# between the task and AWS Secrets Manager.
#
# Usage (staging, automated -- see staging.yml):
#   NAT_INSTANCE_ID=$(tofu output -raw nat_instance_id) bash scripts/wait_for_nat_ready.sh
#
# Usage (production, manual -- production has no CI-based apply; its NAT
# instance is long-lived so this race is rare there -- only on initial
# creation or a NAT instance replacement -- but run this by hand right
# after `tofu apply -var-file=production.tfvars` when it applies):
#   cd terraform && NAT_INSTANCE_ID=$(tofu output -raw nat_instance_id) \
#     bash ../scripts/wait_for_nat_ready.sh
#
# Requires: AWS CLI v2, and credentials that can call ssm:SendCommand /
# ssm:GetCommandInvocation against this instance -- see
# terraform/bootstrap/github_oidc.tf's NATInstanceSSMSend/Read statements.

NAT_INSTANCE_ID="${NAT_INSTANCE_ID:?NAT_INSTANCE_ID must be set}"
AWS_REGION="${AWS_REGION:-us-east-1}"

# Tuned for a t4g.nano AL2023 instance's real observed timing: SSM agent
# registration typically completes within ~60-90s of boot; user_data's
# `dnf install iptables-nft iptables-services` + MASQUERADE config
# typically finishes within another 1-3 minutes. These bound the total
# wait generously above that, not padded once -- same "give real AWS
# propagation generous, not padded-once, time" pattern as this workflow's
# other retry loops (see staging.yml's ECS-stabilize and Smoke test steps).
MAX_ATTEMPTS="${MAX_ATTEMPTS:-20}"     # up to 20 dispatch attempts
DISPATCH_RETRY_SLEEP="${DISPATCH_RETRY_SLEEP:-15}" # seconds between dispatch attempts
POLL_ATTEMPTS="${POLL_ATTEMPTS:-6}"    # per dispatched command, how many times to poll for a terminal status
POLL_SLEEP="${POLL_SLEEP:-5}"          # seconds between polls
# Worst case: 20 * (15 + 6*5) = 900s = 15 minutes before giving up.

info()  { printf '[INFO]  %s\n' "$*"; }
ok()    { printf '[ OK ]  %s\n' "$*"; }
fail()  { printf '[FAIL]  %s\n' "$*" >&2; exit 1; }

# ── Poll until the NAT instance confirms it's actually routing ─────────────────
for attempt in $(seq 1 "$MAX_ATTEMPTS"); do
  info "attempt $attempt/$MAX_ATTEMPTS: dispatching readiness check to $NAT_INSTANCE_ID..."

  SEND_ERR=$(mktemp)
  COMMAND_ID=$(aws ssm send-command \
    --region "$AWS_REGION" \
    --instance-ids "$NAT_INSTANCE_ID" \
    --document-name "AWS-RunShellScript" \
    --parameters '{"commands":["iptables -t nat -L POSTROUTING | grep -q MASQUERADE"]}' \
    --query 'Command.CommandId' \
    --output text 2>"$SEND_ERR")
  SEND_EXIT=$?
  ERR_MSG=$(cat "$SEND_ERR")
  rm -f "$SEND_ERR"

  if [[ $SEND_EXIT -ne 0 || -z "$COMMAND_ID" || "$COMMAND_ID" == "None" ]]; then
    # Most likely cause: the SSM agent hasn't registered with the SSM
    # service yet -- a brand-new instance, not just a slow-to-finish
    # user_data script. send-command fails synchronously in that case
    # (InvalidInstanceId: "<id> is not in a valid state... Valid states
    # are: Online"), it doesn't dispatch and report failure later the way
    # a "check ran but wasn't ready" failure does below. If this message
    # instead mentions AccessDenied, stop and fix IAM (see this script's
    # header) -- it will never resolve itself by retrying.
    info "send-command failed, retrying: $ERR_MSG"
    sleep "$DISPATCH_RETRY_SLEEP"
    continue
  fi

  info "dispatched command $COMMAND_ID, polling for a terminal status..."

  for _ in $(seq 1 "$POLL_ATTEMPTS"); do
    sleep "$POLL_SLEEP"
    STATUS=$(aws ssm get-command-invocation \
      --region "$AWS_REGION" \
      --command-id "$COMMAND_ID" \
      --instance-id "$NAT_INSTANCE_ID" \
      --query 'Status' \
      --output text 2>/dev/null)
    case "$STATUS" in
      Success)
        ok "NAT instance is routing (MASQUERADE rule present)."
        exit 0
        ;;
      Pending|InProgress|Delayed|""|None)
        continue
        ;;
      *)
        # Failed / Cancelled / TimedOut / TargetNotConnected / etc -- the
        # check ran (or tried to) and didn't confirm readiness yet. Break
        # out and dispatch a fresh command on the next outer attempt --
        # AWS doesn't support re-running a command-id that already
        # reached a terminal state.
        info "command $COMMAND_ID finished with status=$STATUS (not ready yet)"
        break
        ;;
    esac
  done

  sleep "$DISPATCH_RETRY_SLEEP"
done

fail "NAT instance $NAT_INSTANCE_ID never confirmed a working MASQUERADE rule within the retry budget"
