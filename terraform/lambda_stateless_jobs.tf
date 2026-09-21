# The Lambda deployment of cmd/lambda-stateless-jobs -- the "Deploy the
# Lambda" dev board card. Handles edge_extraction and canonicalization,
# the two queue.JobType stages that are pure local Go computation with no
# AWS Batch hop, dispatching via internal/worker/lambda.Dispatcher to the
# same EdgeHandler/CanonicalizationHandler the ECS worker already runs.
# See the "Event-Driven Job Orchestration" System Architecture page for
# the full design.
#
# Zip deployment (provided.al2023, arm64), not a container image: Go's
# Lambda story doesn't need one, and a zip avoids growing the ECR-based
# build path this repo already has (Dockerfile, ecs_ecr.tf) for a
# fundamentally different artifact type. The CI/deploy pipeline builds
# ../build/lambda-stateless-jobs/bootstrap before `tofu apply` runs (see
# staging.yml's "Build lambda-stateless-jobs" step) -- this file only
# zips whatever's already there.
data "archive_file" "lambda_stateless_jobs" {
  type        = "zip"
  source_file = "${path.module}/../build/lambda-stateless-jobs/bootstrap"
  output_path = "${path.module}/../build/lambda-stateless-jobs.zip"
}

resource "aws_cloudwatch_log_group" "lambda_stateless_jobs" {
  name              = "/aws/lambda/${local.name_prefix}-stateless-jobs"
  retention_in_days = 60 # matching every other log group in this account
}

data "aws_iam_policy_document" "lambda_stateless_jobs_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "lambda_stateless_jobs" {
  name               = "${local.name_prefix}-lambda-stateless-jobs"
  assume_role_policy = data.aws_iam_policy_document.lambda_stateless_jobs_assume.json
}

