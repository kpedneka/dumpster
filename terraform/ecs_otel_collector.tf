# The metrics sidecar's own CloudWatch Logs destination -- the awsemf
# exporter (otel-collector-config.yaml) writes to CloudWatch Logs first,
# which CloudWatch itself parses into CloudWatch Metrics via Embedded
# Metric Format; this log group is that intermediate destination. 60 days,
# matching the "how long is a metric actually useful, deliberately clearing
# X-Ray's fixed 30-day trace retention floor" decision on the "Push OTel
# metrics to CloudWatch" dev board card -- separate from, and deliberately
# longer-lived than, the shared app log group's 14-day retention (ecs_cluster.tf).
resource "aws_cloudwatch_log_group" "otel_metrics" {
  name              = "/dumpster/${var.environment}/otel-metrics"
  retention_in_days = 60
}

# awsemf needs CloudWatch *Logs* permissions, genuinely different from the
# existing cloudwatch:PutMetricData grant in ecs_worker_autoscaling.tf --
# internal/queuemetrics writes metric data points directly, while awsemf
# writes structured log lines that CloudWatch itself parses into metrics.
# Scoped to this one log group's ARN, not logs:* or an open group pattern.
data "aws_iam_policy_document" "ecs_task_otel_metrics_logs" {
  statement {
    actions = [
      "logs:CreateLogGroup",
      "logs:CreateLogStream",
      "logs:PutLogEvents",
      "logs:DescribeLogStreams",
      "logs:DescribeLogGroups",
      "logs:PutRetentionPolicy",
    ]
    resources = [
      aws_cloudwatch_log_group.otel_metrics.arn,
      "${aws_cloudwatch_log_group.otel_metrics.arn}:*",
    ]
  }
}

resource "aws_iam_role_policy" "ecs_task_otel_metrics_logs" {
  name   = "${local.name_prefix}-ecs-task-otel-metrics-logs"
  role   = aws_iam_role.ecs_task.id
  policy = data.aws_iam_policy_document.ecs_task_otel_metrics_logs.json
}
