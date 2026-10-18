# cmd/lambda-stateless-jobs: edge extraction and canonicalization, pure Go
# computation with no Batch hop. Each job type's state machine (main.tf)
# invokes it; internal/worker/lambda.StepHandler records each job's
# progress in document_job_status.

locals {
  stateless_function_name = "${var.name_prefix}-stateless-jobs"
  stateless_function_arn  = "arn:aws:lambda:${var.aws_region}:${var.account_id}:function:${local.stateless_function_name}"
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

# Creating and removing the network interfaces a Lambda in a VPC uses.
resource "aws_iam_role_policy_attachment" "stateless_vpc_access" {
  count      = var.vpc_config == null ? 0 : 1
  role       = aws_iam_role.stateless.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaVPCAccessExecutionRole"
}

data "aws_iam_policy_document" "stateless" {
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


  dynamic "vpc_config" {
    for_each = var.vpc_config == null ? [] : [var.vpc_config]
    content {
      subnet_ids         = vpc_config.value.subnet_ids
      security_group_ids = vpc_config.value.security_group_ids
    }
  }

  depends_on = [aws_cloudwatch_log_group.stateless, aws_iam_role_policy_attachment.stateless_basic_execution, aws_iam_role_policy_attachment.stateless_vpc_access]
}
