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
  name              = "/ecs/${local.name_prefix}"
  retention_in_days = 14 # short retention -- personal-scale app, not a compliance-driven retention requirement
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
