# Secrets Manager entries for every runtime secret api/worker/cleanup
# need -- per the secrets architecture already decided (original infra
# plan): GitHub holds only the OIDC deploy role, AWS Secrets Manager
# holds everything the app needs to run. Task definitions reference these
# ARNs in their `secrets` block, so values arrive as env vars at
# container start without ever being baked into an image or committed
# anywhere.
#
# Looked up by name (data source), not created/managed by this config --
# deliberately, not an oversight. Secrets have a genuinely different
# lifecycle than the ephemeral compute the rest of this directory manages:
# staging in particular gets destroyed and recreated routinely (see the
# ECS Migration dev board's staging runbook), and when these were
# `resource` blocks, every `tofu destroy` deleted the real secret values
# right along with the compute -- forcing all 7 to be re-entered by hand
# on every single cycle. A real, repeatedly-hit friction point, not a
# hypothetical one. As a data source, `tofu destroy` has no ability to
# touch these at all: destroy the whole environment as many times as you
# want, the values persist in Secrets Manager the entire time. This is a
# real safety improvement for production too, not just staging
# convenience -- a production `tofu destroy` (which should never happen,
# but "should never happen" isn't the same guarantee as "structurally
# can't") now literally cannot delete production's real credentials,
# regardless of any recovery-window setting.
#
# The tradeoff: each of the 3 secrets needs a real, one-time bootstrap
# per environment BEFORE the first `tofu apply` against that environment
# -- before, not after, unlike the old resource-managed version:
#   for name in database-url database-url-pooled anthropic-api-key; do
#     aws secretsmanager create-secret --name "dumpster/staging/$name"
#     aws secretsmanager put-secret-value --secret-id "dumpster/staging/$name" --secret-string '...'
#   done
# (substitute "production" and real values as appropriate). Never needed
# again after that, regardless of how many times the environment itself
# gets destroyed and recreated -- the whole point of this change.
#
# Each secret's real AWS name is namespaced dumpster/<environment>/<name>
# (see ecs_environment.tf's var.environment) -- staging and production
# get their own separate secret values under separate paths, and
# ecs_iam.tf's execution-role policy only grants read access to this
# environment's own path, so staging can never accidentally read (or,
# worse, silently share) production's real database/API credentials.
# The for_each keys below stay short (no environment segment) so every
# reference site (ecs_api.tf, ecs_worker.tf, ecs_cleanup.tf) can address
# a secret the same way regardless of which environment is being applied.
#
# Still on Neon (see the ECS Migration dev board's cost discussion) --
# DATABASE_URL/DATABASE_URL_POOLED hold Neon connection strings today, not
# Aurora. Anthropic direct for generation, not Bedrock yet -- both
# migrations are separate, later cards. Object storage's four secrets
# (s3-access-key, s3-secret-key, r2-scratch-access-key,
# r2-scratch-secret-key) were deleted entirely, not repointed, once real
# document/scratch storage moved to AWS S3 (see s3_storage.tf and the
# "Migrate object storage: R2 -> S3" dev board card) -- S3 participates in
# AWS IAM, so the ECS task role authenticates directly with no static key
# to manage or rotate at all.

locals {
  secret_names = [
    "database-url",        # DATABASE_URL -- Neon direct (non-pooled) connection string, for worker/cleanup
    "database-url-pooled", # DATABASE_URL_POOLED -- Neon PgBouncer connection string, for api
    "anthropic-api-key",   # ANTHROPIC_API_KEY
  ]
}

data "aws_secretsmanager_secret" "runtime" {
  for_each = toset(local.secret_names)
  name     = "dumpster/${var.environment}/${each.value}"
}

output "secret_arns" {
  value       = { for name, secret in data.aws_secretsmanager_secret.runtime : name => secret.arn }
  description = "Reference these in each task definition's secrets block, e.g. secret_arns[\"database-url\"]."
}
