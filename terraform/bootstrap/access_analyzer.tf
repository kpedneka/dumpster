# Continuous drift detection for over-permissioned grants -- the standing
# safeguard against the reactive, one-AccessDenied-at-a-time pattern the
# deploy role's policy (github_oidc.tf) was built under. Flags permissions
# granted but unused for 90+ days, ongoing, no maintenance. Account-level,
# so it belongs in this bootstrap module alongside the OIDC role and
# CloudTrail trail above, not the per-environment ../*.tf config.
#
# Type ACCOUNT_UNUSED_ACCESS, not ACCOUNT (external-access findings) --
# external-access analyzers check for cross-account/public resource
# exposure, a different concern (resource policies, not identity
# policies) that doesn't apply to this personal, single-project account.
# Doesn't require the CloudTrail trail above -- unused-access findings are
# built from IAM's own last-accessed data (Access Advisor), a separate
# source AWS already tracks natively for every account.

resource "aws_accessanalyzer_analyzer" "unused_access" {
  analyzer_name = "dumpster-unused-access"
  type          = "ACCOUNT_UNUSED_ACCESS"

  # Explicit, matching AWS's own server-side default (90 days) -- left
  # implicit originally, which meant every `tofu plan` saw AWS's silently-
  # applied default as drift against "no configuration specified," and
  # since this block can't be updated in place (only replaced), that
  # cosmetic mismatch alone wanted to delete and recreate the analyzer on
  # every single plan. Written out explicitly here so the configured value
  # and the real state actually agree.
  configuration {
    unused_access {
      unused_access_age = 90
    }
  }
}
