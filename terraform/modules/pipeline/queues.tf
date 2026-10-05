# One SQS queue (+ dead-letter queue) for each job type the stateless-jobs
# Lambda runs: edge extraction and canonicalization, the two stages that
# are pure Go computation with no AWS Batch hop.
#
# The Batch-backed job types have no queue: the publisher
# (internal/queue/dispatch) starts their state machines directly. Failed
# executions take the place of a dead-letter queue, with the failure
# recorded in Postgres (internal/jobstatus) for the UI's retry button.
#
# visibility_timeout_seconds matches the stateless Lambda's own timeout, so
# a message isn't redelivered while it's still being processed.
#
# max_receive_count must match queueMaxReceiveCount in
# cmd/lambda-stateless-jobs/main.go: the Lambda's dispatcher uses it to
# know which delivery is the last before the DLQ, and records the failure
# then instead of retrying.
locals {
  queue_job_types = {
    edge_extraction  = { max_receive_count = 3 }
    canonicalization = { max_receive_count = 3 }
  }
}

resource "aws_sqs_queue" "job_dlq" {
  for_each = local.queue_job_types
  name     = "${var.name_prefix}-${replace(each.key, "_", "-")}-dlq"
}

resource "aws_sqs_queue" "job" {
  for_each                   = local.queue_job_types
  name                       = "${var.name_prefix}-${replace(each.key, "_", "-")}"
  visibility_timeout_seconds = local.stateless_timeout_seconds

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.job_dlq[each.key].arn
    maxReceiveCount     = each.value.max_receive_count
  })
}
