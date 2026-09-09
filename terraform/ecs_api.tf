# api, worker, and cleanup task definitions + api/worker services.
# cleanup has no ECS service (it's not long-running) -- see
# ecs_cleanup.tf for its EventBridge Scheduler + RunTask setup instead.
#
# All three share the one dumpster-runtime image (ecs_ecr.tf), differing
# only by container command -- confirmed against the actual Dockerfile,
# not assumed. Env vars/secrets below are grounded in internal/config's
# real fields, not guessed: values still point at Neon/R2/Anthropic
# directly, matching today's actual app -- Aurora/S3/Bedrock migrations
# are separate, later cards that will update these in place.

locals {
  # Env vars/secrets every one of the three commands needs, regardless of
  # which binary runs -- object storage and AWS region are read by all
  # three (cleanup deletes real documents from R2 on account expiry).
  shared_environment = [
    { name = "AWS_REGION", value = var.aws_region },
    { name = "S3_ENDPOINT", value = var.r2_endpoint },
    { name = "S3_REGION", value = "auto" },
    { name = "S3_BUCKET", value = var.r2_bucket },
    { name = "S3_USE_PATH_STYLE", value = "true" },
  ]
  shared_secrets = [
    { name = "S3_ACCESS_KEY", valueFrom = aws_secretsmanager_secret.runtime["s3-access-key"].arn },
    { name = "S3_SECRET_KEY", valueFrom = aws_secretsmanager_secret.runtime["s3-secret-key"].arn },
  ]

  common_log_config = {
    logDriver = "awslogs"
    options = {
      "awslogs-region" = var.aws_region
      "awslogs-group"  = aws_cloudwatch_log_group.ecs.name
    }
  }
}

# --- api ---

resource "aws_ecs_task_definition" "api" {
  family                   = "${local.name_prefix}-api"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"

  # ARM64, matching the runtime image built on Apple Silicon: a real
  # crash caught only by an actual task run, not tofu plan/apply --
  # Fargate defaults to X86_64 when this block is omitted, and Docker on
  # an M-series Mac builds arm64 images by default (no --platform flag
  # needed to notice, or forget). The mismatch doesn't fail the deploy
  # at all; the container starts and immediately dies with "exec format
  # error" -- a kernel-level rejection of a binary built for the wrong
  # instruction set. Matching the architecture here, rather than relying
  # on every future build remembering `--platform linux/amd64`, is also
  # the cheaper Fargate option per vCPU (the same reason the NAT
  # instance is Graviton too).
  runtime_platform {
    cpu_architecture        = "ARM64"
    operating_system_family = "LINUX"
  }

  cpu                = var.api_cpu
  memory             = var.api_memory
  execution_role_arn = aws_iam_role.ecs_execution.arn
  task_role_arn      = aws_iam_role.ecs_task.arn

  container_definitions = jsonencode([
    {
      name      = "api"
      image     = "${aws_ecr_repository.runtime.repository_url}:${var.image_tag}"
      command   = ["/bin/api"]
      essential = true
      portMappings = [
        { containerPort = 8080, name = "api" }
      ]
      environment = concat(local.shared_environment, [
        { name = "HTTP_PORT", value = "8080" },
        { name = "ANTHROPIC_MODEL", value = var.anthropic_model },
        { name = "INFERENCE_SERVICE_URL", value = "http://inference:8000" },
        { name = "COOKIE_SECURE", value = "true" },
        { name = "MAX_DOCUMENTS_PER_SESSION", value = tostring(var.max_documents_per_session) },
        { name = "RATE_LIMIT_REQUESTS", value = tostring(var.rate_limit_requests) },
        { name = "MAX_COMMUNITY_GRAPH_ENTITIES", value = "5000" },
      ])
      secrets = concat(local.shared_secrets, [
        { name = "DATABASE_URL_POOLED", valueFrom = aws_secretsmanager_secret.runtime["database-url-pooled"].arn },
        { name = "ANTHROPIC_API_KEY", valueFrom = aws_secretsmanager_secret.runtime["anthropic-api-key"].arn },
      ])
      logConfiguration = merge(local.common_log_config, {
        options = merge(local.common_log_config.options, { "awslogs-stream-prefix" = "api" })
      })
    }
  ])
}

resource "aws_ecs_service" "api" {
  name            = "api"
  cluster         = aws_ecs_cluster.main.arn
  task_definition = aws_ecs_task_definition.api.arn
  desired_count   = 1
  launch_type     = "FARGATE"

  network_configuration {
    subnets         = [aws_subnet.private_1a.id, aws_subnet.private_1b.id]
    security_groups = [aws_security_group.ecs_tasks.id]
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.api.arn
    container_name   = "api"
    container_port   = 8080
  }

  service_connect_configuration {
    enabled   = true
    namespace = aws_service_discovery_http_namespace.internal.arn
  }

  depends_on = [aws_lb_listener.https]
}

variable "api_cpu" {
  type        = string
  description = "Fargate vCPU units (1024 = 1 vCPU). 512 = 0.5 vCPU, matching the cost estimate sized to fit a future embedding sidecar (not built in this card's scope yet)."
  default     = "512"
}

variable "api_memory" {
  type        = string
  description = "Fargate memory (MiB). 1024 matches the same cost estimate."
  default     = "1024"
}

variable "r2_endpoint" {
  type        = string
  description = "Cloudflare R2 S3-compatible endpoint URL for real document storage (still R2, not S3 yet -- see the 'Migrate object storage: R2 -> S3' card)."
}

variable "r2_bucket" {
  type        = string
  description = "R2 bucket name for real document storage. No default, on purpose -- staging and production must point at separate buckets (e.g. dumpster vs dumpster-staging) so ephemeral staging traffic never reads or writes real user documents; a default here risked silently reusing the production bucket for staging."
}

variable "anthropic_model" {
  type        = string
  description = "Claude model ID, still called directly against the Anthropic API (not Bedrock yet -- see the 'Migrate LLM provider' card)."
  default     = "claude-sonnet-4-6"
}

variable "max_documents_per_session" {
  type    = number
  default = 20
}

variable "rate_limit_requests" {
  type        = number
  default     = 100
  description = "Per-IP request limit. NOTE: internal/ratelimit/memory (the only implementation today) is in-process and will silently under-enforce across multiple api tasks -- see the 'Wire a real shared-store rate limiter' card, which must land before api's desired_count goes above 1."
}
