# The ECS cluster api, worker, and cleanup all run in. Fargate standard,
# already decided (original AWS infra plan) over Express Mode -- this
# system's shape (a persistent worker plus a scheduled cron alongside the
# api) needs task-definition-level control Express Mode hides.

resource "aws_ecs_cluster" "main" {
  name = local.name_prefix

  setting {
    name  = "containerInsights"
    value = "disabled" # real per-hour cost for metrics this app's scale doesn't need yet; enable if debugging ever needs it
  }
}

resource "aws_cloudwatch_log_group" "ecs" {
  name = "/ecs/${local.name_prefix}"
  # 60 days, not 14 -- deliberately chosen to sit meaningfully above X-Ray's
  # fixed, non-configurable 30-day trace retention floor, so a log line can
  # still answer a question a month after its matching trace has expired.
  # See the "Push OTel metrics to CloudWatch" dev board card's retention
  # notes for the full reasoning (logs are the only one of the three
  # telemetry types -- metrics, logs, traces -- where AWS actually lets this
  # be tuned at all).
  retention_in_days = 60
}

output "ecs_cluster_arn" {
  value = aws_ecs_cluster.main.arn
}

output "ecs_cluster_name" {
  value = aws_ecs_cluster.main.name
}

output "ecs_log_group_name" {
  value = aws_cloudwatch_log_group.ecs.name
}
