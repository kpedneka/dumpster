# One SQS queue (+ dead-letter queue) for each job type the stateless-jobs
# Lambda runs: edge extraction and canonicalization, the two stages that
# are pure Go computation with no AWS Batch hop (lambda_stateless_jobs.tf
# wires each queue to the Lambda through an event source mapping).
#
# The Batch-backed job types (document_indexing, entity_extraction,
# region_classification) have no queue. The publisher
# (internal/queue/dispatch) starts their Step Functions executions
# directly: a Standard execution is already a durable unit of work, and
# the Batch job queue already buffers compute, so a queue plus a starter
# Lambda in front would add nothing. Failed executions take the place of a
# dead-letter queue, with the failure recorded in Postgres
# (internal/jobstatus) for the UI's retry button. See the System
# Architecture page's "Decisions" section (2026-10-04).
#
# The deploy role's existing SQS statement covers these queues
# (terraform/bootstrap/github_oidc.tf, sqs:* actions on dumpster-*).
#
# visibility_timeout_seconds: 60s, matching the Lambda's own 60s timeout
# (lambda_stateless_jobs.tf). A message stays invisible for as long as one
# invocation can run, so it isn't redelivered while still being processed.
#
# redrive_policy maxReceiveCount: 3 for both, matching the jobs table's
# default max_attempts. Retries only help transient failures; the
# Lambda's failure handling decides which errors are worth retrying.
locals {
  queue_job_types = {
    edge_extraction  = { max_receive_count = 3 }
    canonicalization = { max_receive_count = 3 }
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
  description = "SQS queue URL per Lambda job type (edge_extraction, canonicalization) -- feeds internal/queue/sqs.Config."
}
