# The event-driven ingestion pipeline, everything that runs a job once it's
# published:
#   - a Step Functions Standard state machine per job type, whose
#     definitions are the same .asl.json files the Go tests check against
#     the handlers: document indexing, entity extraction and region
#     classification (internal/pipeline/*/statemachine.asl.json) run on the
#     pipeline Lambda (cmd/lambda-pipeline), in this file; edge extraction
#     and canonicalization (internal/worker/lambda/statemachine.asl.json)
#     run on the stateless-jobs Lambda (stateless.tf); the malware scan
#     timeout (internal/malwarescan/timeout.asl.json) runs on the malware
#     scan Lambda (malware_scan.tf).
# Nothing polls: every job is a state machine execution the publisher
# starts, so an idle pipeline makes no requests at all.
# Used by the main stack (staging, production) and the pipeline-only dev
# stack (terraform/dev), so every environment runs exactly the same thing.
#
# The Lambdas publish jobs to these state machines, and the state machines
# invoke the Lambdas. Referencing each other's resources would be a cycle,
# so both the state machine ARNs and the Lambda ARNs they invoke are built
# from their names instead.

locals {
  state_machines = {
    document_indexing = {
      name       = "${var.name_prefix}-document-indexing"
      definition = "${path.module}/../../../internal/pipeline/docindex/statemachine.asl.json"
      vars = {
        embed_job_queue      = var.batch.embed_job_queue_arn
        embed_job_definition = var.batch.embed_job_definition_name
      }
    }
    entity_extraction = {
      name       = "${var.name_prefix}-entity-extraction"
      definition = "${path.module}/../../../internal/pipeline/entityextract/statemachine.asl.json"
      vars = {
        entity_job_queue      = var.batch.entity_job_queue_arn
        entity_job_definition = var.batch.entity_job_definition_name
      }
    }
    region_classification = {
      name       = "${var.name_prefix}-region-classification"
      definition = "${path.module}/../../../internal/pipeline/regionclassify/statemachine.asl.json"
      vars = {
        layout_job_queue      = var.batch.layout_job_queue_arn
        layout_job_definition = var.batch.layout_job_definition_name
        embed_job_queue       = var.batch.embed_job_queue_arn
        embed_job_definition  = var.batch.embed_job_definition_name
      }
    }
    edge_extraction = {
      name       = "${var.name_prefix}-edge-extraction"
      definition = "${path.module}/../../../internal/worker/lambda/statemachine.asl.json"
      vars       = { lambda_arn = local.stateless_function_arn }
    }
    canonicalization = {
      name       = "${var.name_prefix}-canonicalization"
      definition = "${path.module}/../../../internal/worker/lambda/statemachine.asl.json"
      vars       = { lambda_arn = local.stateless_function_arn }
    }
    malware_scan_timeout = {
      name       = "${var.name_prefix}-malware-scan-timeout"
      definition = "${path.module}/../../../internal/malwarescan/timeout.asl.json"
      vars = {
        lambda_arn      = local.malware_scan_function_arn
        timeout_seconds = var.malware_scan_timeout_seconds
      }
    }
  }
  state_machine_arns = {
    for k, sm in local.state_machines :
    k => "arn:aws:states:${var.aws_region}:${var.account_id}:stateMachine:${sm.name}"
  }
  function_name = "${var.name_prefix}-pipeline"
  function_arn  = "arn:aws:lambda:${var.aws_region}:${var.account_id}:function:${local.function_name}"
}

# --- Lambda ---

resource "aws_cloudwatch_log_group" "lambda" {
  name              = "/aws/lambda/${local.function_name}"
  retention_in_days = var.log_retention_days
}

data "aws_iam_policy_document" "lambda_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "lambda" {
  name               = "${var.name_prefix}-lambda-pipeline"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume.json
}

