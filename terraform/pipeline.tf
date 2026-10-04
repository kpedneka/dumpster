# The event-driven ingestion pipeline (terraform/modules/pipeline): the
# document indexing, entity extraction and region classification state
# machines, and the one Lambda (cmd/lambda-pipeline) behind all three.
#
# Deployed but idle until the publish-path cutover: nothing starts these
# state machines until cmd/api publishes through internal/queue/dispatch.
#
# The CI/deploy pipeline builds ../build/lambda-pipeline/bootstrap before
# any tofu operation (staging.yml), the same way it builds
# lambda-stateless-jobs -- archive_file is evaluated on every plan, apply
# and destroy.
data "archive_file" "lambda_pipeline" {
  type        = "zip"
  source_file = "${path.module}/../build/lambda-pipeline/bootstrap"
  output_path = "${path.module}/../build/lambda-pipeline.zip"
}

module "pipeline" {
  source = "./modules/pipeline"

  name_prefix     = local.name_prefix
  aws_region      = var.aws_region
  account_id      = data.aws_caller_identity.current.account_id
  lambda_zip_path = data.archive_file.lambda_pipeline.output_path
  lambda_zip_hash = data.archive_file.lambda_pipeline.output_base64sha256

  # AWS_REGION is reserved and set by Lambda itself (see
  # lambda_stateless_jobs.tf). S3 regions and buckets match the ECS
  # services' shared_environment and the worker's scratch settings.
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

  uploads_bucket_arn = aws_s3_bucket.documents.arn
  scratch_bucket_arn = aws_s3_bucket.scratch.arn

  downstream_queues = {
    edge_extraction_arn  = aws_sqs_queue.job["edge_extraction"].arn
    edge_extraction_url  = aws_sqs_queue.job["edge_extraction"].url
    canonicalization_arn = aws_sqs_queue.job["canonicalization"].arn
    canonicalization_url = aws_sqs_queue.job["canonicalization"].url
  }

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
    sid     = "SendToLambdaQueues"
    actions = ["sqs:SendMessage"]
    resources = [
      aws_sqs_queue.job["edge_extraction"].arn,
      aws_sqs_queue.job["canonicalization"].arn,
    ]
  }
}

resource "aws_iam_role_policy" "ecs_task_publish" {
  name   = "${local.name_prefix}-ecs-task-publish"
  role   = aws_iam_role.ecs_task.id
  policy = data.aws_iam_policy_document.ecs_task_publish.json
}
