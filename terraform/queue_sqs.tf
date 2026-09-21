# One SQS queue (+ dead-letter queue) per queue.JobType — the publish-side
# infrastructure for the event-driven job orchestration replacing the ECS
# worker's Postgres-poll loop. See the "Event-Driven Job Orchestration:
# SQS + Lambda + Step Functions" System Architecture page and its dev
# board for the full design and the incident that motivated it.
#
# Provisioning only, at this stage: nothing publishes to or consumes from
# these queues yet (internal/queue/sqs.Store exists and is unit-tested,
# but isn't wired into cmd/api/cmd/worker) and no Lambda/Step Functions
# consumer exists yet either — that's workstream B/C on the dev board.
# Additive and inert until then, same as this account's IAM deploy role
# already grants sqs:* against the dumpster-* ARN pattern these match
# (terraform/bootstrap/github_oidc.tf's existing SQS statement, added
# for ecs_cleanup.tf's cleanup_dlq), so no IAM change is needed here.
#
# visibility_timeout_seconds: 60s across every queue, deliberately uniform
# and short. In the target design, the Lambda triggered by each queue's
# event source mapping either does the whole job itself (edge extraction,
# canonicalization -- pure local compute) or, for the three Batch-backed
# job types, just calls Step Functions StartExecution and returns --
# Step Functions' own batch:submitJob.sync task owns the actual multi-
# minute wait on AWS Batch from there, decoupled from this SQS message's
# lifecycle entirely. No queue here needs a visibility timeout long enough
# to cover a Batch job's runtime, only long enough for a Lambda cold start
# plus a quick handoff.
#
# redrive_policy maxReceiveCount: 1 for entity_extraction and
# region_classification, matching queue.SingleShotMaxAttempts (internal/
# queue/queue.go) -- both are thin wrappers around one AWS Batch
# submission today, and that reasoning (a failure is deterministic, not
# transient, so retrying just wastes compute) carries over unchanged.
# Everything else gets 3, matching the jobs table's current max_attempts
# default.
locals {
  queue_job_types = {
    document_indexing     = { max_receive_count = 3 }
    entity_extraction     = { max_receive_count = 1 }
    edge_extraction       = { max_receive_count = 3 }
    region_classification = { max_receive_count = 1 }
    canonicalization      = { max_receive_count = 3 }
  }
}

resource "aws_sqs_queue" "job_dlq" {
  for_each = local.queue_job_types
  name     = "${local.name_prefix}-${replace(each.key, "_", "-")}-dlq"
}

resource "aws_sqs_queue" "job" {
  for_each                   = local.queue_job_types
  name                       = "${local.name_prefix}-${replace(each.key, "_", "-")}"
  visibility_timeout_seconds = 60

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.job_dlq[each.key].arn
    maxReceiveCount     = each.value.max_receive_count
  })
}

output "job_queue_urls" {
  value       = { for k, q in aws_sqs_queue.job : k => q.url }
  description = "SQS queue URL per queue.JobType -- feeds internal/queue/sqs.Config once wired into cmd/api/cmd/worker (workstream B/C)."
}
