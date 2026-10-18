# The event-driven ingestion pipeline (terraform/modules/pipeline): a
# state machine per job type and the Lambdas behind them. cmd/api
# publishes jobs into it through internal/queue/dispatch.
#
# The CI/deploy pipeline builds every ../build/lambda-*/bootstrap binary
# before any tofu operation (staging.yml) -- archive_file is evaluated on
# every plan, apply and destroy.
data "archive_file" "lambda_pipeline" {
  type        = "zip"
  source_file = "${path.module}/../build/lambda-pipeline/bootstrap"
  output_path = "${path.module}/../build/lambda-pipeline.zip"
}

data "archive_file" "lambda_stateless_jobs" {
  type        = "zip"
  source_file = "${path.module}/../build/lambda-stateless-jobs/bootstrap"
  output_path = "${path.module}/../build/lambda-stateless-jobs.zip"
}

data "archive_file" "lambda_malware_scan" {
  type        = "zip"
  source_file = "${path.module}/../build/lambda-malware-scan/bootstrap"
  output_path = "${path.module}/../build/lambda-malware-scan.zip"
}

data "aws_secretsmanager_secret_version" "lambda_database_url_pooled" {
  secret_id = data.aws_secretsmanager_secret.runtime["database-url-pooled"].arn
}

# The pipeline Lambdas' security group: nothing connects to a Lambda over
# the network (they're invoked through the Lambda API), so no ingress.
# Egress stays open until the egress-allowlist change narrows it.
resource "aws_security_group" "lambda" {
  name        = "${local.name_prefix}-lambda"
  description = "Pipeline Lambdas: outbound only"
  vpc_id      = data.aws_vpc.default.id

  egress {
    description = "Outbound to AWS APIs and Neon"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

module "pipeline" {
  source = "./modules/pipeline"

  # The Lambdas run in the private subnets, behind the NAT instance like the
  # API and Batch, so the VPC's egress controls cover them too.
  vpc_config = {
    subnet_ids         = [aws_subnet.private_1a.id, aws_subnet.private_1b.id]
    security_group_ids = [aws_security_group.lambda.id]
  }

  name_prefix = local.name_prefix
  aws_region  = var.aws_region
  account_id  = data.aws_caller_identity.current.account_id

  lambda_zip_path = data.archive_file.lambda_pipeline.output_path
  lambda_zip_hash = data.archive_file.lambda_pipeline.output_base64sha256
  # AWS_REGION is reserved and set by Lambda itself. S3 regions and buckets
  # match the ECS services' shared_environment.
  lambda_environment = {
    DATABASE_URL_POOLED          = data.aws_secretsmanager_secret_version.lambda_database_url_pooled.secret_string
    S3_REGION                    = var.aws_region
    S3_BUCKET                    = aws_s3_bucket.documents.bucket
    S3_USE_PATH_STYLE            = "false"
    S3_SCRATCH_REGION            = var.aws_region
    S3_SCRATCH_BUCKET            = aws_s3_bucket.scratch.bucket
    S3_SCRATCH_USE_PATH_STYLE    = "false"
    ENTITY_EXTRACTION_BATCH_SIZE = "50"
  }

  malware_scan_lambda_zip_path = data.archive_file.lambda_malware_scan.output_path
  malware_scan_lambda_zip_hash = data.archive_file.lambda_malware_scan.output_base64sha256

  stateless_lambda_zip_path = data.archive_file.lambda_stateless_jobs.output_path
  stateless_lambda_zip_hash = data.archive_file.lambda_stateless_jobs.output_base64sha256
  stateless_lambda_environment = {
    BEDROCK_MODEL_ID    = local.bedrock_model_id
    DATABASE_URL_POOLED = data.aws_secretsmanager_secret_version.lambda_database_url_pooled.secret_string
  }
  # Each environment invokes through its own tagged inference profile
  # (bedrock.tf), which routes through the shared cross-region profile to
  # the foundation model; Bedrock checks permission at every hop.
  bedrock_model_arns = concat(
    [aws_bedrock_inference_profile.app.arn],
    [
      "arn:aws:bedrock:${var.aws_region}:${data.aws_caller_identity.current.account_id}:inference-profile/us.anthropic.claude-sonnet-5",
      "arn:aws:bedrock:*::foundation-model/anthropic.claude-sonnet-5",
    ]
  )

  uploads_bucket_arn = aws_s3_bucket.documents.arn
  scratch_bucket_arn = aws_s3_bucket.scratch.arn

  # Region extraction shares the embedding queue (batch.tf).
  batch = {
    embed_job_queue_arn        = aws_batch_job_queue.embedding.arn
    embed_job_definition_name  = aws_batch_job_definition.embedding.name
    layout_job_queue_arn       = aws_batch_job_queue.embedding.arn
    layout_job_definition_name = aws_batch_job_definition.region_extraction.name
    entity_job_queue_arn       = aws_batch_job_queue.entity_extraction.arn
    entity_job_definition_name = aws_batch_job_definition.entity_extraction.name
  }
}

# These lived at the root (lambda_stateless_jobs.tf) before the pipeline
# module took them over.
moved {
  from = aws_lambda_function.stateless_jobs
  to   = module.pipeline.aws_lambda_function.stateless
}

moved {
  from = aws_cloudwatch_log_group.lambda_stateless_jobs
  to   = module.pipeline.aws_cloudwatch_log_group.stateless
}

moved {
  from = aws_iam_role.lambda_stateless_jobs
  to   = module.pipeline.aws_iam_role.stateless
}

moved {
  from = aws_iam_role_policy_attachment.lambda_stateless_jobs_basic_execution
  to   = module.pipeline.aws_iam_role_policy_attachment.stateless_basic_execution
}

# cmd/api publishes through internal/queue/dispatch: it starts document
# indexing and region classification executions on upload, any job type's
# on a retry, and each upload's malware scan timeout
# (internal/malwarescan). All of them are state machines.
data "aws_iam_policy_document" "ecs_task_publish" {
  statement {
    sid       = "StartPipelineExecutions"
    actions   = ["states:StartExecution"]
    resources = values(module.pipeline.state_machine_arns)
  }
}

resource "aws_iam_role_policy" "ecs_task_publish" {
  name   = "${local.name_prefix}-ecs-task-publish"
  role   = aws_iam_role.ecs_task.id
  policy = data.aws_iam_policy_document.ecs_task_publish.json
}
