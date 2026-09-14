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
      image     = "${data.aws_ecr_repository.runtime.repository_url}:${var.image_tag}"
      command   = ["/bin/worker"]
      essential = true
      environment = concat(local.shared_environment, [
        { name = "ANTHROPIC_MODEL", value = var.anthropic_model },
        { name = "BATCH_JOB_QUEUE", value = aws_batch_job_queue.entity_extraction.name },
        { name = "BATCH_JOB_DEFINITION", value = aws_batch_job_definition.entity_extraction.name },
        { name = "EMBED_BATCH_JOB_QUEUE", value = aws_batch_job_queue.embedding.name },
        { name = "EMBED_BATCH_JOB_DEFINITION", value = aws_batch_job_definition.embedding.name },
        # PDF/image region classification runs as its own AWS Batch job --
        # see internal/manifest/awsbatch's package doc. Required, not
        # opt-in: cmd/worker exits at startup if either is empty. No
        # fallback to the old standalone inference service's synchronous
        # /regions call remains -- that's fully retired now that Fly
        # deploys have stopped entirely (see the "Fold region
        # classification into the AWS Batch embed job" dev board card).
        # Shares the embedding queue, not a dedicated one -- see batch.tf.
        { name = "REGIONS_BATCH_JOB_QUEUE", value = aws_batch_job_queue.embedding.name },
        { name = "REGIONS_BATCH_JOB_DEFINITION", value = aws_batch_job_definition.region_extraction.name },
        { name = "WORKER_CONCURRENCY", value = "5" },
        { name = "ENTITY_EXTRACTION_BATCH_SIZE", value = "50" },
        # No S3_SCRATCH_ENDPOINT and no access-key secrets, same reasoning
        # as the documents bucket in ecs_api.tf's shared_environment: real
        # AWS S3 needs no endpoint override, and credentials come from the
        # ECS task role (granted access in s3_storage.tf), not a static key.
        { name = "S3_SCRATCH_REGION", value = var.aws_region },
        { name = "S3_SCRATCH_BUCKET", value = aws_s3_bucket.scratch.bucket },
        { name = "S3_SCRATCH_USE_PATH_STYLE", value = "false" },
      ])
      secrets = concat(local.shared_secrets, [
        { name = "DATABASE_URL", valueFrom = data.aws_secretsmanager_secret.runtime["database-url"].arn },
        { name = "ANTHROPIC_API_KEY", valueFrom = data.aws_secretsmanager_secret.runtime["anthropic-api-key"].arn },
      ])
      logConfiguration = merge(local.common_log_config, {
        options = merge(local.common_log_config.options, { "awslogs-stream-prefix" = "worker" })
      })
    },
    {
      # The metrics sidecar -- see the matching container in
      # aws_ecs_task_definition.api (ecs_api.tf) for the full reasoning;
      # identical shape here, just DUMPSTER_SERVICE = "worker".
      name      = "otel-collector"
      image     = "${data.aws_ecr_repository.otel_collector.repository_url}:${var.image_tag}"
      essential = false
      environment = [
        { name = "DUMPSTER_ENV", value = var.environment },
        { name = "DUMPSTER_SERVICE", value = "worker" },
      ]
      portMappings = [
        { containerPort = 4317, name = "otel-collector" }
      ]
      logConfiguration = merge(local.common_log_config, {
        options = merge(local.common_log_config.options, { "awslogs-stream-prefix" = "otel-collector" })
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

  # No Service Connect: worker only ever reached the standalone inference
  # service over it (for /regions), and that call moved to an AWS Batch
  # job submission instead -- see internal/manifest/awsbatch. Nothing
  # left in this cluster needs internal service discovery.

  # Application Auto Scaling (ecs_worker_autoscaling.tf) owns desired_count
  # once this service exists -- same reasoning as aws_ecs_service.api in
  # ecs_api.tf.
  lifecycle {
    ignore_changes = [desired_count]
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

# The Batch job queues/definitions referenced above (aws_batch_job_queue.*,
# aws_batch_job_definition.*) now live in batch.tf, environment-
# parameterized like every other resource in this config -- see that
# file's header for why entity-extraction and embedding each get their
# own per-environment queue/compute environment (no more sharing GPU
# capacity between staging and production), while region extraction
# keeps riding the embedding queue rather than getting a dedicated one.

