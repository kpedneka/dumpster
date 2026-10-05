# cmd/lambda-stateless-jobs: edge extraction and canonicalization, pure Go
# computation with no Batch hop, triggered by their SQS queues through
# event source mappings. internal/worker/lambda.Dispatcher records each
# job's progress in document_job_status.

locals {
  stateless_function_name = "${var.name_prefix}-stateless-jobs"
  # A DB write plus, for canonicalization, a couple of synchronous LLM
  # calls (alias judge, cross-link).
  stateless_timeout_seconds = 60
}

resource "aws_cloudwatch_log_group" "stateless" {
  name              = "/aws/lambda/${local.stateless_function_name}"
  retention_in_days = var.log_retention_days
}

resource "aws_iam_role" "stateless" {
  name               = "${var.name_prefix}-lambda-stateless-jobs"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume.json
}

resource "aws_iam_role_policy_attachment" "stateless_basic_execution" {
  role       = aws_iam_role.stateless.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

data "aws_iam_policy_document" "stateless" {
  statement {
    sid       = "ConsumeQueues"
    actions   = ["sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:GetQueueAttributes"]
    resources = [for q in aws_sqs_queue.job : q.arn]
  }

  dynamic "statement" {
    for_each = length(var.bedrock_model_arns) > 0 ? [1] : []
    content {
      sid = "InvokeBedrock"
      actions = [
        "bedrock:InvokeModel",
        "bedrock:InvokeModelWithResponseStream",
        "bedrock:Converse",
        "bedrock:ConverseStream",
      ]
      resources = var.bedrock_model_arns
    }
  }
}

resource "aws_iam_role_policy" "stateless" {
  name   = "${var.name_prefix}-lambda-stateless-jobs"
  role   = aws_iam_role.stateless.id
  policy = data.aws_iam_policy_document.stateless.json
}

resource "aws_lambda_function" "stateless" {
  function_name    = local.stateless_function_name
  role             = aws_iam_role.stateless.arn
  handler          = "bootstrap"
  runtime          = "provided.al2023"
  architectures    = ["arm64"]
  filename         = var.stateless_lambda_zip_path
  source_code_hash = var.stateless_lambda_zip_hash
  timeout          = local.stateless_timeout_seconds
  memory_size      = 512

  environment {
    # AWS_REGION is reserved: Lambda sets it, and setting it here fails
    # CreateFunction.
    variables = var.stateless_lambda_environment
  }

  depends_on = [aws_cloudwatch_log_group.stateless, aws_iam_role_policy_attachment.stateless_basic_execution]
}

# ReportBatchItemFailures matches the dispatcher's per-record
# BatchItemFailures, so one failed record doesn't redeliver the whole batch.
resource "aws_lambda_event_source_mapping" "stateless" {
  for_each                = aws_sqs_queue.job
  event_source_arn        = each.value.arn
  function_name           = aws_lambda_function.stateless.arn
  function_response_types = ["ReportBatchItemFailures"]
}
