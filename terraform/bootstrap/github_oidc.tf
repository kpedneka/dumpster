# GitHub Actions -> AWS auth for CI, via OIDC -- no static AWS keys ever
# live in GitHub, matching the secrets architecture already decided (see
# ecs_secrets.tf's header: "GitHub holds only the OIDC deploy role, AWS
# Secrets Manager holds everything the app needs to run"). This is that
# role, and the account-wide trust anchor it depends on -- neither existed
# before this (confirmed via `aws iam list-open-id-connect-providers`,
# empty), needed for the first time by the "design the staging spin-up/
# test/tear-down workflow" dev board card's actual CI job.
#
# A deliberately separate root module/state from ../*.tf, not folded into
# the same directory the staging/production workflows apply -- two real
# reasons, not just tidiness. First, this resource set is account-global
# (one OIDC provider, one deploy role for the whole repo), while every
# other resource in this config is workspace/environment-scoped
# (staging vs production, applied separately) -- there's no environment
# for "the thing that lets CI authenticate" to belong to. Second, and
# more important: the deploy role IAM policy below grants iam:PutRolePolicy
# etc. on every dumpster-* role -- which would include this role's own
# policy if it were ever included in a routine `tofu apply` that role
# itself runs, a real self-privilege-escalation path if a CI job (however
# unlikely, from a repo-scoped OIDC trust condition) ever pointed at this
# directory instead of ../. Keeping it a separate root module makes that
# structurally impossible, not just discouraged by convention: CI's own
# apply commands never `cd` here. Apply this directory once, by hand,
# with your own admin credentials -- not part of any automated workflow.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }
  }
}

provider "aws" {
  region = var.aws_region
}

# Fetched, not hand-typed: a wrong hex thumbprint is exactly the kind of
# typo that's invisible until it silently breaks (or, worse, silently
# doesn't break anything meaningful since AWS ignores this value anyway --
# see the resource below) -- fetching it from the actual issuer removes
# the chance of getting it wrong at all.
data "tls_certificate" "github_actions" {
  url = "https://token.actions.githubusercontent.com"
}

variable "aws_region" {
  type        = string
  description = "AWS region this account's resources run in -- matches ../ecs_iam.tf's variable of the same name."
  default     = "us-east-1"
}

data "aws_caller_identity" "current" {}

resource "aws_iam_openid_connect_provider" "github_actions" {
  url            = "https://token.actions.githubusercontent.com"
  client_id_list = ["sts.amazonaws.com"]
  # AWS now validates GitHub's OIDC tokens against its own trusted CA
  # bundle regardless of this value (the thumbprint_list argument is
  # effectively vestigial post-2023, per AWS's own provider docs) --
  # still required to be non-empty for the resource to apply.
  thumbprint_list = [data.tls_certificate.github_actions.certificates[0].sha1_fingerprint]
}

# repo:kpedneka/dumpster:* -- every trigger type (pull_request, push to
# main) from this one repo, nothing else. GitHub's actual `sub` claim
# format differs by trigger (pull_request-triggered runs send
# "repo:OWNER/REPO:pull_request", push-triggered runs send
# "repo:OWNER/REPO:ref:refs/heads/<branch>") -- a single wildcard is
# simpler than enumerating both formats and already covers the CD-on-
# push-to-main workflow this same card still needs to build, not just
# today's staging workflow.
data "aws_iam_policy_document" "github_actions_assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.github_actions.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }
    condition {
      test     = "StringLike"
      variable = "token.actions.githubusercontent.com:sub"
      values   = ["repo:kpedneka/dumpster:*"]
    }
  }
}

resource "aws_iam_role" "github_actions_deploy" {
  name               = "dumpster-github-actions-deploy"
  assume_role_policy = data.aws_iam_policy_document.github_actions_assume.json
}