resource "aws_iam_role_policy_attachment" "lambda_stateless_jobs_basic_execution" {
  role       = aws_iam_role.lambda_stateless_jobs.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

# Scoped to exactly the two queues this Lambda actually consumes -- not
# every queue queue_sqs.tf creates. document_indexing/entity_extraction/
# region_classification are workstream C's (Step Functions), never this
# function's.
data "aws_iam_policy_document" "lambda_stateless_jobs_sqs" {
  statement {
    actions = [
      "sqs:ReceiveMessage",
      "sqs:DeleteMessage",
      "sqs:GetQueueAttributes",
    ]
    resources = [
      aws_sqs_queue.job["edge_extraction"].arn,
      aws_sqs_queue.job["canonicalization"].arn,
    ]
  }
}

resource "aws_iam_role_policy" "lambda_stateless_jobs_sqs" {
  name   = "${local.name_prefix}-lambda-stateless-jobs-sqs"
  role   = aws_iam_role.lambda_stateless_jobs.id
  policy = data.aws_iam_policy_document.lambda_stateless_jobs_sqs.json
}

data "aws_iam_policy_document" "lambda_stateless_jobs_secrets" {
  statement {
    actions = ["secretsmanager:GetSecretValue"]
    resources = [
      data.aws_secretsmanager_secret.runtime["database-url-pooled"].arn,
      data.aws_secretsmanager_secret.runtime["anthropic-api-key"].arn,
    ]
  }
}

resource "aws_iam_role_policy" "lambda_stateless_jobs_secrets" {
  name   = "${local.name_prefix}-lambda-stateless-jobs-secrets"
  role   = aws_iam_role.lambda_stateless_jobs.id
  policy = data.aws_iam_policy_document.lambda_stateless_jobs_secrets.json
}

# Same 3-hop chain as ecs_iam.tf's data.aws_iam_policy_document.
# ecs_task_bedrock -- see that resource's comment for why all three ARNs
# are needed, not just the one actually named in a request. Copied, not
# shared: this Lambda's role and the ECS task role are separate
# principals with separate blast radii, even though they need identical
# Bedrock access.
data "aws_iam_policy_document" "lambda_stateless_jobs_bedrock" {
  statement {
    actions = [
      "bedrock:InvokeModel",
      "bedrock:InvokeModelWithResponseStream",
      "bedrock:Converse",
      "bedrock:ConverseStream",
    ]
    resources = concat(
      var.environment == "production" ? [aws_bedrock_inference_profile.app[0].arn] : [],
      [
        "arn:aws:bedrock:${var.aws_region}:${data.aws_caller_identity.current.account_id}:inference-profile/us.anthropic.claude-sonnet-5",
        "arn:aws:bedrock:*::foundation-model/anthropic.claude-sonnet-5",
      ]
    )
  }
}

resource "aws_iam_role_policy" "lambda_stateless_jobs_bedrock" {
  name   = "${local.name_prefix}-lambda-stateless-jobs-bedrock"
  role   = aws_iam_role.lambda_stateless_jobs.id
  policy = data.aws_iam_policy_document.lambda_stateless_jobs_bedrock.json
}

# Secret *values*, not just ARNs: unlike ECS's `secrets` block (valueFrom
# = ARN, resolved by the ECS agent at container start, never entering
# Terraform state), Lambda environment variables are static strings this
# config has to resolve itself at apply time. That does mean these two
# values land in Terraform state -- an accepted, explicit tradeoff at
# this project's scale (state already lives in a private, non-public S3
# bucket -- state_backend.tf -- and every other secret this account holds
# is reachable by anyone who can already read that bucket or assume the
# deploy role), not an oversight. The AWS Parameters and Secrets Lambda
# Extension is the alternative that avoids this entirely by fetching
# secrets at runtime instead of deploy time -- worth it if this ever
# needs a stricter blast radius than "same as everything else in this
# account," not before.
data "aws_secretsmanager_secret_version" "lambda_database_url_pooled" {
  secret_id = data.aws_secretsmanager_secret.runtime["database-url-pooled"].arn
}

data "aws_secretsmanager_secret_version" "lambda_anthropic_api_key" {
  secret_id = data.aws_secretsmanager_secret.runtime["anthropic-api-key"].arn
}

resource "aws_lambda_function" "stateless_jobs" {
  function_name = "${local.name_prefix}-stateless-jobs"
  role          = aws_iam_role.lambda_stateless_jobs.arn
  handler       = "bootstrap"
  runtime       = "provided.al2023"
  architectures = ["arm64"]

  filename         = data.archive_file.lambda_stateless_jobs.output_path
  source_code_hash = data.archive_file.lambda_stateless_jobs.output_base64sha256

  # 60s: generous for a DB write plus, for canonicalization, a couple of
  # synchronous Anthropic/Bedrock calls (alias judge, cross-link) -- see
  # canonicalizationhandler.go's own doc on why those are still inline
  # HTTP calls, not a Batch hop. No AWS Batch wait to size around here at
  # all, unlike the workstream C job types.
  timeout     = 60
  memory_size = 512

  environment {
    variables = {
      AWS_REGION          = var.aws_region
      LLM_PROVIDER        = local.llm_provider
      BEDROCK_MODEL_ID    = local.bedrock_model_id
      ANTHROPIC_MODEL     = var.anthropic_model
      DATABASE_URL_POOLED = data.aws_secretsmanager_secret_version.lambda_database_url_pooled.secret_string
      ANTHROPIC_API_KEY   = data.aws_secretsmanager_secret_version.lambda_anthropic_api_key.secret_string
    }
  }

  depends_on = [aws_cloudwatch_log_group.lambda_stateless_jobs]
}

resource "aws_lambda_event_source_mapping" "edge_extraction" {
  event_source_arn = aws_sqs_queue.job["edge_extraction"].arn
  function_name    = aws_lambda_function.stateless_jobs.arn
  # Matches Dispatcher.HandleSQSEvent's per-record BatchItemFailures
  # reporting -- without this, a batch containing even one failed record
  # would redeliver every record in it, including the ones that already
  # succeeded.
  function_response_types = ["ReportBatchItemFailures"]
}

resource "aws_lambda_event_source_mapping" "canonicalization" {
  event_source_arn        = aws_sqs_queue.job["canonicalization"].arn
  function_name           = aws_lambda_function.stateless_jobs.arn
  function_response_types = ["ReportBatchItemFailures"]
}
