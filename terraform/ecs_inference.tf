# The standalone inference service (query-time embeddings + PDF region
# classification) -- still deployed as-is here, matching today's actual
# Fly architecture exactly. Eliminating it (folding embeddings into an
# api sidecar, region classification into the Batch embed job) is its
# own separate, later card ("Eliminate the standalone inference service"
# / "Fold region classification into the AWS Batch embed job",
# Workstream 3) -- this card's job is a working, equivalent migration
# first, not the optimized end state yet.
#
# api and worker reach this over ECS Service Connect at the stable name
# "inference", matching INFERENCE_SERVICE_URL's current
# http://inference:8000 (Docker Compose's own built-in service-name DNS)
# -- Service Connect is ECS's native equivalent of that same idea.

resource "aws_service_discovery_http_namespace" "internal" {
  name        = "${var.environment}.dumpster.internal"
  description = "Service Connect namespace for api/worker/inference internal calls"
}

resource "aws_ecr_repository" "inference" {
  name                 = "${local.name_prefix}-inference"
  image_tag_mutability = "IMMUTABLE"

  # Staging only -- see the matching comment on aws_ecr_repository.runtime
  # in ecs_ecr.tf for why.
  force_delete = var.environment == "staging"

  image_scanning_configuration {
    scan_on_push = true
  }
}

resource "aws_security_group" "inference" {
  name        = "${local.name_prefix}-inference"
  description = "inference task: inbound only from the api/worker security group, on the internal Service Connect port"
  vpc_id      = data.aws_vpc.default.id

  ingress {
    description     = "From api/worker only"
    from_port       = 8000
    to_port         = 8000
    protocol        = "tcp"
    security_groups = [aws_security_group.ecs_tasks.id]
  }

  egress {
    description = "Outbound via the NAT instance"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Name = "${local.name_prefix}-inference"
  }
}

resource "aws_ecs_task_definition" "inference" {
  family                   = "${local.name_prefix}-inference"
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

  cpu                = var.inference_cpu
  memory             = var.inference_memory
  execution_role_arn = aws_iam_role.ecs_execution.arn
  task_role_arn      = aws_iam_role.ecs_task.arn

  container_definitions = jsonencode([
    {
      name      = "inference"
      image     = "${aws_ecr_repository.inference.repository_url}:${var.image_tag}"
      essential = true
      portMappings = [
        { containerPort = 8000, name = "inference" }
      ]
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          "awslogs-group"         = aws_cloudwatch_log_group.ecs.name
          "awslogs-region"        = var.aws_region
          "awslogs-stream-prefix" = "inference"
        }
      }
    }
  ])
}

resource "aws_ecs_service" "inference" {
  name            = "inference"
  cluster         = aws_ecs_cluster.main.arn
  task_definition = aws_ecs_task_definition.inference.arn
  desired_count   = 1
  launch_type     = "FARGATE"

  network_configuration {
    subnets         = [aws_subnet.private_1a.id, aws_subnet.private_1b.id]
    security_groups = [aws_security_group.inference.id]
  }

  service_connect_configuration {
    enabled   = true
    namespace = aws_service_discovery_http_namespace.internal.arn

    service {
      port_name      = "inference"
      discovery_name = "inference"
      client_alias {
        port     = 8000
        dns_name = "inference"
      }
    }
  }
}

variable "inference_cpu" {
  type        = string
  description = "Fargate vCPU units (1024 = 1 vCPU) for the inference task. Not yet real-world sized -- this is a placeholder pending the same benchmarking the Scalability Review page already owes for GLiNER/embedder throughput."
  default     = "1024"
}

variable "inference_memory" {
  type        = string
  description = "Fargate memory (MiB) for the inference task. Placeholder pending real sizing -- the current Fly machine runs shared-cpu-2x/4096MB for this service."
  default     = "4096"
}

variable "image_tag" {
  type        = string
  description = "Tag of the images to deploy, shared across all task definitions -- set by the CD pipeline to the commit SHA being deployed."
  default     = "latest"
}
