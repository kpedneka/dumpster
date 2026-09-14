# AWS Batch's default log group for every job this account runs, on
# whichever compute environment -- entity extraction, embedding, region
# extraction (batch.tf). Shared across environments, not per-environment:
# its name has no environment segment, unlike every other resource in this
# config, because batch.tf defines no explicit logConfiguration on any job
# definition, so a staging job and a production job both land in this one
# group today.
#
# Managed only under the production workspace, never staging: two separate
# state files must not fight over the same physical AWS resource, and
# staging's routine destroy/recreate cycle (the whole point of the staging
# CI workflow) must never be the one that deletes it out from under
# whichever environment's jobs are also using it -- the same reasoning
# ecs_ecr.tf's header comment gives for why the ECR repositories are data
# sources, applied here to a resource this config does need to own (for its
# retention policy) rather than just reference.
#
# Adopted via import, not created fresh: this log group already existed,
# hand-created by AWS Batch itself on the account's first job run, with no
# retention policy at all -- confirmed directly via `aws logs
# describe-log-groups` (no retentionInDays field at all), 28.5MB and
# growing. 60 days, matching the same retention decision applied to the ECS
# app log group (ecs_cluster.tf) and the new otel-metrics log group
# (ecs_otel_collector.tf) -- see the "Push OTel metrics to CloudWatch" dev
# board card.
import {
  for_each = var.environment == "production" ? toset(["batch_job"]) : toset([])
  to       = aws_cloudwatch_log_group.batch_job[0]
  id       = "/aws/batch/job"
}

resource "aws_cloudwatch_log_group" "batch_job" {
  count             = var.environment == "production" ? 1 : 0
  name              = "/aws/batch/job"
  retention_in_days = 60
}
