# Where ../*.tf's real state lives -- an S3 bucket, not this machine's
# local disk. See terraform/bootstrap/state_backend.tf's header for the
# real incident that surfaced this was ever missing: every `tofu apply`
# this project's had used purely local state, invisible to any other
# machine (including, critically, an ephemeral GitHub Actions runner --
# the staging CI workflow's first real run tried to create every staging
# resource from scratch because it had no way to see this session's
# already-real ones).
#
# A `backend` block can't reference variables, locals, or data sources --
# Terraform needs to know where its own state lives before it can
# evaluate any of those -- so the bucket name is the same hardcoded,
# account-confirmed literal every other genuinely-global value in this
# config uses (data.aws_vpc.default's id, ecs_networking.tf's header).
#
# No `key` collision between environments: OpenTofu's S3 backend
# automatically namespaces every non-default workspace under
# env:/<workspace>/<key> -- staging and production each get their own
# state object in this one bucket, the same separation
# ecs_environment.tf's header already establishes for workspaces vs.
# var.environment (two different mechanisms, doing two different jobs).
#
# use_lockfile, not a DynamoDB table: OpenTofu/Terraform 1.10+ support
# native S3 conditional-write locking (a `.tflock` object using
# If-None-Match) -- one less piece of infrastructure than the old
# S3+DynamoDB pattern, and this bucket's own IAM grants (the deploy
# role's S3 statement in bootstrap/github_oidc.tf, scoped to
# arn:aws:s3:::dumpster-*) already cover it with no separate DynamoDB
# permission needed.
terraform {
  backend "s3" {
    bucket       = "dumpster-terraform-state-973010535819"
    key          = "dumpster.tfstate"
    region       = "us-east-1"
    use_lockfile = true
  }
}
