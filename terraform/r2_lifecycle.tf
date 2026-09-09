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
# Applied with OpenTofu, not Terraform -- see the "Terraform for ECS
# services" dev board card (Fly -> AWS ECS Migration board) for why:
# HashiCorp's Business Source License doesn't restrict this project's
# actual use (managing our own infrastructure), but switching now, while
# only this file and one other exist, costs nothing -- OpenTofu is a
# drop-in CLI (same HCL, same provider ecosystem, same state format) and
# avoids the license question being worth re-litigating later once a lot
# more infrastructure code exists.
#
# Apply once against the production account:
#   cd terraform
#   tofu init
#   tofu apply -var="cloudflare_account_id=<ACCOUNT_ID>"
#
# Requires the Cloudflare provider's v5.x line specifically -- confirmed
# by inspecting the real installed v4.52.9 provider's schema (`tofu
# providers schema -json`), which exposes only a bare cloudflare_r2_bucket
# resource (account_id/name/location, no lifecycle support at all).
# cloudflare_r2_bucket_lifecycle doesn't exist before v5; this file
# originally referenced cloudflare_r2_bucket_lifecycle_configuration,
# which has never existed in any version -- a naming guess made by
# analogy to AWS's aws_s3_bucket_lifecycle_configuration that was never
# actually run against a real provider until it blocked a real `tofu
# plan`. v5's schema is also structurally different from the S3-style
# expiration{days=N} block guessed here originally -- see the rule shape
# below.
#   https://registry.terraform.io/providers/cloudflare/cloudflare/latest/docs/resources/r2_bucket_lifecycle

terraform {
  required_providers {
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 5.0"
    }
    # aws provider requirement lives here too, not duplicated per-file --
    # OpenTofu/Terraform allows exactly one required_providers block per
    # module, and every .tf file in this directory is one module. Used by
    # ecs_networking.tf and (once reactivated) aws_batch_confirm_step.tf.
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

variable "cloudflare_account_id" {
  type        = string
  description = "Cloudflare account ID (visible in the R2 dashboard URL, or as the subdomain segment of the R2 S3-compatible endpoint, https://<account_id>.r2.cloudflarestorage.com). Only meaningful in production -- this rule is production-gated below, so staging plans never need a real value here. Defaults to empty for exactly that reason."
  default     = ""

  validation {
    condition     = var.environment != "production" || length(var.cloudflare_account_id) > 0
    error_message = "cloudflare_account_id must be set when environment is \"production\"."
  }
}

variable "r2_bucket_name" {
  type        = string
  description = "Name of the R2 bucket that stores user uploads"
  default     = "dumpster"
}

resource "cloudflare_r2_bucket_lifecycle" "orphan_backstop" {
  # Production-only: this predates the staging/production split and has
  # no environment awareness of its own -- without this guard, a staging
  # apply would try to manage a lifecycle rule on r2_bucket_name's
  # production bucket, which has nothing to do with staging at all.
  count = var.environment == "production" ? 1 : 0

  account_id  = var.cloudflare_account_id
  bucket_name = var.r2_bucket_name

  # v5's rule shape: a list of objects, not repeated `rule {}` blocks: an
  # empty prefix matches every object in the bucket (no scoping), and
  # deletion is expressed as an age-based transition in seconds, not
  # S3-style expiration.days.
  rules = [{
    id = "orphan-object-backstop"
    conditions = {
      prefix = ""
    }
    enabled = true

    # Expire any object older than 48 hours. The sweep normally removes objects
    # within minutes of session expiry; this rule fires only if the sweep fails.
    delete_objects_transition = {
      condition = {
        type    = "Age"
        max_age = 172800 # 2 days, in seconds
      }
    }
  }]
}
