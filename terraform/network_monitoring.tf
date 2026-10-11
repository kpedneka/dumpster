# Network and account monitoring: what the app reaches, what it looks up,
# and alerts on suspicious activity. Detective controls only -- nothing here
# blocks traffic. The DNS query logs are also the evidence the egress
# allowlist (DNS Firewall) is built from, so the allowlist reflects real
# traffic rather than a guess.
#
# Staging and production share the account's default VPC (ecs_networking.tf)
# and GuardDuty is one detector per account and region, so all of this is
# account- or VPC-level, not per environment. Only the production workspace
# manages it (like bedrock_budget.tf's cost allocation tag); staging's
# applies and teardowns never touch it, and it covers staging's traffic too.

locals {
  monitoring_count   = var.environment == "production" ? 1 : 0
  network_logs_alert = "kpednekar95@gmail.com"
}

# --- Log bucket: VPC Flow Logs and Route 53 DNS query logs ---

resource "aws_s3_bucket" "network_logs" {
  count  = local.monitoring_count
  bucket = "dumpster-network-logs-${data.aws_caller_identity.current.account_id}"
}

resource "aws_s3_bucket_public_access_block" "network_logs" {
  count                   = local.monitoring_count
  bucket                  = aws_s3_bucket.network_logs[0].id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "network_logs" {
  count  = local.monitoring_count
  bucket = aws_s3_bucket.network_logs[0].id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# 90 days: long enough to investigate a GuardDuty finding (which keeps its
# own findings for 90 days) and to review traffic before tightening egress.
resource "aws_s3_bucket_lifecycle_configuration" "network_logs" {
  count  = local.monitoring_count
  bucket = aws_s3_bucket.network_logs[0].id
  rule {
    id     = "expire-network-logs"
    status = "Enabled"
    filter {}
    expiration {
      days = 90
    }
  }
}

# Both log types are delivered by the AWS log delivery service, into
# AWSLogs/<account>/... under this bucket.
data "aws_iam_policy_document" "network_logs" {
  count = local.monitoring_count

  statement {
    sid       = "LogDeliveryWrite"
    actions   = ["s3:PutObject"]
    resources = ["${aws_s3_bucket.network_logs[0].arn}/AWSLogs/${data.aws_caller_identity.current.account_id}/*"]
    principals {
      type        = "Service"
      identifiers = ["delivery.logs.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
    condition {
      test     = "StringEquals"
      variable = "s3:x-amz-acl"
      values   = ["bucket-owner-full-control"]
    }
  }
  statement {
    sid       = "LogDeliveryAclCheck"
    actions   = ["s3:GetBucketAcl"]
    resources = [aws_s3_bucket.network_logs[0].arn]
    principals {
      type        = "Service"
      identifiers = ["delivery.logs.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
  }
}

resource "aws_s3_bucket_policy" "network_logs" {
  count  = local.monitoring_count
  bucket = aws_s3_bucket.network_logs[0].id
  policy = data.aws_iam_policy_document.network_logs[0].json
}

# --- VPC Flow Logs: every connection in the VPC, accepted or rejected ---

resource "aws_flow_log" "vpc" {
  count                    = local.monitoring_count
  vpc_id                   = data.aws_vpc.default.id
  traffic_type             = "ALL"
  log_destination_type     = "s3"
  log_destination          = aws_s3_bucket.network_logs[0].arn
  max_aggregation_interval = 600

  destination_options {
    file_format        = "parquet"
    per_hour_partition = true
  }

  depends_on = [aws_s3_bucket_policy.network_logs]
}

# --- Route 53 DNS query logs: every domain the VPC looks up ---

resource "aws_route53_resolver_query_log_config" "vpc" {
  count           = local.monitoring_count
  name            = "dumpster-vpc-dns-queries"
  destination_arn = aws_s3_bucket.network_logs[0].arn

  depends_on = [aws_s3_bucket_policy.network_logs]
}

resource "aws_route53_resolver_query_log_config_association" "vpc" {
  count                        = local.monitoring_count
  resolver_query_log_config_id = aws_route53_resolver_query_log_config.vpc[0].id
  resource_id                  = data.aws_vpc.default.id
}

# --- GuardDuty threat detection ---
#
# Analyses the account's CloudTrail events, VPC flow logs and DNS logs for
# threats (misused credentials, contact with known-malicious hosts, crypto
# mining). Separate from GuardDuty Malware Protection for S3, which scans
# uploads (modules/pipeline/malware_scan.tf) and needs no detector.

resource "aws_guardduty_detector" "account" {
  count                        = local.monitoring_count
  enable                       = true
  finding_publishing_frequency = "FIFTEEN_MINUTES"
}

# Protection plans that match what the account runs: S3 data access
# (uploads, scratch), Lambda network activity, and scanning an EC2 disk for
# malware when a finding involves it (the NAT instance, Batch's GPU hosts).
# No EKS, RDS or runtime agent, so those stay off.
locals {
  guardduty_features = {
    S3_DATA_EVENTS         = "ENABLED"
    LAMBDA_NETWORK_LOGS    = "ENABLED"
    EBS_MALWARE_PROTECTION = "ENABLED"
    EKS_AUDIT_LOGS         = "DISABLED"
    RDS_LOGIN_EVENTS       = "DISABLED"
    RUNTIME_MONITORING     = "DISABLED"
  }
}

resource "aws_guardduty_detector_feature" "account" {
  for_each    = var.environment == "production" ? local.guardduty_features : {}
  detector_id = aws_guardduty_detector.account[0].id
  name        = each.key
  status      = each.value
}

# --- Alerts: email high-severity findings ---
#
# GuardDuty's console holds every finding; this sends the ones worth acting
# on now (severity 7+, "High") to email, so they aren't only visible to
# someone who happens to look. The subscription must be confirmed from the
# email AWS sends.

resource "aws_sns_topic" "security_alerts" {
  count = local.monitoring_count
  name  = "dumpster-security-alerts"
}

data "aws_iam_policy_document" "security_alerts" {
  count = local.monitoring_count
  statement {
    actions   = ["sns:Publish"]
    resources = [aws_sns_topic.security_alerts[0].arn]
    principals {
      type        = "Service"
      identifiers = ["events.amazonaws.com"]
    }
    condition {
      test     = "ArnEquals"
      variable = "aws:SourceArn"
      values   = [aws_cloudwatch_event_rule.guardduty_high[0].arn]
    }
  }
}

resource "aws_sns_topic_policy" "security_alerts" {
  count  = local.monitoring_count
  arn    = aws_sns_topic.security_alerts[0].arn
  policy = data.aws_iam_policy_document.security_alerts[0].json
}

resource "aws_sns_topic_subscription" "security_alerts_email" {
  count     = local.monitoring_count
  topic_arn = aws_sns_topic.security_alerts[0].arn
  protocol  = "email"
  endpoint  = local.network_logs_alert
}

resource "aws_cloudwatch_event_rule" "guardduty_high" {
  count       = local.monitoring_count
  name        = "dumpster-guardduty-high-severity"
  description = "GuardDuty findings of severity 7 or higher."
  event_pattern = jsonencode({
    source        = ["aws.guardduty"]
    "detail-type" = ["GuardDuty Finding"]
    detail = {
      severity = [{ numeric = [">=", 7] }]
    }
  })
}

resource "aws_cloudwatch_event_target" "guardduty_high" {
  count = local.monitoring_count
  rule  = aws_cloudwatch_event_rule.guardduty_high[0].name
  arn   = aws_sns_topic.security_alerts[0].arn
}

output "network_logs_bucket" {
  value       = local.monitoring_count == 1 ? aws_s3_bucket.network_logs[0].bucket : null
  description = "Where VPC Flow Logs and Route 53 DNS query logs are delivered (production workspace only)."
}
