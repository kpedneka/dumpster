# Real AWS S3 buckets for object storage -- replaces Cloudflare R2 (see the
# "Migrate object storage: R2 -> S3" dev board card). Two buckets per
# environment, same split as before: real user documents (durable, IAM-only
# access) and a transient scratch bucket for AWS Batch jobs' input/output
# handoff (internal/entity/awsbatch, internal/llm/awsbatch,
# internal/manifest/awsbatch).
#
# Terraform-managed here, unlike AWS Batch's own job queues/definitions
# (deliberately hand-registered -- see ecs_worker.tf's note on
# var.regions_batch_job_definition): buckets are simple, low-churn resources
# with none of Batch's tight-loop-registration friction, and this is also
# the first point in this codebase where the R2 buckets themselves become
# Terraform-managed at all (they never were -- created by hand in the
# Cloudflare dashboard, per .env.example). Moving to AWS S3 is exactly the
# right moment to stop doing that by hand too.
#
# aws provider requirement lives here, not duplicated per-file --
# OpenTofu/Terraform allows exactly one required_providers block per module.
# Previously lived in the now-deleted r2_lifecycle.tf, which also carried a
# cloudflare provider requirement -- dropped entirely now that R2 is fully
# retired from every AWS-hosted environment.
terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

# Bucket names must be globally unique across all of AWS, not just this
# account -- "dumpster-staging" or "dumpster" alone risks colliding with a
# bucket in someone else's account. Suffixing with this account's ID (already
# available via ecs_iam.tf's data source) is the standard, cheap way to
# guarantee uniqueness without inventing a naming scheme.
locals {
  documents_bucket_name = "${local.name_prefix}-${data.aws_caller_identity.current.account_id}"
  scratch_bucket_name   = "${local.name_prefix}-scratch-${data.aws_caller_identity.current.account_id}"
}

# --- Documents bucket: real, user-owned uploads ---

resource "aws_s3_bucket" "documents" {
  bucket = local.documents_bucket_name

  # Same reasoning as aws_ecr_repository's force_delete (ecs_ecr.tf):
  # a non-empty bucket makes `tofu destroy` fail partway through by
  # default, and every staging teardown cycle leaves real (if
  # disposable) test objects behind -- caught for real the first time
  # staging was actually torn down after real usage. Production keeps
  # the safe default: force-deleting a bucket holding real user
  # documents should never be one command away.
  force_destroy = var.environment == "staging"
}

resource "aws_s3_bucket_public_access_block" "documents" {
  bucket                  = aws_s3_bucket.documents.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "documents" {
  bucket = aws_s3_bucket.documents.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# Independent infrastructure guardrail, not the primary deletion path: the
# account sweep (internal/account) hard-deletes every session's real objects
# before removing the session row, so under normal operation nothing here
# outlives its session. This rule exists in case the sweep malfunctions --
# same 48-hour ceiling the old (broken, never-applied) R2 lifecycle rule
# used, now expressed with a lifecycle resource that actually exists.
resource "aws_s3_bucket_lifecycle_configuration" "documents" {
  bucket = aws_s3_bucket.documents.id
  rule {
    id     = "orphan-object-backstop"
    status = "Enabled"
    filter {}
    expiration {
      days = 2
    }
  }
}

# --- Scratch bucket: transient AWS Batch job input/output handoff ---

resource "aws_s3_bucket" "scratch" {
  bucket = local.scratch_bucket_name

  # Same reasoning as the documents bucket above (and aws_ecr_repository's
  # force_delete, ecs_ecr.tf) -- scratch objects are already transient and
  # non-sensitive by design, so force-deleting them in staging carries even
  # less risk than the documents bucket does.
  force_destroy = var.environment == "staging"
}

resource "aws_s3_bucket_public_access_block" "scratch" {
  bucket                  = aws_s3_bucket.scratch.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# Scratch objects are already explicitly deleted by the Go code that writes
# them (internal/manifest/awsbatch and friends defer a delete via
# context.WithoutCancel once a job completes) -- this is a backstop for the
# same reason the documents bucket's rule is: if that cleanup ever fails to
# run (a crashed worker, an unhandled error), objects don't accumulate
# forever. A shorter window than documents (1 day, not 2): nothing here is
# ever meant to be read again once its Batch job finishes.
resource "aws_s3_bucket_lifecycle_configuration" "scratch" {
  bucket = aws_s3_bucket.scratch.id
  rule {
    id     = "scratch-backstop"
    status = "Enabled"
    filter {}
    expiration {
      days = 1
    }
  }
}

# --- VPC Gateway Endpoint: S3 traffic never needs the NAT instance ---
#
# Free (Gateway endpoints, unlike Interface endpoints, have no hourly or
# per-GB charge) and directly removes S3 from the NAT instance's traffic --
# real motivation named on the Infrastructure page's Cost Comparison
# section ("S3 has a free Gateway Endpoint"). Associated with the private
# route table only: that's what api/worker's tasks actually use (see
# ecs_networking.tf) and the only place NAT cost is being paid at all --
# AWS Batch's own compute environment runs in the default VPC's public
# subnets with a direct internet route already, so it has nothing to gain
# here.
resource "aws_vpc_endpoint" "s3" {
  vpc_id            = data.aws_vpc.default.id
  service_name      = "com.amazonaws.${var.aws_region}.s3"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = [aws_route_table.private.id]

  tags = {
    Name = "${local.name_prefix}-s3"
  }
}

# --- IAM: grant the ECS task role S3 access, no static keys anywhere ---
#
# Real AWS S3 participates in IAM, unlike R2 -- api/worker/cleanup's shared
# task role (ecs_iam.tf) gets scoped read/write/delete on both buckets
# directly, and internal/objectstore/s3store.New resolves those permissions
# via the standard AWS credential chain when no AccessKey/SecretKey are
# configured (see internal/config.Config's S3AccessKey/S3ScratchAccessKey
# doc comments). The s3-access-key/s3-secret-key and
# r2-scratch-access-key/r2-scratch-secret-key Secrets Manager entries are
# retired entirely, not repointed -- see ecs_secrets.tf.
data "aws_iam_policy_document" "ecs_task_s3" {
  statement {
    actions = [
      "s3:GetObject",
      "s3:PutObject",
      "s3:DeleteObject",
    ]
    resources = [
      "${aws_s3_bucket.documents.arn}/*",
      "${aws_s3_bucket.scratch.arn}/*",
    ]
  }
}

resource "aws_iam_role_policy" "ecs_task_s3" {
  name   = "${local.name_prefix}-ecs-task-s3"
  role   = aws_iam_role.ecs_task.id
  policy = data.aws_iam_policy_document.ecs_task_s3.json
}

output "documents_bucket_name" {
  value = aws_s3_bucket.documents.bucket
}

output "scratch_bucket_name" {
  value = aws_s3_bucket.scratch.bucket
}
