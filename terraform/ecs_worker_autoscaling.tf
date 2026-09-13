# Application Auto Scaling for the worker ECS service, driven by queue
# backlog depth -- not CPU/memory (see the "Design a queue-depth-based
# autoscaling signal for the worker" dev board card). The worker is
# I/O-bound (waiting on AWS Batch jobs and LLM calls), not CPU-bound, so
# CPU/memory target tracking (the api service's signal, ecs_api_autoscaling.tf)
# would be the wrong fit here: a worker sitting mostly idle waiting on a
# Batch job's completion can show near-zero CPU while genuinely backlogged.
#
# internal/queuemetrics publishes the real signal -- pending job count,
# queried from Postgres -- to CloudWatch every QueueMetricsPublishInterval
# (default 60s, see internal/config.Config's doc). Every worker replica
# publishes independently; CloudWatch tolerates the redundant data points
# trivially, and it means the signal doesn't depend on any one replica
# staying alive.
#
# Step scaling, not target tracking: target tracking wants a metric that
# naturally divides across capacity (ALB request-count-per-target already
# is one; CPU utilization effectively is too). Raw queue depth doesn't --
# adding a worker doesn't shrink "how many jobs are waiting" the way
# adding an api task shrinks "requests per target." Getting target tracking
# to work correctly here would mean publishing a *derived*
# jobs-per-running-task ratio, which needs the worker to also track its own
# service's live task count (via ecs:DescribeServices, a permission and a
# polling call this doesn't otherwise need) or enabling Container Insights
# for its RunningTaskCount metric (real cost and complexity for a personal-
# scale project). Step scaling acts on the raw backlog number directly --
# simpler, and a good match for this app's stated "accept simpler
# tradeoffs at personal scale" posture elsewhere in this Terraform.
#
# min=1/max=5: same "starting point sized for the 10-100 user design
# target, not a measured ceiling" caveat as the api service's autoscaling
# variables -- cheap to raise once real backlog data justifies it. min=1,
# not 0: per Dev Board #4's reconciliation, scaling to a true zero floor is
# possible now that the worker holds no warm state, but nothing in this
# card builds a scale-from-zero trigger (e.g. the API nudging a scale-up on
# enqueue), so a cold queue at min=0 would sit unprocessed until the next
# scheduled scaling evaluation with no reactive wake-up at all. Revisit
# once that trigger exists.

resource "aws_appautoscaling_target" "worker" {
  max_capacity       = var.worker_max_capacity
  min_capacity       = var.worker_min_capacity
  resource_id        = "service/${aws_ecs_cluster.main.name}/${aws_ecs_service.worker.name}"
  scalable_dimension = "ecs:service:DesiredCount"
  service_namespace  = "ecs"
}

# --- Scale out: sustained backlog above threshold ---
#
# 2 consecutive 60s periods above threshold before firing -- long enough to
# ignore a single-document upload's brief backlog blip, short enough that a
# genuine, sustained burst gets more capacity within ~2 minutes.
resource "aws_cloudwatch_metric_alarm" "worker_backlog_high" {
  alarm_name          = "${local.name_prefix}-worker-backlog-high"
  namespace           = "Dumpster/Worker"
  metric_name         = "PendingJobsTotal"
  statistic           = "Average"
  period              = 60
  evaluation_periods  = 2
  threshold           = var.worker_scale_out_backlog_threshold
  comparison_operator = "GreaterThanThreshold"
  # A gap in data (e.g. between a worker restart and its first publish)
  # should never itself trigger a scale-out -- absence of a signal is not
  # evidence of backlog.
  treat_missing_data = "notBreaching"
  alarm_actions      = [aws_appautoscaling_policy.worker_scale_out.arn]
}

