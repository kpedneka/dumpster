# The event-driven ingestion pipeline (terraform/modules/pipeline): the
# document indexing, entity extraction and region classification state
# machines and the Lambda behind them, plus the edge extraction and
# canonicalization queues and the stateless-jobs Lambda that consumes them.
#
# Deployed but idle until the publish-path cutover: nothing publishes jobs
# into it until cmd/api uses internal/queue/dispatch.
#
# The CI/deploy pipeline builds both ../build/lambda-*/bootstrap binaries
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

data "aws_secretsmanager_secret_version" "lambda_database_url_pooled" {
  secret_id = data.aws_secretsmanager_secret.runtime["database-url-pooled"].arn
}

data "aws_secretsmanager_secret_version" "lambda_anthropic_api_key" {
  secret_id = data.aws_secretsmanager_secret.runtime["anthropic-api-key"].arn
}

module "pipeline" {
  source = "./modules/pipeline"

  name_prefix = local.name_prefix
  aws_region  = var.aws_region
  account_id  = data.aws_caller_identity.current.account_id

  lambda_zip_path = data.archive_file.lambda_pipeline.output_path
  lambda_zip_hash = data.archive_file.lambda_pipeline.output_base64sha256
  # AWS_REGION is reserved and set by Lambda itself. S3 regions and buckets
  # match the ECS services' shared_environment and the worker's scratch
  # settings.
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

  stateless_lambda_zip_path = data.archive_file.lambda_stateless_jobs.output_path
  stateless_lambda_zip_hash = data.archive_file.lambda_stateless_jobs.output_base64sha256
  stateless_lambda_environment = {
    LLM_PROVIDER        = local.llm_provider
    BEDROCK_MODEL_ID    = local.bedrock_model_id
    ANTHROPIC_MODEL     = var.anthropic_model
    DATABASE_URL_POOLED = data.aws_secretsmanager_secret_version.lambda_database_url_pooled.secret_string
    ANTHROPIC_API_KEY   = data.aws_secretsmanager_secret_version.lambda_anthropic_api_key.secret_string
  }
  # Production invokes through its own tagged inference profile
  # (bedrock.tf); every environment may also use the shared cross-region
  # profile and the foundation model it routes to.
  bedrock_model_arns = concat(
    var.environment == "production" ? [aws_bedrock_inference_profile.app[0].arn] : [],
    [
      "arn:aws:bedrock:${var.aws_region}:${data.aws_caller_identity.current.account_id}:inference-profile/us.anthropic.claude-sonnet-5",
      "arn:aws:bedrock:*::foundation-model/anthropic.claude-sonnet-5",
    ]
  )

  uploads_bucket_arn = aws_s3_bucket.documents.arn
  scratch_bucket_arn = aws_s3_bucket.scratch.arn

  # Region extraction shares the embedding queue, as it does on the worker
  # (ecs_worker.tf's REGIONS_BATCH_JOB_QUEUE).
  batch = {
    embed_job_queue_arn        = aws_batch_job_queue.embedding.arn
    embed_job_definition_name  = aws_batch_job_definition.embedding.name
    layout_job_queue_arn       = aws_batch_job_queue.embedding.arn
    layout_job_definition_name = aws_batch_job_definition.region_extraction.name
    entity_job_queue_arn       = aws_batch_job_queue.entity_extraction.arn
    entity_job_definition_name = aws_batch_job_definition.entity_extraction.name
  }
}

# These lived at the root (queue_sqs.tf, lambda_stateless_jobs.tf) before
# the pipeline module took them over. Staging's state has the queues from
# a targeted apply; nothing else existed yet in any environment.
moved {
  from = aws_sqs_queue.job
  to   = module.pipeline.aws_sqs_queue.job
}

moved {
  from = aws_sqs_queue.job_dlq
  to   = module.pipeline.aws_sqs_queue.job_dlq
}

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

moved {
  from = aws_lambda_event_source_mapping.edge_extraction
  to   = module.pipeline.aws_lambda_event_source_mapping.stateless["edge_extraction"]
}

moved {
  from = aws_lambda_event_source_mapping.canonicalization
  to   = module.pipeline.aws_lambda_event_source_mapping.stateless["canonicalization"]
}

output "job_queue_urls" {
  value       = module.pipeline.job_queue_urls
  description = "SQS queue URL per Lambda job type (edge_extraction, canonicalization)."
}

# cmd/api publishes through internal/queue/dispatch after cutover: it
# starts document indexing and region classification executions on upload,
# and entity extraction on a retry. Granted now so the cutover is a code
# and config change only.
data "aws_iam_policy_document" "ecs_task_publish" {
  statement {
    sid       = "StartPipelineExecutions"
    actions   = ["states:StartExecution"]
    resources = values(module.pipeline.state_machine_arns)
  }
  statement {
    sid       = "SendToLambdaQueues"
    actions   = ["sqs:SendMessage"]
    resources = values(module.pipeline.job_queue_arns)
  }
}

resource "aws_iam_role_policy" "ecs_task_publish" {
  name   = "${local.name_prefix}-ecs-task-publish"
  role   = aws_iam_role.ecs_task.id
  policy = data.aws_iam_policy_document.ecs_task_publish.json
}