resource "aws_iam_role_policy_attachment" "lambda_basic_execution" {
  role       = aws_iam_role.lambda.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

# Creating and removing the network interfaces a Lambda in a VPC uses.
resource "aws_iam_role_policy_attachment" "lambda_vpc_access" {
  count      = var.vpc_config == null ? 0 : 1
  role       = aws_iam_role.lambda.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole"
}

data "aws_iam_policy_document" "lambda" {
  # Only when uploads live in an AWS bucket. Local dev's uploads are on
  # Cloudflare R2, reached with static keys in the Lambda's environment.
  dynamic "statement" {
    for_each = var.uploads_bucket_arn == null ? [] : [var.uploads_bucket_arn]
    content {
      sid       = "ReadUploads"
      actions   = ["s3:GetObject"]
      resources = ["${statement.value}/*"]
    }
  }
  statement {
    sid       = "ScratchHandoff"
    actions   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"]
    resources = ["${var.scratch_bucket_arn}/*"]
  }
  statement {
    sid       = "PublishToStateMachines"
    actions   = ["states:StartExecution"]
    resources = values(local.state_machine_arns)
  }
}

resource "aws_iam_role_policy" "lambda" {
  name   = "${var.name_prefix}-lambda-pipeline"
  role   = aws_iam_role.lambda.id
  policy = data.aws_iam_policy_document.lambda.json
}

# Where internal/queue/dispatch publishes each job type, for every Lambda
# that starts jobs (this one and the malware scan's).
locals {
  dispatch_environment = {
    DOCUMENT_INDEXING_STATE_MACHINE_ARN     = local.state_machine_arns.document_indexing
    ENTITY_EXTRACTION_STATE_MACHINE_ARN     = local.state_machine_arns.entity_extraction
    REGION_CLASSIFICATION_STATE_MACHINE_ARN = local.state_machine_arns.region_classification
    EDGE_EXTRACTION_STATE_MACHINE_ARN       = local.state_machine_arns.edge_extraction
    CANONICALIZATION_STATE_MACHINE_ARN      = local.state_machine_arns.canonicalization
  }
}

resource "aws_lambda_function" "pipeline" {
  function_name    = local.function_name
  role             = aws_iam_role.lambda.arn
  handler          = "bootstrap"
  runtime          = "provided.al2023"
  architectures    = ["arm64"]
  filename         = var.lambda_zip_path
  source_code_hash = var.lambda_zip_hash

  # Steps read a document or a layout result and write its chunks or
  # entities; 5 minutes covers a large document with room to spare, and
  # 1GB covers a layout result carrying base64 figure crops.
  timeout     = 300
  memory_size = 1024

  environment {
    variables = merge(var.lambda_environment, local.dispatch_environment)
  }


  dynamic "vpc_config" {
    for_each = var.vpc_config == null ? [] : [var.vpc_config]
    content {
      subnet_ids         = vpc_config.value.subnet_ids
      security_group_ids = vpc_config.value.security_group_ids
    }
  }

  depends_on = [aws_cloudwatch_log_group.lambda, aws_iam_role_policy_attachment.lambda_basic_execution, aws_iam_role_policy_attachment.lambda_vpc_access]
}

# --- State machines ---

data "aws_iam_policy_document" "states_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["states.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "states" {
  name               = "${var.name_prefix}-pipeline-states"
  assume_role_policy = data.aws_iam_policy_document.states_assume.json
}

# What batch:submitJob.sync needs, per the Step Functions + AWS Batch docs:
# SubmitJob (scoped to this pipeline's queues and job definitions), and
# DescribeJobs/TerminateJob on "*" since job IDs only exist at runtime.
# .sync waits on Batch's job state-change events through a rule Step
# Functions manages itself, StepFunctionsGetEventsForBatchJobsRule.
data "aws_iam_policy_document" "states" {
  statement {
    sid     = "InvokeLambdas"
    actions = ["lambda:InvokeFunction"]
    resources = flatten([
      for arn in [aws_lambda_function.pipeline.arn, aws_lambda_function.stateless.arn, aws_lambda_function.malware_scan.arn] :
      [arn, "${arn}:*"]
    ])
  }
  statement {
    sid     = "SubmitBatchJobs"
    actions = ["batch:SubmitJob"]
    resources = concat(
      distinct([var.batch.embed_job_queue_arn, var.batch.layout_job_queue_arn, var.batch.entity_job_queue_arn]),
      [for name in distinct([var.batch.embed_job_definition_name, var.batch.layout_job_definition_name, var.batch.entity_job_definition_name]) :
      "arn:aws:batch:${var.aws_region}:${var.account_id}:job-definition/${name}:*"],
      [for name in distinct([var.batch.embed_job_definition_name, var.batch.layout_job_definition_name, var.batch.entity_job_definition_name]) :
      "arn:aws:batch:${var.aws_region}:${var.account_id}:job-definition/${name}"],
    )
  }
  statement {
    sid       = "TrackBatchJobs"
    actions   = ["batch:DescribeJobs", "batch:TerminateJob"]
    resources = ["*"]
  }
  statement {
    sid       = "BatchJobEventsRule"
    actions   = ["events:PutTargets", "events:PutRule", "events:DescribeRule"]
    resources = ["arn:aws:events:${var.aws_region}:${var.account_id}:rule/StepFunctionsGetEventsForBatchJobsRule"]
  }
}

resource "aws_iam_role_policy" "states" {
  name   = "${var.name_prefix}-pipeline-states"
  role   = aws_iam_role.states.id
  policy = data.aws_iam_policy_document.states.json
}

resource "aws_sfn_state_machine" "this" {
  for_each = local.state_machines

  name     = each.value.name
  type     = "STANDARD"
  role_arn = aws_iam_role.states.arn
  # The pipeline Lambda unless the state machine's vars name another.
  definition = templatefile(each.value.definition, merge({ lambda_arn = local.function_arn }, each.value.vars))

  depends_on = [aws_iam_role_policy.states]
}
