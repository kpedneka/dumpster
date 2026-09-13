# Application Auto Scaling for the api ECS service. Depends on the shared
# rate limiter (internal/ratelimit/pgstore, see the "Wire a real shared-store
# rate limiter" dev board card) already having landed -- without it, N api
# replicas would each enforce the per-IP request limit independently,
# silently making the effective limit N times more permissive the moment
# this policy scales past one task.
#
# Metric: CPU utilization, not ALB request-count-per-target. Considered and
# rejected: RequestCountPerTarget is a completed-requests-per-minute metric,
# a poor fit for this API's actual traffic shape, which is a mix of
# near-instant polling calls (the upload-progress checklist polls every 2s
# per open document) and long-lived SSE search streams
# (internal/server/searchhandler.go) that can hold a target's connection
# open for the full duration of an LLM generation. A long stream doesn't
# register as load on this metric until it completes, so it would
# systematically under-report exactly the traffic that matters most for
# perceived responsiveness. CPU utilization has no such lag, and -- unlike a
# typical thin API proxy -- api's task now includes the embed-sidecar
# container (query-time embedding inference, a real per-replica CPU cost),
# so CPU utilization is a genuine signal of the task's actual work, not an
# anemic proxy for it.
#
# min=1/max=3: a starting point sized for this app's stated 10-100 user
# design target, not a measured ceiling -- cheap to raise once real
# staging/production traffic shows sustained CPU pressure at 3 tasks.

resource "aws_appautoscaling_target" "api" {
  max_capacity       = var.api_max_capacity
  min_capacity       = var.api_min_capacity
  resource_id        = "service/${aws_ecs_cluster.main.name}/${aws_ecs_service.api.name}"
  scalable_dimension = "ecs:service:DesiredCount"
  service_namespace  = "ecs"
}

resource "aws_appautoscaling_policy" "api_cpu" {
  name               = "${local.name_prefix}-api-cpu-target-tracking"
  policy_type        = "TargetTrackingScaling"
  resource_id        = aws_appautoscaling_target.api.resource_id
  scalable_dimension = aws_appautoscaling_target.api.scalable_dimension
  service_namespace  = aws_appautoscaling_target.api.service_namespace

  target_tracking_scaling_policy_configuration {
    predefined_metric_specification {
      predefined_metric_type = "ECSServiceAverageCPUUtilization"
    }
    target_value = var.api_cpu_target_percent
    # Scale out fast (a slow-to-scale API is a user-facing latency problem),
    # scale in slow (avoid flapping capacity up and down across a brief,
    # noisy CPU spike -- e.g. one long report-style search query).
    scale_out_cooldown = 60
    scale_in_cooldown  = 300
  }
}

# Fargate Spot: evaluated and rejected for api specifically, unlike the
# worker (see the "Evaluate Fargate Spot for worker tasks" dev board card,
# where it's a real, likely win). api serves live, user-facing HTTP
# requests behind an ALB, including long-lived SSE search streams -- a Spot
# reclaim (a 2-minute warning, then termination) would visibly cut off a
# response mid-stream for whichever user happened to be talking to that
# task, with no retry path the way a queue-based worker job has. At this
# app's min_capacity of 1-3 tasks, losing even one to a reclaim is a large
# fraction of total capacity, not a statistical rounding error the way it
# would be in a much larger fleet. Standard (on-demand) Fargate only.

variable "api_min_capacity" {
  type        = number
  default     = 1
  description = "Minimum number of api tasks Application Auto Scaling will maintain."
}

variable "api_max_capacity" {
  type        = number
  default     = 3
  description = "Maximum number of api tasks Application Auto Scaling can scale out to. Starting point sized for this app's stated 10-100 user design target, not a measured ceiling -- see this file's header comment."
}

variable "api_cpu_target_percent" {
  type        = number
  default     = 60
  description = "Target average CPU utilization (%) the api service's target-tracking policy scales toward. Includes the embed-sidecar container's CPU usage, not just the api container's -- ECSServiceAverageCPUUtilization is measured at the task level, across every container in the task definition."
}
