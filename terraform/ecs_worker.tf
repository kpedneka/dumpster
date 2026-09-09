# worker task definition + service. No load balancer, no public
# exposure -- it only ever polls Postgres and submits AWS Batch jobs.

resource "aws_ecs_task_definition" "worker" {
  family                   = "${local.name_prefix}-worker"
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

  cpu                = var.worker_cpu
  memory             = var.worker_memory
  execution_role_arn = aws_iam_role.ecs_execution.arn
  task_role_arn      = aws_iam_role.ecs_task.arn

  container_definitions = jsonencode([
    {
      name      = "worker"
      image     = "${aws_ecr_repository.runtime.repository_url}:${var.image_tag}"
      command   = ["/bin/worker"]
      essential = true
      environment = concat(local.shared_environment, [
        { name = "ANTHROPIC_MODEL", value = var.anthropic_model },
        { name = "INFERENCE_SERVICE_URL", value = "http://inference:8000" },
        { name = "BATCH_JOB_QUEUE", value = var.entity_extraction_job_queue },
        { name = "BATCH_JOB_DEFINITION", value = var.entity_extraction_job_definition },
        { name = "EMBED_BATCH_JOB_QUEUE", value = var.embedding_job_queue },
        { name = "EMBED_BATCH_JOB_DEFINITION", value = var.embedding_job_definition },
        { name = "WORKER_CONCURRENCY", value = "5" },
        { name = "ENTITY_EXTRACTION_BATCH_SIZE", value = "50" },
        { name = "R2_SCRATCH_ENDPOINT", value = var.r2_scratch_endpoint },
        { name = "R2_SCRATCH_REGION", value = "auto" },
        { name = "R2_SCRATCH_BUCKET", value = var.r2_scratch_bucket },
        { name = "R2_SCRATCH_USE_PATH_STYLE", value = "false" },
      ])
      secrets = concat(local.shared_secrets, [
        { name = "DATABASE_URL", valueFrom = aws_secretsmanager_secret.runtime["database-url"].arn },
        { name = "ANTHROPIC_API_KEY", valueFrom = aws_secretsmanager_secret.runtime["anthropic-api-key"].arn },
        { name = "R2_SCRATCH_ACCESS_KEY", valueFrom = aws_secretsmanager_secret.runtime["r2-scratch-access-key"].arn },
        { name = "R2_SCRATCH_SECRET_KEY", valueFrom = aws_secretsmanager_secret.runtime["r2-scratch-secret-key"].arn },
      ])
      logConfiguration = merge(local.common_log_config, {
        options = merge(local.common_log_config.options, { "awslogs-stream-prefix" = "worker" })
      })
    }
  ])
}

resource "aws_ecs_service" "worker" {
  name            = "worker"
  cluster         = aws_ecs_cluster.main.arn
  task_definition = aws_ecs_task_definition.worker.arn
  desired_count   = 1
  launch_type     = "FARGATE"

  network_configuration {
    subnets         = [aws_subnet.private_1a.id, aws_subnet.private_1b.id]
    security_groups = [aws_security_group.ecs_tasks.id]
  }

  service_connect_configuration {
    enabled   = true
    namespace = aws_service_discovery_http_namespace.internal.arn
  }
}

variable "worker_cpu" {
  type        = string
  description = "Fargate vCPU units (1024 = 1 vCPU). 256 = 0.25 vCPU, matching the cost estimate -- the worker is a thin orchestrator today (poll, submit Batch jobs, wait), not a model host."
  default     = "256"
}

variable "worker_memory" {
  type        = string
  description = "Fargate memory (MiB). 512 matches the same cost estimate."
  default     = "512"
}

# The four Batch variables below default to the real, existing production
# queues/definitions regardless of var.environment -- deliberately not
# environment-parameterized. Standing up a second, GPU-backed Batch
# compute environment just for ephemeral staging runs would cost real
# money for infrastructure a short-lived smoke test doesn't need; staging
# is expected to share production's Batch queues unless a future card
# decides otherwise. This is a real, accepted tradeoff, not an oversight:
# it means a staging worker's entity-extraction/embedding jobs land in
# the same queue as production's, so avoid running staging under
# sustained load that would compete with real traffic for GPU capacity.

variable "entity_extraction_job_queue" {
  type        = string
  description = "AWS Batch job queue for entity extraction. Real value confirmed via `aws batch describe-job-queues`. Shared across environments -- see the note above."
  default     = "dumpster-batch-gpu-pilot-queue"
}

variable "entity_extraction_job_definition" {
  type        = string
  description = "AWS Batch job definition for entity extraction. Real value confirmed via `aws batch describe-job-definitions` -- the production one, not the -dev variant. Shared across environments -- see the note above."
  default     = "dumpster-entity-extraction"
}

variable "embedding_job_queue" {
  type        = string
  description = "AWS Batch job queue for ingestion-time embedding. Real value confirmed via `aws batch describe-job-queues`. Shared across environments -- see the note above."
  default     = "dumpster-embedding-queue"
}

variable "embedding_job_definition" {
  type        = string
  description = "AWS Batch job definition for ingestion-time embedding. Real value confirmed via `aws batch describe-job-definitions`. Shared across environments -- see the note above."
  default     = "dumpster-embedding-jobdef"
}

variable "r2_scratch_endpoint" {
  type        = string
  description = "R2 endpoint for the Batch jobs' transient scratch bucket -- a different bucket from real document storage (var.r2_endpoint/r2_bucket in ecs_api.tf), no local-dev default since a real Fargate job needs a real internet-reachable bucket regardless of environment."
}

variable "r2_scratch_bucket" {
  type        = string
  description = "R2 scratch bucket name for AWS Batch job input/output handoff."
}
