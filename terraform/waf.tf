# AWS WAF on the CloudFront distribution (frontend.tf): the inbound
# request filter in front of the app. CloudFront is the only public entry
# point (the ALB is internal, reached through a VPC origin), and the DNS
# records are DNS-only (dns.tf), so every request passes through this.
#
# Rules: AWS's IP reputation list, known-bad-inputs and common (OWASP-style)
# managed rule groups, plus a per-IP rate limit on /api/*. Document uploads
# are excluded from the common rule group: its body rules match arbitrary
# file content (a PDF that mentions "<script>" isn't an attack) and it
# rejects bodies over 8 KB. Uploads have their own controls: the size limit
# and the malware scan.
#
# var.waf_block = false (the default) runs every rule in count mode: WAF logs
# what it would block and blocks nothing, so legitimate traffic can be
# checked against the logs before switching to block.

variable "waf_block" {
  type        = bool
  default     = false
  description = "Whether the WAF blocks matching requests (true) or only counts them (false, for review)."
}

variable "waf_api_rate_limit" {
  type        = number
  default     = 2000
  description = "Requests to /api/* one IP may make in any 5-minute window before WAF acts on it. A coarse guard in front of the app's own rate limiter."
}

locals {
  waf_name = "${local.name_prefix}-edge"
}

resource "aws_wafv2_web_acl" "edge" {
  name  = local.waf_name
  scope = "CLOUDFRONT"

  default_action {
    allow {}
  }

  rule {
    name     = "aws-ip-reputation"
    priority = 10
    override_action {
      dynamic "none" {
        for_each = var.waf_block ? [1] : []
        content {}
      }
      dynamic "count" {
        for_each = var.waf_block ? [] : [1]
        content {}
      }
    }
    statement {
      managed_rule_group_statement {
        vendor_name = "AWS"
        name        = "AWSManagedRulesAmazonIpReputationList"
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${local.waf_name}-ip-reputation"
      sampled_requests_enabled   = true
    }
  }

  rule {
    name     = "aws-known-bad-inputs"
    priority = 20
    override_action {
      dynamic "none" {
        for_each = var.waf_block ? [1] : []
        content {}
      }
      dynamic "count" {
        for_each = var.waf_block ? [] : [1]
        content {}
      }
    }
    statement {
      managed_rule_group_statement {
        vendor_name = "AWS"
        name        = "AWSManagedRulesKnownBadInputsRuleSet"
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${local.waf_name}-known-bad-inputs"
      sampled_requests_enabled   = true
    }
  }

  rule {
    name     = "aws-common"
    priority = 30
    override_action {
      dynamic "none" {
        for_each = var.waf_block ? [1] : []
        content {}
      }
      dynamic "count" {
        for_each = var.waf_block ? [] : [1]
        content {}
      }
    }
    statement {
      managed_rule_group_statement {
        vendor_name = "AWS"
        name        = "AWSManagedRulesCommonRuleSet"

        # Everything except document uploads (POST /api/kbs/{id}/documents).
        scope_down_statement {
          not_statement {
            statement {
              and_statement {
                statement {
                  byte_match_statement {
                    search_string         = "POST"
                    positional_constraint = "EXACTLY"
                    field_to_match {
                      method {}
                    }
                    text_transformation {
                      priority = 0
                      type     = "NONE"
                    }
                  }
                }
                statement {
                  regex_match_statement {
                    regex_string = "^/api/kbs/[^/]+/documents$"
                    field_to_match {
                      uri_path {}
                    }
                    text_transformation {
                      priority = 0
                      type     = "NONE"
                    }
                  }
                }
              }
            }
          }
        }
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${local.waf_name}-common"
      sampled_requests_enabled   = true
    }
  }

  rule {
    name     = "api-rate-limit"
    priority = 40
    action {
      dynamic "block" {
        for_each = var.waf_block ? [1] : []
        content {}
      }
      dynamic "count" {
        for_each = var.waf_block ? [] : [1]
        content {}
      }
    }
    statement {
      rate_based_statement {
        limit                 = var.waf_api_rate_limit
        evaluation_window_sec = 300
        aggregate_key_type    = "IP"
        scope_down_statement {
          byte_match_statement {
            search_string         = "/api/"
            positional_constraint = "STARTS_WITH"
            field_to_match {
              uri_path {}
            }
            text_transformation {
              priority = 0
              type     = "NONE"
            }
          }
        }
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${local.waf_name}-api-rate-limit"
      sampled_requests_enabled   = true
    }
  }

  visibility_config {
    cloudwatch_metrics_enabled = true
    metric_name                = local.waf_name
    sampled_requests_enabled   = true
  }
}

# --- Logs: every request WAF evaluated, with the rule that matched ---
#
# WAF requires the log group name to start with aws-waf-logs-. Kept 30 days:
# enough to review count mode before switching to block.

resource "aws_cloudwatch_log_group" "waf" {
  name              = "aws-waf-logs-${local.name_prefix}-edge"
  retention_in_days = 30
}

data "aws_iam_policy_document" "waf_logs" {
  statement {
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["${aws_cloudwatch_log_group.waf.arn}:*"]
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
      test     = "ArnLike"
      variable = "aws:SourceArn"
      values   = ["arn:aws:logs:${var.aws_region}:${data.aws_caller_identity.current.account_id}:*"]
    }
  }
}

resource "aws_cloudwatch_log_resource_policy" "waf" {
  policy_name     = "${local.name_prefix}-waf-logs"
  policy_document = data.aws_iam_policy_document.waf_logs.json
}

resource "aws_wafv2_web_acl_logging_configuration" "edge" {
  resource_arn            = aws_wafv2_web_acl.edge.arn
  log_destination_configs = [aws_cloudwatch_log_group.waf.arn]

  depends_on = [aws_cloudwatch_log_resource_policy.waf]
}
