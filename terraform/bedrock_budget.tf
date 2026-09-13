# Card #22: caps production's real AWS Bedrock spend at ~$20/month and
# makes the app degrade gracefully once that's hit, rather than an
# unbounded bill -- explicit go-live gate for the DNS/TLS cutover (#20),
# driven by a real incident (a load-testing burst burned real
# non-refundable Anthropic credit in under an hour with zero spend
# control between the app and the bill). Production-only: staging isn't
# on Bedrock at all yet (ecs_api.tf's local.llm_provider), so there's
# nothing to budget there until that changes.
#
# Real, honest caveat this design can't engineer around: AWS Cost
# Explorer/Budgets data has an inherent ~24-48h reporting lag. This is a
# lagging safety net against a runaway bill, not real-time, per-request
# rate limiting -- a true hard cap on individual requests would need to
# live in the app itself (out of scope here; the app-side change this
# card also makes is limited to recognizing a budget-triggered deny and
# degrading gracefully once AWS actually applies it, not preventing the
# spend that happens before the lag catches up).
#
# Filtered by Service (Amazon Bedrock), not the tagged application
# inference profile from bedrock.tf, even though that profile exists
# specifically for cost separation: tag-based Cost Explorer/Budgets
# filtering requires activating cost allocation tags first, which has its
# own propagation delay on top of the existing reporting lag, and this is
# a single-project personal AWS account with no other Bedrock consumer --
# "all Bedrock spend in this account" and "this app's production Bedrock
# spend" are the same number today. The tagged profile stays valuable for
# Cost Explorer breakdown/reporting regardless, and if a second Bedrock
# consumer (e.g. staging, later) ever shares this account, switching this
# filter to tag-based is the natural next step at that point, not now.

locals {
  bedrock_budget_amount = "20"
}

# A real, real-world limitation of Budget Actions worth being explicit
# about even though it can't be worked around here: the ECS task role is
# shared across api/worker/cleanup (ecs_iam.tf), so a triggered deny
# blocks Bedrock for the whole task role, not just the api container's
# search-answering path. Scoped tightly to only Bedrock actions, though --
# never broader -- a budget overrun must never cascade into breaking S3,
# Secrets Manager, or any other AWS call this role makes.
data "aws_iam_policy_document" "bedrock_deny" {
  count = var.environment == "production" ? 1 : 0

  statement {
    effect = "Deny"
    actions = [
      "bedrock:InvokeModel",
      "bedrock:InvokeModelWithResponseStream",
      "bedrock:Converse",
      "bedrock:ConverseStream",
    ]
    resources = ["*"]
  }
}

resource "aws_iam_policy" "bedrock_deny" {
  count       = var.environment == "production" ? 1 : 0
  name        = "${local.name_prefix}-bedrock-budget-deny"
  description = "Attached to the ECS task role automatically by the Bedrock spend budget action once the monthly limit is hit -- see bedrock_budget.tf."
  policy      = data.aws_iam_policy_document.bedrock_deny[0].json
}

# AWS Budgets assumes this role to actually execute the action (attach/
# detach the deny policy above) -- scoped narrowly to exactly that one
# policy and exactly the ECS task role, not broad IAM administration.
data "aws_iam_policy_document" "budgets_action_assume" {
  count = var.environment == "production" ? 1 : 0

  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["budgets.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "budgets_action" {
  count              = var.environment == "production" ? 1 : 0
  name               = "${local.name_prefix}-budgets-bedrock-action"
  assume_role_policy = data.aws_iam_policy_document.budgets_action_assume[0].json
}

data "aws_iam_policy_document" "budgets_action" {
  count = var.environment == "production" ? 1 : 0

  statement {
    actions = [
      "iam:AttachRolePolicy",
      "iam:DetachRolePolicy",
      "iam:ListAttachedRolePolicies",
    ]
    resources = [aws_iam_role.ecs_task.arn]
  }

  statement {
    actions   = ["iam:GetPolicy", "iam:GetPolicyVersion"]
    resources = [aws_iam_policy.bedrock_deny[0].arn]
  }
}

resource "aws_iam_role_policy" "budgets_action" {
  count  = var.environment == "production" ? 1 : 0
  name   = "${local.name_prefix}-budgets-bedrock-action"
  role   = aws_iam_role.budgets_action[0].id
  policy = data.aws_iam_policy_document.budgets_action[0].json
}

resource "aws_budgets_budget" "bedrock" {
  count = var.environment == "production" ? 1 : 0

  name         = "${local.name_prefix}-bedrock-monthly"
  budget_type  = "COST"
  limit_amount = local.bedrock_budget_amount
  limit_unit   = "USD"
  time_unit    = "MONTHLY"

  cost_filter {
    name   = "Service"
    values = ["Amazon Bedrock"]
  }

  # Awareness alerts, independent of the hard-stop action below -- the
  # user finds out at 80% (a warning, still time to react manually) and
  # again at 100% (the same moment the deny action fires), not only by
  # noticing the app started degrading.
  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 80
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = ["kpednekar95@gmail.com"]
  }

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 100
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = ["kpednekar95@gmail.com"]
  }
}

# The actual hard stop: at 100% of the budget, automatically (no manual
# approval step -- AUTOMATIC, matching "the system should gracefully
# handle this" rather than waiting on a human to click approve)
# attaches the deny policy to the ECS task role.
resource "aws_budgets_budget_action" "bedrock_deny" {
  count = var.environment == "production" ? 1 : 0

  budget_name        = aws_budgets_budget.bedrock[0].name
  action_type        = "APPLY_IAM_POLICY"
  approval_model     = "AUTOMATIC"
  notification_type  = "ACTUAL"
  execution_role_arn = aws_iam_role.budgets_action[0].arn

  action_threshold {
    action_threshold_type  = "PERCENTAGE"
    action_threshold_value = 100
  }

  definition {
    iam_action_definition {
      policy_arn = aws_iam_policy.bedrock_deny[0].arn
      roles      = [aws_iam_role.ecs_task.name]
    }
  }

  subscriber {
    address           = "kpednekar95@gmail.com"
    subscription_type = "EMAIL"
  }
}
