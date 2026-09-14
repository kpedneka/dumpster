# api, worker, and cleanup task definitions + api/worker services.
# cleanup has no ECS service (it's not long-running) -- see
# ecs_cleanup.tf for its EventBridge Scheduler + RunTask setup instead.
#
# All three share the one dumpster-runtime image (ecs_ecr.tf), differing
# only by container command -- confirmed against the actual Dockerfile,
# not assumed. Env vars/secrets below are grounded in internal/config's
# real fields, not guessed: object storage points at real AWS S3
# (s3_storage.tf, IAM-role auth, no static key) -- Neon direct is still in
# place (Aurora migration is separate, later work); Anthropic direct is
# now production-scoped only to staging -- see local.llm_provider below.

locals {
  # Production launches on Bedrock, not direct Anthropic, from day one --
  # not a preference, a real cost incident: a load-testing burst against
  # staging burned real non-refundable Anthropic credit in under an hour
  # with no spend controls in between the app and the bill at all. Bedrock
  # bills through AWS instead, authenticated via this task's own IAM role,
  # no API key in the request path. Staging keeps direct Anthropic for now
  # (cheaper to iterate against while its own remaining credit lasts --
  # see the "DNS and TLS cutover plan" dev board card's notes) -- not a
  # permanent split, just not worth re-plumbing both environments at once
  # under real time pressure to ship the cutover itself.
  llm_provider = var.environment == "production" ? "bedrock" : "anthropic"

  # Env vars/secrets every one of the three commands needs, regardless of
  # which binary runs -- object storage and AWS region are read by all
  # three (cleanup deletes real documents from S3 on account expiry).
  #
  # No S3_ENDPOINT: real AWS S3 needs no BaseEndpoint override, only a
  # region (see internal/objectstore/s3store.Config's doc comment) -- unlike
  # the R2 endpoint this replaced, which had to be a Cloudflare account-
  # specific URL. No S3_ACCESS_KEY/SECRET_KEY either: left unset so
  # s3store.New resolves credentials via the ECS task role instead (granted
  # S3 access in s3_storage.tf) -- real AWS S3 participates in IAM, unlike
  # R2, so there's no static key to manage or rotate for this bucket at all
  # any more.
  shared_environment = [
    { name = "AWS_REGION", value = var.aws_region },
    { name = "S3_REGION", value = var.aws_region },
    { name = "S3_BUCKET", value = aws_s3_bucket.documents.bucket },
    { name = "S3_USE_PATH_STYLE", value = "false" },
    { name = "LLM_PROVIDER", value = local.llm_provider },
    { name = "BEDROCK_MODEL_ID", value = local.bedrock_model_id },
  ]
  shared_secrets = []

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
      image     = "${data.aws_ecr_repository.runtime.repository_url}:${var.image_tag}"
      command   = ["/bin/api"]
      essential = true
      portMappings = [
        { containerPort = 8080, name = "api" }
      ]
      environment = concat(local.shared_environment, [
        { name = "HTTP_PORT", value = "8080" },
        { name = "ANTHROPIC_MODEL", value = var.anthropic_model },
        # localhost, not the Service Connect DNS name -- the query
        # embedder now talks to the sidecar container below, in this
        # same task, not the standalone inference service. No Go code
        # change needed for this: internal/llm/inference's HTTP client
        # is baseURL-agnostic, this is purely a config value. Region
        # classification (cmd/worker's separate INFERENCE_SERVICE_URL,
        # see ecs_worker.tf) is unaffected -- it still calls the
        # standalone service, unchanged, until that's folded into the
        # AWS Batch embed job in a separate, later card.
        { name = "INFERENCE_SERVICE_URL", value = "http://localhost:8000" },
        { name = "COOKIE_SECURE", value = "true" },
        { name = "MAX_DOCUMENTS_PER_SESSION", value = tostring(var.max_documents_per_session) },
        { name = "RATE_LIMIT_REQUESTS", value = tostring(var.rate_limit_requests) },
        { name = "MAX_COMMUNITY_GRAPH_ENTITIES", value = "5000" },
      ])
      secrets = concat(local.shared_secrets, [
        { name = "DATABASE_URL_POOLED", valueFrom = data.aws_secretsmanager_secret.runtime["database-url-pooled"].arn },
        { name = "ANTHROPIC_API_KEY", valueFrom = data.aws_secretsmanager_secret.runtime["anthropic-api-key"].arn },
      ])
      logConfiguration = merge(local.common_log_config, {
        options = merge(local.common_log_config.options, { "awslogs-stream-prefix" = "api" })
      })
    },
    {
      # The query-embedding sidecar -- see scripts/embed_service.py and
      # Dockerfile.batch-embed-job's `sidecar` target. A second, real
      # ECS-managed container in api's own task (Fargate's awsvpc mode
      # gives every container in one task definition a shared network
      # namespace, so "localhost" above genuinely reaches this one, no
      # Service Connect needed for it) -- explicitly not the old
      # per-Go-process subprocess pattern this project already retired
      # once (see the "Eliminate the standalone inference service" dev
      # board card): a real container with its own health check, not a
      # subprocess with no timeout.
      #
      # essential = true, deliberately: if this container dies, api
      # genuinely can't serve search (every query needs an embedding),
      # so the whole task should be considered failed and restarted by
      # ECS rather than silently degrading with the api container still
      # accepting traffic it can't actually fulfill.
      name      = "embed-sidecar"
      image     = "${data.aws_ecr_repository.embed_sidecar.repository_url}:${var.image_tag}"
      essential = true
      # Caps PyTorch's (and the underlying BLAS library's) CPU thread count
      # -- ported from the old fly.inference.toml, where the identical
      # setting was load-bearing: measured directly, an unset default let
      # PyTorch autodetect the *host's* logical CPU count rather than this
      # container's real allocation, and the resulting over-threading
      # turned a 47s embedding call into 13m18s under throttling (see
      # internal/llm/awsbatch's package doc for the full story). Set to
      # "1", not Fly's "2": that value matched shared-cpu-2x's 2 dedicated
      # vCPUs for this process alone, but api_cpu below is 1024 units (1
      # vCPU) shared across BOTH containers in this task -- "1" is the
      # correct translation of the same fix to this task's actual
      # allocation, not a guess. Revisit if api_cpu is ever raised.
      environment = [
        { name = "OMP_NUM_THREADS", value = "1" },
        { name = "MKL_NUM_THREADS", value = "1" },
        { name = "OPENBLAS_NUM_THREADS", value = "1" },
      ]
      portMappings = [
        { containerPort = 8000, name = "embed-sidecar" }
      ]
      # No curl in this image (kept minimal on purpose -- see the
      # Dockerfile) -- a plain Python one-liner instead of adding a
      # dependency just for the health check.
      healthCheck = {
        command     = ["CMD-SHELL", "python3 -c \"import urllib.request; urllib.request.urlopen('http://localhost:8000/healthz')\" || exit 1"]
        interval    = 15
        timeout     = 5
        retries     = 3
        startPeriod = 30
      }
      logConfiguration = merge(local.common_log_config, {
        options = merge(local.common_log_config.options, { "awslogs-stream-prefix" = "embed-sidecar" })
      })
    },
    {
      # The metrics sidecar -- see Dockerfile.otel-collector and
      # otel-collector-config.yaml. internal/telemetry pushes OTLP over
      # localhost:4317 to this container, which exports to CloudWatch (see
      # the "Push OTel metrics to CloudWatch" dev board card).
      #
      # essential = false, deliberately unlike embed-sidecar above: this
      # container dying degrades observability, not the ability to serve a
      # real user request, so it must not take the whole task down with it.
      name      = "otel-collector"
      image     = "${data.aws_ecr_repository.otel_collector.repository_url}:${var.image_tag}"
      essential = false
      environment = [
        { name = "DUMPSTER_ENV", value = var.environment },
        { name = "DUMPSTER_SERVICE", value = "api" },
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

  # No Service Connect: the only thing api ever reached over it was the
  # standalone inference service (query embeddings), which now runs as a
  # same-task sidecar reached over localhost instead -- see the
  # embed-sidecar container in aws_ecs_task_definition.api above.

  # Application Auto Scaling (ecs_api_autoscaling.tf) owns desired_count
  # once this service exists -- without ignore_changes here, every `tofu
  # apply` would fight the autoscaler by resetting desired_count back to
  # this resource's static value, undoing whatever scale-out the target
  # tracking policy had put in place moments before.
  lifecycle {
    ignore_changes = [desired_count]
  }

  depends_on = [aws_lb_listener.https]
}

variable "api_cpu" {
  type        = string
  description = "Fargate vCPU units (1024 = 1 vCPU) for the whole task -- shared across the api and embed-sidecar containers, not per-container. Not yet real-world sized against actual sidecar CPU usage under load (the standalone inference service it's descended from ran regions+embeddings combined at 1024 -- dropping regions should need meaningfully less, but that's not measured yet); revisit once staging has real query traffic to profile against."
  default     = "1024"
}

variable "api_memory" {
  type        = string
  description = "Fargate memory (MiB) for the whole task -- shared across the api and embed-sidecar containers, not per-container. Bumped from the pre-sidecar 1024 default: the standalone inference service alone (embeddings + region classification) measured ~866MB used on Fly; the sidecar drops region classification's memory (pdfplumber/pymupdf, plus its isolated-subprocess ceiling) but this number isn't independently measured yet either -- same caveat as api_cpu."
  default     = "2048"
}

variable "anthropic_model" {
  type        = string
  description = "Claude model ID for staging, still called directly against the Anthropic API -- production uses Bedrock instead, see local.llm_provider and var.bedrock_model_id."
  default     = "claude-sonnet-4-6"
}

variable "bedrock_model_id" {
  type        = string
  description = "Bedrock model ID or inference profile ID/ARN production's api/worker/cleanup invoke via AWS Bedrock's Converse API. Not the bare model name the direct Anthropic API uses -- Claude Sonnet 5 specifically rejects on-demand invocation by model ID and requires an inference profile (confirmed for real against this account: `aws bedrock-runtime converse --model-id anthropic.claude-sonnet-5` fails with ValidationException; the US cross-region inference profile below is what actually works). Read by every environment's task definitions (harmless when local.llm_provider is \"anthropic\" -- just an unused env var), not only production's, so flipping LLM_PROVIDER back to \"anthropic\" during a Bedrock outage never needs a redeploy to add a missing value."
  default     = "us.anthropic.claude-sonnet-5"
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
