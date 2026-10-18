# cleanup: no ECS service (it runs to completion and exits, not
# long-running) -- EventBridge Scheduler triggers an ECS RunTask every
# 15 minutes, same image as api/worker with the command overridden to
# /bin/cleanup. EventBridge Scheduler (not the older EventBridge Rules)
# for its retry policy and dead-letter queue.

resource "aws_ecs_task_definition" "cleanup" {
  family                   = "${local.name_prefix}-cleanup"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"

  # ARM64, matching the runtime image built on Apple Silicon -- see the
  # matching comment on aws_ecs_task_definition.api in ecs_api.tf for
  # why (a real crash -- "exec format error" -- caught only by an
  # actual task run, not tofu plan/apply).
  runtime_platform {
    cpu_architecture        = "ARM64"
    operating_system_family = "LINUX"
  }

  cpu                = "256"
  memory             = "512"
  execution_role_arn = aws_iam_role.ecs_execution.arn
  task_role_arn      = aws_iam_role.ecs_task.arn

  container_definitions = jsonencode([
    {
      name        = "cleanup"
      image       = "${data.aws_ecr_repository.runtime.repository_url}:${var.image_tag}"
      command     = ["/bin/cleanup"]
      essential   = true
      environment = concat(local.shared_environment, [])
      secrets = concat(local.shared_secrets, [
        { name = "DATABASE_URL", valueFrom = data.aws_secretsmanager_secret.runtime["database-url"].arn },
      ])
      logConfiguration = merge(local.common_log_config, {
        options = merge(local.common_log_config.options, { "awslogs-stream-prefix" = "cleanup" })
      })
    }
  ])
}

# --- EventBridge Scheduler: 15-minute trigger, ECS RunTask target ---

data "aws_iam_policy_document" "scheduler_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["scheduler.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "scheduler" {
  name               = "${local.name_prefix}-cleanup-scheduler"
  assume_role_policy = data.aws_iam_policy_document.scheduler_assume.json
}

data "aws_iam_policy_document" "scheduler_run_task" {
  statement {
    actions   = ["ecs:RunTask"]
    resources = [replace(aws_ecs_task_definition.cleanup.arn, "/:\\d+$/", ":*")] # allow any revision of this task family
  }
  statement {
    actions   = ["iam:PassRole"]
    resources = [aws_iam_role.ecs_execution.arn, aws_iam_role.ecs_task.arn]
  }
}

resource "aws_iam_role_policy" "scheduler_run_task" {
  name   = "${local.name_prefix}-cleanup-scheduler-run-task"
  role   = aws_iam_role.scheduler.id
  policy = data.aws_iam_policy_document.scheduler_run_task.json
}

resource "aws_sqs_queue" "cleanup_dlq" {
  name = "${local.name_prefix}-cleanup-dlq"
}

resource "aws_scheduler_schedule" "cleanup" {
  name       = "${local.name_prefix}-cleanup"
  group_name = "default"

  # The privacy page promises a session is deleted after 6 hours of
  # inactivity or 24 hours after creation. The API stops serving an
  # expired session immediately, but its rows and uploaded files only
  # leave storage when this sweep runs -- so the interval is the most a
  # session's data can outlive that promise. It was once daily, which let
  # expired data sit in storage for up to a day. 15 minutes also matches
  # account.WarningLeadTime, so no session can go from "not yet due a
  # warning" to "expired" between two runs.
  schedule_expression = "rate(15 minutes)"

  flexible_time_window {
    mode = "OFF"
  }

  target {
    arn      = aws_ecs_cluster.main.arn
    role_arn = aws_iam_role.scheduler.arn

    ecs_parameters {
      task_definition_arn = aws_ecs_task_definition.cleanup.arn
      launch_type         = "FARGATE"
      network_configuration {
        subnets         = [aws_subnet.private_1a.id, aws_subnet.private_1b.id]
        security_groups = [aws_security_group.ecs_tasks.id]
      }
    }

    retry_policy {
      maximum_retry_attempts = 3
      # No older than the interval: past that, the next scheduled run
      # covers the same sessions, so a late retry would only overlap it.
      maximum_event_age_in_seconds = 900
    }

    dead_letter_config {
      arn = aws_sqs_queue.cleanup_dlq.arn
    }
  }
}