# Scoped to what a real `tofu apply`/`destroy` of this directory's config
# touches. Resource-scoped by the dumpster-* naming convention every
# resource in this config already follows, wherever the service actually
# supports resource-level IAM (S3, IAM roles/instance profiles,
# Secrets Manager, ECR, CloudWatch Logs/alarms). ECS, EC2, Batch, ELB,
# Application Auto Scaling, and EventBridge Scheduler get broader,
# service-level grants instead -- not full least-privilege, a deliberate
# tradeoff: those services either don't support meaningful resource-level
# scoping for the create/update/delete actions Terraform needs (EC2's
# networking actions, most Batch and ECS calls), or Terraform's own
# read-then-diff cycle needs broad Describe*/List* access anyway. Same
# "accept simpler tradeoffs at personal scale" posture as the NAT
# instance and ALB decisions elsewhere in this config -- this is a
# single-project AWS account, not a shared one, so the blast radius of
# this role over-reaching is contained to this project's own resources
# regardless.
data "aws_iam_policy_document" "github_actions_deploy" {
  statement {
    sid = "BroadServiceAccess"
    actions = [
      "ecs:*",
      "ec2:*",
      "batch:*",
      "elasticloadbalancing:*",
      "application-autoscaling:*",
      "scheduler:*",
      "acm:DescribeCertificate",
      "acm:ListCertificates",
      "acm:GetCertificate",
      # data.aws_acm_certificate (ecs_alb.tf) reads a certificate's tags
      # as part of its normal lookup, not just its metadata -- missing
      # this was a real gap the deploy role's first actual `tofu plan`
      # run surfaced (AccessDenied on ListTagsForCertificate), not
      # something caught by validation ahead of time.
      "acm:ListTagsForCertificate",
      "sts:GetCallerIdentity",
    ]
    resources = ["*"]
  }

  # data.aws_ssm_parameter.al2023_arm64 (ecs_networking.tf) resolves the
  # NAT instance's AMI ID via AWS's own public parameter path -- owned by
  # Amazon, not this account (no account ID in the ARN), so this is a
  # read against a public, shared resource, not a scoping gap in this
  # project's own naming convention. Also only surfaced by a real first
  # `tofu plan` run against the deploy role, not anticipated ahead of time.
  statement {
    sid       = "SSMPublicParameters"
    actions   = ["ssm:GetParameter", "ssm:GetParameters"]
    resources = ["arn:aws:ssm:${var.aws_region}::parameter/aws/service/ami-amazon-linux-latest/*"]
  }

  # CloudWatch alarm ARNs use a colon before the alarm name (alarm:name),
  # not a slash the way S3/IAM/ECR resource paths do -- this statement's
  # resources pattern originally used the wrong separator (alarm/dumpster-*),
  # so it silently matched nothing at all until a real plan run's state
  # refresh (cloudwatch:ListTagsForResource) surfaced it as an
  # AccessDenied, not caught by validation or review.
  statement {
    sid       = "CloudWatch"
    actions   = ["cloudwatch:*"]
    resources = ["arn:aws:cloudwatch:${var.aws_region}:${data.aws_caller_identity.current.account_id}:alarm:dumpster-*"]
  }

  # cloudwatch:PutMetricData/ListMetrics/GetMetricData and friends have no
  # resource-level scoping at all (always "*") -- split from the alarm
  # statement above, which does support it, rather than widening that
  # one's resources to "*" too.
  statement {
    sid       = "CloudWatchMetrics"
    actions   = ["cloudwatch:PutMetricData", "cloudwatch:ListMetrics", "cloudwatch:GetMetricData", "cloudwatch:DescribeAlarms"]
    resources = ["*"]
  }

  statement {
    sid = "Logs"
    actions = [
      "logs:CreateLogGroup",
      "logs:DeleteLogGroup",
      "logs:PutRetentionPolicy",
      # Both tag API generations granted -- the provider version pinned
      # here isn't confirmed to use one over the other, and getting this
      # wrong the same way the CloudWatch alarm ARN separator was wrong
      # costs another full CI round trip to discover.
      "logs:TagResource",
      "logs:UntagResource",
      "logs:ListTagsForResource",
      "logs:TagLogGroup",
      "logs:UntagLogGroup",
      "logs:ListTagsLogGroup",
    ]
    resources = ["arn:aws:logs:${var.aws_region}:${data.aws_caller_identity.current.account_id}:log-group:/ecs/dumpster-*"]
  }

  # DescribeLogGroups is a list-style call (optionally filtered by name
  # prefix), not a lookup against one resource's ARN -- AWS doesn't
  # accept it scoped to a specific log-group ARN the way the create/
  # delete/tag actions above are (found by a real plan run refreshing
  # this resource's state, not anticipated ahead of time -- same story
  # as the SQS statement below).
  statement {
    sid       = "LogsDescribe"
    actions   = ["logs:DescribeLogGroups"]
    resources = ["*"]
  }

  # Missed entirely in the original policy -- ecs_cleanup.tf's
  # aws_sqs_queue.cleanup_dlq has no service statement at all until this,
  # caught only by a real plan run trying to refresh it.
  statement {
    sid = "SQS"
    actions = [
      "sqs:CreateQueue",
      "sqs:DeleteQueue",
      "sqs:GetQueueAttributes",
      "sqs:GetQueueUrl",
      "sqs:SetQueueAttributes",
      "sqs:TagQueue",
      "sqs:UntagQueue",
      "sqs:ListQueueTags",
    ]
    resources = ["arn:aws:sqs:${var.aws_region}:${data.aws_caller_identity.current.account_id}:dumpster-*"]
  }

  statement {
    sid       = "S3"
    actions   = ["s3:*"]
    resources = ["arn:aws:s3:::dumpster-*"]
  }

  statement {
    sid       = "ECR"
    actions   = ["ecr:*"]
    resources = ["arn:aws:ecr:${var.aws_region}:${data.aws_caller_identity.current.account_id}:repository/dumpster-*"]
  }

  # GetAuthorizationToken (needed for `docker login`) is account-wide by
  # design -- AWS doesn't support resource-level scoping for it.
  statement {
    sid       = "ECRAuth"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }

  statement {
    sid = "SecretsManagerRead"
    actions = [
      "secretsmanager:DescribeSecret",
      "secretsmanager:ListSecrets",
      # data.aws_secretsmanager_secret's normal read includes the
      # secret's resource policy, not just its metadata -- same "only
      # found by a real plan run" story as the two grants above.
      "secretsmanager:GetResourcePolicy",
    ]
    resources = ["arn:aws:secretsmanager:${var.aws_region}:${data.aws_caller_identity.current.account_id}:secret:dumpster/*"]
  }

  # IAM: scoped to this project's own role/instance-profile naming
  # convention -- covers both the per-environment roles this config
  # creates (dumpster-staging-ecs-execution, etc.) and the shared,
  # pre-existing Batch instance role/profile batch.tf reads via data
  # source (dumpster-batch-ecs-instance-profile/-role), which also
  # matches the dumpster-* prefix.
  statement {
    sid = "IAMScoped"
    actions = [
      "iam:CreateRole",
      "iam:DeleteRole",
      "iam:GetRole",
      "iam:TagRole",
      "iam:PutRolePolicy",
      "iam:DeleteRolePolicy",
      "iam:GetRolePolicy",
      "iam:AttachRolePolicy",
      "iam:DetachRolePolicy",
      "iam:ListRolePolicies",
      "iam:ListAttachedRolePolicies",
      "iam:ListInstanceProfilesForRole",
      "iam:PassRole",
      "iam:GetInstanceProfile",
      "iam:GetOpenIDConnectProvider",
      "iam:UntagRole",
      # aws_iam_instance_profile.nat_ssm's own lifecycle (ecs_networking.tf)
      # -- missed in the original policy alongside GetInstanceProfile,
      # which only covers reading one, not managing it.
      "iam:CreateInstanceProfile",
      "iam:DeleteInstanceProfile",
      "iam:AddRoleToInstanceProfile",
      "iam:RemoveRoleFromInstanceProfile",
      "iam:TagInstanceProfile",
    ]
    resources = [
      "arn:aws:iam::${data.aws_caller_identity.current.account_id}:role/dumpster-*",
      "arn:aws:iam::${data.aws_caller_identity.current.account_id}:instance-profile/dumpster-*",
      "arn:aws:iam::${data.aws_caller_identity.current.account_id}:oidc-provider/token.actions.githubusercontent.com",
    ]
  }

  # IAM roles this deploy role must be able to *read and pass* but never
  # modify -- the account's default ecsTaskExecutionRole (batch.tf's
  # Fargate job definitions reference it as their executionRoleArn) and
  # the Batch service-linked role (batch.tf's compute environments pass
  # it as their service_role), neither of which follows the dumpster-*
  # naming convention and neither of which this Terraform config ever
  # creates or changes. PassRole is what was actually missing here --
  # GetRole alone covers reading a role's own definition, not handing it
  # to another AWS service on this role's behalf, which is what
  # RegisterJobDefinition/CreateComputeEnvironment need to do with these
  # two roles specifically (found by a real apply attempt, not
  # anticipated -- the dumpster-* PassRole grant on IAMScoped above only
  # ever covered roles this config itself manages).
  statement {
    sid     = "IAMReadOnlyExternal"
    actions = ["iam:GetRole", "iam:PassRole"]
    resources = [
      "arn:aws:iam::${data.aws_caller_identity.current.account_id}:role/ecsTaskExecutionRole",
      "arn:aws:iam::${data.aws_caller_identity.current.account_id}:role/aws-service-role/batch.amazonaws.com/AWSServiceRoleForBatch",
    ]
  }

  # Defense-in-depth against the self-escalation path the header comment
  # already explains: this role's own name (dumpster-github-actions-
  # deploy) matches the dumpster-* wildcard IAMScoped grants above, so
  # without this explicit deny, the role could in principle rewrite its
  # own trust policy or permissions. Structurally unreachable already
  # (this directory is never part of any automated apply -- see header),
  # but an explicit deny costs nothing and doesn't rely solely on that.
  statement {
    sid    = "DenySelfModification"
    effect = "Deny"
    actions = [
      "iam:PutRolePolicy",
      "iam:DeleteRolePolicy",
      "iam:DeleteRole",
      "iam:AttachRolePolicy",
      "iam:DetachRolePolicy",
      "iam:UpdateAssumeRolePolicy",
    ]
    resources = [aws_iam_role.github_actions_deploy.arn]
  }
}

resource "aws_iam_role_policy" "github_actions_deploy" {
  name   = "dumpster-github-actions-deploy"
  role   = aws_iam_role.github_actions_deploy.id
  policy = data.aws_iam_policy_document.github_actions_deploy.json
}

output "github_actions_deploy_role_arn" {
  value       = aws_iam_role.github_actions_deploy.arn
  description = "Set as the role-to-assume in the staging/CD workflows' aws-actions/configure-aws-credentials step."
}
