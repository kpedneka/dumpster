# The S3 bucket ../*.tf's real state lives in -- found missing only by
# the staging workflow's actual first CI run (PR #94, card #12): every
# `tofu apply` this session had used purely local state on this one
# machine, which was invisible to a GitHub Actions runner. CI's `tofu
# apply` started from a blank state and tried to create every staging
# resource from scratch, colliding with the real ones this session had
# already built locally -- caught in full before anything was actually
# lost (confirmed directly against AWS: the real ECS services, security
# groups, ALB target group, and ECR repos were all still exactly as they
# were, no duplicates, no orphans -- see PR #94's own history for the
# specific check).
#
# Lives here, in bootstrap/, not ../ -- same reasoning as the OIDC
# provider/deploy role in this directory's other file: this bucket has to
# exist *before* ../'s own `tofu init` can even run (a backend config
# can't be satisfied by the same apply that would create the backend's
# own storage), so it can't be managed by the config that depends on it.
# Applied once, by hand, exactly like github_oidc.tf.

resource "aws_s3_bucket" "terraform_state" {
  bucket = "dumpster-terraform-state-${data.aws_caller_identity.current.account_id}"

  # Deliberately the opposite tradeoff from s3_storage.tf's documents/
  # scratch buckets, which force_destroy in staging for exactly the
  # reason this bucket must never: losing app data in an ephemeral test
  # environment is recoverable (re-ingest a document); losing this
  # bucket's contents means losing the record of every real AWS resource
  # this config manages, for every environment, at once.
  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_versioning" "terraform_state" {
  bucket = aws_s3_bucket.terraform_state.id
  versioning_configuration {
    # A corrupted or wrongly-overwritten state file is the single most
    # dangerous failure mode a shared backend has -- versioning means a
    # bad write is a `aws s3api list-object-versions` + restore away
    # from being unrecoverable, not gone.
    status = "Enabled"
  }
}

resource "aws_s3_bucket_public_access_block" "terraform_state" {
  bucket                  = aws_s3_bucket.terraform_state.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "terraform_state" {
  bucket = aws_s3_bucket.terraform_state.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

output "terraform_state_bucket_name" {
  value       = aws_s3_bucket.terraform_state.bucket
  description = "Set as the `bucket` argument in ../backend.tf's backend \"s3\" block."
}
