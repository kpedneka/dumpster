# Object-storage lifecycle backstop for the R2 bucket.
#
# The account sweep hard-deletes every session's S3 objects before removing
# the session row, so under normal operation no objects outlive their session.
# This lifecycle rule is an independent infrastructure guardrail: if the sweep
# malfunctions, R2 will still expunge objects after 2 days rather than
# accumulating orphaned blobs indefinitely.
#
# Rule parameters
#   expiration.days = 2   — 48-hour ceiling on object age
#
# Apply once against the production account:
#   cd terraform
#   terraform init
#   terraform apply -var="cloudflare_account_id=<ACCOUNT_ID>"
#
# Requires the Cloudflare Terraform provider ≥ 4.x:
#   https://registry.terraform.io/providers/cloudflare/cloudflare/latest/docs/resources/r2_bucket_lifecycle_configuration

terraform {
  required_providers {
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 4.0"
    }
  }
}

variable "cloudflare_account_id" {
  type        = string
  description = "Cloudflare account ID (visible in the R2 dashboard URL)"
}

variable "r2_bucket_name" {
  type        = string
  description = "Name of the R2 bucket that stores user uploads"
  default     = "dumpster"
}

resource "cloudflare_r2_bucket_lifecycle_configuration" "orphan_backstop" {
  account_id  = var.cloudflare_account_id
  bucket_name = var.r2_bucket_name

  rules {
    id     = "orphan-object-backstop"
    status = "enabled"

    # Expire any object older than 48 hours. The sweep normally removes objects
    # within minutes of session expiry; this rule fires only if the sweep fails.
    expiration {
      days = 2
    }
  }
}
