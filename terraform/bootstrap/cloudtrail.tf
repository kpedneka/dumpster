# A real CloudTrail trail, not just the account's free 90-day Event
# History -- IAM Access Analyzer's policy-generation feature (see this
# module's github_oidc.tf for the deploy role it's cross-checking) requires
# an actual Trail delivering to S3 plus a dedicated service role to read
# it; the free Event History doesn't satisfy that API at all (confirmed
# directly: `start-policy-generation` errors "Missing cloudTrailDetails"
# without one, and no trail existed in this account before this file).
#
# Single-region (us-east-1), not multi-region: every resource this account
# manages already runs in us-east-1 only -- matches this config's existing
# "accept simpler tradeoffs at personal scale" posture elsewhere (the NAT
# instance, the ALB). Account-level, so it lives in this bootstrap module
# alongside the OIDC role and the unused-access analyzer below, not the
# per-environment ../*.tf config.

resource "aws_s3_bucket" "cloudtrail" {
  bucket = "dumpster-cloudtrail-${data.aws_caller_identity.current.account_id}"
}

resource "aws_s3_bucket_public_access_block" "cloudtrail" {
  bucket = aws_s3_bucket.cloudtrail.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# The two statements CloudTrail itself needs on its own destination bucket
# -- GetBucketAcl to verify write access before logging starts, PutObject
# scoped to the AWSLogs/<account-id>/ prefix CloudTrail always writes under,
# with the bucket-owner-full-control condition AWS's own trail-creation
# docs require (otherwise the account that owns the bucket doesn't actually
# own the delivered log objects).
data "aws_iam_policy_document" "cloudtrail_bucket" {
  statement {
    sid = "AWSCloudTrailAclCheck"
    principals {
      type        = "Service"
      identifiers = ["cloudtrail.amazonaws.com"]
    }
    actions   = ["s3:GetBucketAcl"]
    resources = [aws_s3_bucket.cloudtrail.arn]
  }

  statement {
    sid = "AWSCloudTrailWrite"
    principals {
      type        = "Service"
      identifiers = ["cloudtrail.amazonaws.com"]
    }
    actions   = ["s3:PutObject"]
    resources = ["${aws_s3_bucket.cloudtrail.arn}/AWSLogs/${data.aws_caller_identity.current.account_id}/*"]
    condition {
      test     = "StringEquals"
      variable = "s3:x-amz-acl"
      values   = ["bucket-owner-full-control"]
    }
  }
}

resource "aws_s3_bucket_policy" "cloudtrail" {
  bucket = aws_s3_bucket.cloudtrail.id
  policy = data.aws_iam_policy_document.cloudtrail_bucket.json
}

resource "aws_cloudtrail" "main" {
  name                          = "dumpster-trail"
  s3_bucket_name                = aws_s3_bucket.cloudtrail.id
  include_global_service_events = true
  is_multi_region_trail         = false
  enable_logging                = true

  depends_on = [aws_s3_bucket_policy.cloudtrail]
}

# The service role Access Analyzer assumes to read this trail for policy
# generation -- exact permissions/trust policy shape is AWS's own
# documented one (IAM User Guide, "Permissions required to generate a
# policy"), not guessed: cloudtrail:GetTrail plus the last-accessed-details
# actions are account-wide (no resource-level scoping AWS supports for
# either), and S3 read is scoped to this specific trail bucket only.
data "aws_iam_policy_document" "access_analyzer_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["access-analyzer.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "access_analyzer_policy_gen" {
  name               = "dumpster-access-analyzer-policy-gen"
  assume_role_policy = data.aws_iam_policy_document.access_analyzer_assume.json
}

data "aws_iam_policy_document" "access_analyzer_policy_gen" {
  statement {
    actions   = ["cloudtrail:GetTrail"]
    resources = ["*"]
  }
  statement {
    actions = [
      "iam:GetServiceLastAccessedDetails",
      "iam:GenerateServiceLastAccessedDetails",
    ]
    resources = ["*"]
  }
  statement {
    actions = ["s3:GetObject", "s3:ListBucket"]
    resources = [
      aws_s3_bucket.cloudtrail.arn,
      "${aws_s3_bucket.cloudtrail.arn}/*",
    ]
  }
}

resource "aws_iam_role_policy" "access_analyzer_policy_gen" {
  name   = "dumpster-access-analyzer-policy-gen"
  role   = aws_iam_role.access_analyzer_policy_gen.id
  policy = data.aws_iam_policy_document.access_analyzer_policy_gen.json
}

output "cloudtrail_arn" {
  value       = aws_cloudtrail.main.arn
  description = "Pass to `aws accessanalyzer start-policy-generation`'s --cloud-trail-details trails[].cloudTrailArn."
}

output "access_analyzer_policy_gen_role_arn" {
  value       = aws_iam_role.access_analyzer_policy_gen.arn
  description = "Pass to `aws accessanalyzer start-policy-generation`'s --cloud-trail-details accessRole."
}
