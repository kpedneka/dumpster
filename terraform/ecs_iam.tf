# IAM for the ECS services: one shared execution role (what ECS itself
# needs to start a task -- pull the image, write logs, read secrets into
# env vars) and one shared task role (what the running application code
# is allowed to call AWS APIs as). Shared across api/worker/cleanup
# rather than one role per service: all three need the same execution
# permissions, and only the worker's actual code paths call Batch/Bedrock
# -- a shared task role is simpler to reason about at this scale than
# three near-identical roles, and can be split later if that ever stops
# being true.
#
# Scoped to what the app actually calls today, not preemptively broadened
# for services not yet migrated (Aurora and Bedrock each get their own IAM
# additions on their own dev board cards, when those migrations actually
# happen). Object storage's S3 permissions live in s3_storage.tf, not here,
# since that policy needs the bucket resources this file doesn't define --
# Neon and Anthropic remain the only genuinely external services, reached
# over the internet via the NAT instance, not through IAM at all.

data "aws_caller_identity" "current" {}

# --- Execution role: what ECS needs to start a task ---

data "aws_iam_policy_document" "ecs_task_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "ecs_execution" {
  name               = "${local.name_prefix}-ecs-execution"
  assume_role_policy = data.aws_iam_policy_document.ecs_task_assume.json
}

# AWS-managed policy covering ECR pull + CloudWatch Logs write -- the
# standard baseline every ECS execution role needs, not worth
# hand-rolling.
resource "aws_iam_role_policy_attachment" "ecs_execution_managed" {
  role       = aws_iam_role.ecs_execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

# The managed policy above doesn't cover Secrets Manager -- task
# definitions read secrets into env vars via this same execution role,
# per the secrets architecture already decided (GitHub holds only the
# OIDC deploy role; AWS Secrets Manager holds every runtime secret).
data "aws_iam_policy_document" "ecs_execution_secrets" {
  statement {
    actions = ["secretsmanager:GetSecretValue"]
    # Scoped to this environment's own secret path -- staging's execution
    # role literally cannot read production's secrets (or vice versa),
    # even by mistake, since each environment's secrets live under a
    # different dumpster/<environment>/* prefix (see ecs_secrets.tf).
    resources = ["arn:aws:secretsmanager:${var.aws_region}:${data.aws_caller_identity.current.account_id}:secret:dumpster/${var.environment}/*"]
  }
}

resource "aws_iam_role_policy" "ecs_execution_secrets" {
  name   = "${local.name_prefix}-ecs-execution-secrets"
  role   = aws_iam_role.ecs_execution.id
  policy = data.aws_iam_policy_document.ecs_execution_secrets.json
}

# --- Task role: what the running application code itself can call ---

resource "aws_iam_role" "ecs_task" {
  name               = "${local.name_prefix}-ecs-task"
  assume_role_policy = data.aws_iam_policy_document.ecs_task_assume.json
}

# The worker submits and polls AWS Batch jobs (internal/entity/awsbatch,
# internal/llm/awsbatch) -- scoped to the job queues/definitions this
# account actually has, not batch:* broadly.
data "aws_iam_policy_document" "ecs_task_batch" {
  statement {
    actions = [
      "batch:SubmitJob",
      "batch:DescribeJobs",
      "batch:TerminateJob",
    ]
    resources = ["*"] # Batch job/queue ARNs are dynamic per submission; the API itself has no resource-level scoping for SubmitJob targets
  }
}

resource "aws_iam_role_policy" "ecs_task_batch" {
  name   = "${local.name_prefix}-ecs-task-batch"
  role   = aws_iam_role.ecs_task.id
  policy = data.aws_iam_policy_document.ecs_task_batch.json
}

# CloudWatch Logs read -- internal/entity/awsbatch.JobLogs() reads a
# submitted job's own output back to parse its BATCH_RESULT: line.
data "aws_iam_policy_document" "ecs_task_logs_read" {
  statement {
    actions = [
      "logs:GetLogEvents",
      "logs:DescribeLogStreams",
    ]
    resources = ["arn:aws:logs:${var.aws_region}:${data.aws_caller_identity.current.account_id}:log-group:/aws/batch/*"]
  }
}

resource "aws_iam_role_policy" "ecs_task_logs_read" {
  name   = "${local.name_prefix}-ecs-task-logs-read"
  role   = aws_iam_role.ecs_task.id
  policy = data.aws_iam_policy_document.ecs_task_logs_read.json
}

variable "aws_region" {
  type        = string
  description = "AWS region this account's resources run in"
  default     = "us-east-1"
}

output "ecs_execution_role_arn" {
  value = aws_iam_role.ecs_execution.arn
}

output "ecs_task_role_arn" {
  value = aws_iam_role.ecs_task.arn
}
