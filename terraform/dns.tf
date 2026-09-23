# Keeps kpednekar.dev's staging DNS record pointed at whichever CloudFront
# distribution the current ephemeral staging cycle actually created. Real
# gap found live: staging's distribution is destroyed and recreated on
# every deploy (staging.yml), but nothing was ever updating
# dumpster-staging.kpednekar.dev's DNS to match -- it was still pointing at
# a stale CNAME from a much older architecture (a public ALB, since
# replaced by the private-ALB/VPC-origin migration), meaning no human could
# actually reach staging by URL for manual testing, silently, since that
# migration landed. staging.yml's own smoke test worked around this by
# testing CloudFront's own default *.cloudfront.net domain directly instead
# of the real alias -- fine for automation, but a person can't be expected
# to do the same `curl --resolve` trick from a browser.
#
# Cloudflare, not Route 53: this account has no Route 53 hosted zone at all
# (confirmed via `aws route53 list-hosted-zones`) -- kpednekar.dev's
# authoritative DNS has stayed on Cloudflare (`dig NS kpednekar.dev`)
# since before this project's AWS migration, a leftover of R2's dashboard-
# managed DNS rather than anything AWS-side. The cloudflare provider was
# already a known integration here once, for that same reason (see
# s3_storage.tf's required_providers comment) -- reintroduced now for DNS
# specifically, not resurrected in full.
#
# Staging only: production's DNS was set up once, by hand, during its own
# cutover, and stays correct because production's CloudFront distribution
# is long-lived, not destroyed/recreated per deploy -- nothing to automate
# there today. Could be brought under this same pattern later for
# consistency, not required to fix the actual live bug.

# Empty block, deliberately: the provider reads CLOUDFLARE_API_TOKEN from
# the environment automatically, same implicit-credential pattern this
# config already relies on for the aws provider (no explicit provider "aws"
# block anywhere here either -- both read real credentials from the
# environment the person or CI job running `tofu apply` already has, not
# from anything written into this repo).
provider "cloudflare" {}

data "cloudflare_zone" "kpednekar_dev" {
  count = var.environment == "staging" ? 1 : 0

  filter = {
    name = "kpednekar.dev"
  }
}

resource "cloudflare_dns_record" "staging" {
  count = var.environment == "staging" ? 1 : 0

  zone_id = data.cloudflare_zone.kpednekar_dev[0].zone_id
  name    = var.acm_domain_name
  type    = "CNAME"
  content = aws_cloudfront_distribution.frontend.domain_name
  ttl     = 60    # short -- this value changes every ephemeral staging cycle, unlike a normal long-lived record
  proxied = false # CloudFront is already the CDN/edge here; Cloudflare's own proxy would just add a second, redundant hop
  comment = "Managed by terraform -- repointed automatically on every staging deploy (dns.tf)"
}