resource "aws_appautoscaling_policy" "worker_scale_out" {
  name               = "${local.name_prefix}-worker-scale-out"
  policy_type        = "StepScaling"
  resource_id        = aws_appautoscaling_target.worker.resource_id
  scalable_dimension = aws_appautoscaling_target.worker.scalable_dimension
  service_namespace  = aws_appautoscaling_target.worker.service_namespace

  step_scaling_policy_configuration {
    adjustment_type         = "ChangeInCapacity"
    cooldown                = 120
    metric_aggregation_type = "Average"

    step_adjustment {
      metric_interval_lower_bound = 0
      metric_interval_upper_bound = var.worker_scale_out_backlog_threshold * 2
      scaling_adjustment          = 1
    }
    step_adjustment {
      metric_interval_lower_bound = var.worker_scale_out_backlog_threshold * 2
      scaling_adjustment          = 2
    }
  }
}

# --- Scale in: sustained near-empty backlog ---
#
# 5 consecutive 60s periods (5 minutes) below threshold before firing --
# deliberately much longer than the scale-out alarm's 2, so a worker doesn't
# get removed moments before the next document upload arrives. A longer
# cooldown here too (300s vs. scale-out's 120s), for the same
# flap-avoidance reason already used on the api service's target-tracking
# policy.
resource "aws_cloudwatch_metric_alarm" "worker_backlog_low" {
  alarm_name          = "${local.name_prefix}-worker-backlog-low"
  namespace           = "Dumpster/Worker"
  metric_name         = "PendingJobsTotal"
  statistic           = "Average"
  period              = 60
  evaluation_periods  = 5
  threshold           = var.worker_scale_in_backlog_threshold
  comparison_operator = "LessThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [aws_appautoscaling_policy.worker_scale_in.arn]
}

resource "aws_appautoscaling_policy" "worker_scale_in" {
  name               = "${local.name_prefix}-worker-scale-in"
  policy_type        = "StepScaling"
  resource_id        = aws_appautoscaling_target.worker.resource_id
  scalable_dimension = aws_appautoscaling_target.worker.scalable_dimension
  service_namespace  = aws_appautoscaling_target.worker.service_namespace

  step_scaling_policy_configuration {
    adjustment_type         = "ChangeInCapacity"
    cooldown                = 300
    metric_aggregation_type = "Average"

    step_adjustment {
      metric_interval_upper_bound = 0
      scaling_adjustment          = -1
    }
  }
}

# --- IAM: let the worker publish its own scaling signal ---
#
# Scoped to the one namespace internal/queuemetrics/cloudwatch writes to,
# via a condition key -- CloudWatch has no resource-level ARNs for
# PutMetricData to scope against directly, but it does support conditioning
# on the namespace, which is the tightest grant actually available.
data "aws_iam_policy_document" "ecs_task_cloudwatch_metrics" {
  statement {
    actions   = ["cloudwatch:PutMetricData"]
    resources = ["*"]
    condition {
      test     = "StringEquals"
      variable = "cloudwatch:namespace"
      values   = ["Dumpster/Worker"]
    }
  }
}

resource "aws_iam_role_policy" "ecs_task_cloudwatch_metrics" {
  name   = "${local.name_prefix}-ecs-task-cloudwatch-metrics"
  role   = aws_iam_role.ecs_task.id
  policy = data.aws_iam_policy_document.ecs_task_cloudwatch_metrics.json
}

variable "worker_min_capacity" {
  type        = number
  default     = 1
  description = "Minimum number of worker tasks Application Auto Scaling will maintain. See this file's header comment for why this isn't 0."
}

variable "worker_max_capacity" {
  type        = number
  default     = 5
  description = "Maximum number of worker tasks Application Auto Scaling can scale out to. Starting point, not a measured ceiling -- see this file's header comment."
}

variable "worker_scale_out_backlog_threshold" {
  type        = number
  default     = 20
  description = "Total pending job count above which the scale-out alarm fires. Starting point, not a measured optimum -- tune once real staging/production backlog patterns are observed."
}

variable "worker_scale_in_backlog_threshold" {
  type        = number
  default     = 2
  description = "Total pending job count below which the scale-in alarm fires. Deliberately close to zero, not just \"lower than the scale-out threshold\": scaling in should only happen once the backlog is genuinely drained, not merely reduced."
}
