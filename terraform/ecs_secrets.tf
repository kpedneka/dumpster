# Secrets Manager entries for every runtime secret api/worker/cleanup
# need -- per the secrets architecture already decided (original infra
# plan): GitHub holds only the OIDC deploy role, AWS Secrets Manager
# holds everything the app needs to run. Task definitions reference these
# ARNs in their `secrets` block, so values arrive as env vars at
# container start without ever being baked into an image or committed
# anywhere.
#
# Deliberately creates only the secret *containers* here, not their
# values -- no secret_version resource, on purpose. Real values (the
# actual Anthropic key, DB passwords, R2 keys) never belong in a .tf file
# or Terraform state in plain text. Populate each one by hand once,
# after apply:
#   aws secretsmanager put-secret-value --secret-id dumpster/production/database-url --secret-string '...'
# (etc. for each secret below, substituting "staging" for the other
# environment). Tasks referencing an unpopulated secret will fail to
# start until this is done -- expected, not a bug.
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
# DATABASE_URL/DATABASE_URL_POOLED hold Neon connection strings today,
# not Aurora. Still on R2 for real document storage and Anthropic direct
# for generation -- both get their own secrets here now and get replaced
# in place, same secret names, when those migration cards happen (S3 and
# Bedrock use IAM instead of a key at all, so those two secrets get
# deleted entirely at that point, not repointed).

locals {
  secret_names = [
    "database-url",          # DATABASE_URL -- Neon direct (non-pooled) connection string, for worker/cleanup
    "database-url-pooled",   # DATABASE_URL_POOLED -- Neon PgBouncer connection string, for api
    "anthropic-api-key",     # ANTHROPIC_API_KEY
    "s3-access-key",         # S3_ACCESS_KEY -- real document storage (R2 today)
    "s3-secret-key",         # S3_SECRET_KEY
    "r2-scratch-access-key", # R2_SCRATCH_ACCESS_KEY -- Batch job scratch handoff bucket
    "r2-scratch-secret-key", # R2_SCRATCH_SECRET_KEY
  ]
}

resource "aws_secretsmanager_secret" "runtime" {
  for_each = toset(local.secret_names)
  name     = "dumpster/${var.environment}/${each.value}"

  # Staging gets destroyed and recreated repeatedly by design (the whole
  # point of an ephemeral spin-up/test/tear-down environment) -- AWS's
  # default 30-day recovery window would block re-creating a
  # same-named secret for up to 30 days after a `tofu destroy`, since
  # the old one is still soft-deleted and reserving the name. Production
  # keeps the real default (immediate force-deletion of a live secret
  # should never be one command away).
  recovery_window_in_days = var.environment == "staging" ? 0 : 30
}

output "secret_arns" {
  value       = { for name, secret in aws_secretsmanager_secret.runtime : name => secret.arn }
  description = "Reference these in each task definition's secrets block, e.g. secret_arns[\"database-url\"]."
}
