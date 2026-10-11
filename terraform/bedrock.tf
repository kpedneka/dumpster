# A dedicated, tagged Bedrock application inference profile per
# environment -- not the bare system-defined cross-region profile
# (us.anthropic.claude-sonnet-5) every account gets automatically, but our
# own ARN that wraps it. AWS Cost Explorer and Budgets attribute spend by
# which ARN was invoked, and the system-defined profile is shared
# account-wide. Invoking through a profile tagged with this environment
# lets production's spend budget (bedrock_budget.tf) count production only,
# so staging, the dev stack (terraform/dev) and local runs can never trip
# production's Bedrock deny.
resource "aws_bedrock_inference_profile" "app" {
  name        = local.name_prefix
  description = "dumpster application Claude Sonnet 5 tagged for cost tracking"

  model_source {
    copy_from = "arn:aws:bedrock:${var.aws_region}:${data.aws_caller_identity.current.account_id}:inference-profile/us.anthropic.claude-sonnet-5"
  }

  tags = {
    environment = var.environment
    app         = "dumpster"
  }
}

# The value passed as BEDROCK_MODEL_ID to the API and the stateless-jobs
# Lambda.
locals {
  bedrock_model_id = aws_bedrock_inference_profile.app.arn
}
