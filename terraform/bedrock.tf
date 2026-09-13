# A dedicated, tagged Bedrock application inference profile for
# production -- not the bare system-defined cross-region profile
# (us.anthropic.claude-sonnet-5) every account gets automatically, but our
# own ARN that wraps it. The whole reason this exists: AWS Cost Explorer
# and Budgets attribute spend by which ARN was actually invoked, and the
# system-defined profile is shared account-wide with no way to separate
# "this app's production usage" from anything else that might ever call
# the same model in this account. An application inference profile is
# AWS's own documented mechanism for exactly this -- tag it, invoke
# through its ARN instead of the system one, and cost tracking (and the
# spend budget/safeguard this profile exists to support -- see the
# "Bedrock spend budget and graceful degradation" dev board card) can
# filter to it specifically.
#
# Production-only for now, not both environments: staging isn't on
# Bedrock at all yet (ecs_api.tf's local.llm_provider), so there's
# nothing to tag-separate there until that changes.
resource "aws_bedrock_inference_profile" "app" {
  count = var.environment == "production" ? 1 : 0

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

# The actual value passed as BEDROCK_MODEL_ID (ecs_api.tf's
# shared_environment) -- production invokes through its own tagged
# profile's ARN above; staging (which never sets LLM_PROVIDER=bedrock,
# so this value is never actually used there) falls back to
# var.bedrock_model_id's raw default, since aws_bedrock_inference_profile.app
# doesn't exist outside production to reference.
locals {
  bedrock_model_id = var.environment == "production" ? aws_bedrock_inference_profile.app[0].arn : var.bedrock_model_id
}
