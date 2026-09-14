# Decouples the SPA's static hosting from the API's Fargate task -- see the
# "Decouple SPA hosting from the API instance" dev board card. Before this,
# both environments served the built React app from the same Go binary/task
# that served the API (internal/server/server.go's old spaHandler), meaning
# a frontend-only change needed a full backend image rebuild + ECS redeploy
# to ship -- confirmed as a real cost, not a theoretical one: two real
# production fixes on 2026-09-13 (session-dedup, embed-sidecar threads) each
# needed exactly that, despite touching only one side of the app.
#
# One CloudFront distribution per environment does the routing a single
# same-origin server used to: /api/* (and /healthz) go to the ALB, and
# everything else goes to a private S3 bucket holding the built SPA. Applies
# to staging too, not just production: internal/server/server.go's /api
# prefix is shared code, not environment-conditional, so staging's Go binary
# stops serving the SPA at all the moment this ships regardless of whether
# staging gets its own CloudFront -- without it, staging's per-PR manual
# testing workflow would silently lose its frontend. The real cost of that
# choice is accepted deliberately: staging's entire environment (including
# its ALB) is destroyed and recreated per PR cycle via the shared "staging"
# OpenTofu workspace (see .github/workflows/staging.yml), and a CloudFront
# distribution typically takes 5-20 minutes to reach "Deployed" -- meaningfully
# slower than staging's current ~15-minute cycle. Accepted over the
# alternative (S3-only, two subdomains for staging) because that alternative
# would make staging cross-origin -- requiring real CORS handling that
# doesn't exist anywhere in this codebase and that production would never
# use -- and would leave CloudFront's own routing/SPA-fallback/cache-header
# behavior, the actual subject of this card, completely untested in staging.

locals {
  frontend_bucket_name          = "${local.name_prefix}-frontend-${data.aws_caller_identity.current.account_id}"
  frontend_s3_origin_id         = "s3-frontend"
  frontend_alb_origin_id        = "alb"
  cloudfront_origin_header_name = "X-Origin-Verify"
}

# See frontend.tf's alb origin block and ecs_alb.tf's listener rule for how
# this is used -- a shared secret proving a request to the ALB genuinely
# came from this specific CloudFront distribution, not just any CloudFront
# distribution. Treated like a real credential per AWS's own guidance
# (username/password, not a public value) -- 32 random characters,
# alphanumeric only (special=false) since it travels as a raw HTTP header
# value, where certain special characters are awkward or invalid.
resource "random_password" "cloudfront_origin_secret" {
  length  = 32
  special = false
}

# The private, AWS-managed connection into the VPC that lets CloudFront
# reach the internal ALB (ecs_alb.tf) without it ever having a public IP.
# CloudFront creates a service-managed ENI in the ALB's own subnets for
# this -- confirmed directly against AWS's docs, that ENI creation is what
# the "up to 15 minutes to reach Deployed" wait (below) actually covers.
resource "aws_cloudfront_vpc_origin" "alb" {
  vpc_origin_endpoint_config {
    name                   = "${local.name_prefix}-alb"
    arn                    = aws_lb.api.arn
    http_port              = 80
    https_port             = 443
    origin_protocol_policy = "https-only"

    origin_ssl_protocols {
      items    = ["TLSv1.2"]
      quantity = 1
    }
  }
}

# --- S3 bucket: the built SPA (dist/), private, reachable only via CloudFront ---

resource "aws_s3_bucket" "frontend" {
  bucket = local.frontend_bucket_name

  # Same reasoning as the documents/scratch buckets (s3_storage.tf): staging
  # gets torn down constantly and holds nothing worth protecting from
  # force-delete; production keeps the safe default.
  force_destroy = var.environment == "staging"
}

resource "aws_s3_bucket_public_access_block" "frontend" {
  bucket                  = aws_s3_bucket.frontend.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "frontend" {
  bucket = aws_s3_bucket.frontend.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# Origin Access Control: the modern (non-deprecated) way to let CloudFront,
# and only CloudFront, read this bucket -- no public bucket, no legacy OAI.
resource "aws_cloudfront_origin_access_control" "frontend" {
  name                              = "${local.name_prefix}-frontend"
  origin_access_control_origin_type = "s3"
  signing_behavior                  = "always"
  signing_protocol                  = "sigv4"
}

# Scoped with a SourceArn condition to this specific distribution, not just
# "any CloudFront distribution in this account" -- the standard OAC bucket
# policy shape AWS documents, preventing a different distribution (this
# account has more than one, staging + production) from being pointed at
# this bucket.
data "aws_iam_policy_document" "frontend_bucket" {
  statement {
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.frontend.arn}/*"]
    principals {
      type        = "Service"
      identifiers = ["cloudfront.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "AWS:SourceArn"
      values   = [aws_cloudfront_distribution.frontend.arn]
    }
  }
}

resource "aws_s3_bucket_policy" "frontend" {
  bucket = aws_s3_bucket.frontend.id
  policy = data.aws_iam_policy_document.frontend_bucket.json
}

# --- Cache-Control: hashed assets cached forever, index.html never cached ---
#
# Real gap found while investigating this card: the JS/CSS bundle is
# filename-hashed (a new build gets a new filename) but was previously
# served with Cloudflare's own default 4-hour heuristic cache, not anything
# the app set explicitly -- confirmed directly (no Cache-Control logic
# existed in server.go). A hashed asset never changes at its existing
# filename, so it should be cached effectively forever; index.html, by
# contrast, is the one file that MUST be re-fetched on every visit, since
# it's what references the current build's hashed filenames -- caching it
# would mean visitors keep loading a stale app shell that references
# assets from a previous deploy.
resource "aws_cloudfront_response_headers_policy" "immutable_assets" {
  name = "${local.name_prefix}-immutable-assets"
  custom_headers_config {
    items {
      header   = "Cache-Control"
      value    = "public, max-age=31536000, immutable"
      override = true
    }
  }
}

resource "aws_cloudfront_response_headers_policy" "no_cache_shell" {
  name = "${local.name_prefix}-no-cache-shell"
  custom_headers_config {
    items {
      header   = "Cache-Control"
      value    = "no-cache"
      override = true
    }
  }
}

# --- Managed policies (AWS-provided, not custom) for the /api/* behavior ---
# CachingDisabled: never cache dynamic API responses at the edge.
# AllViewer: forward every header/cookie/query string to the origin --
# needed for the session cookie and any request header the API reads.
data "aws_cloudfront_cache_policy" "caching_optimized" {
  name = "Managed-CachingOptimized"
}

data "aws_cloudfront_cache_policy" "caching_disabled" {
  name = "Managed-CachingDisabled"
}

data "aws_cloudfront_origin_request_policy" "all_viewer" {
  name = "Managed-AllViewer"
}

# --- The distribution itself ---

resource "aws_cloudfront_distribution" "frontend" {
  enabled             = true
  is_ipv6_enabled     = true
  default_root_object = "index.html"
  aliases             = [var.acm_domain_name]

  # PriceClass_100 (cheapest, fewest edge locations) for staging: its only
  # real traffic is one IP (var.staging_allowed_cidrs) doing manual review,
  # so the extra edge locations PriceClass_All pays for buy nothing here.
  # Production keeps every edge location -- real, geographically-unknown
  # visitors benefit from the full network.
  #
  # wait_for_deployment = false for staging: confirmed directly against the
  # provider's own docs, this only controls whether `tofu apply`/`destroy`
  # blocks until the distribution reaches "Deployed" (5-20+ min,
  # confirmed directly against AWS's docs) -- it does NOT change how many
  # edge locations receive the config (that's price_class's job, and
  # confirmed separately that price_class doesn't affect propagation speed
  # either). Staging's entire environment is destroyed and recreated every
  # PR cycle (.github/workflows/staging.yml), so blocking on global
  # propagation here would roughly double that cycle's time for a
  # verification step that (per production's own default) still matters
  # there, just not enough to justify the wait on every single staging
  # cycle. Production keeps the default (true): a real go-live deserves a
  # real verified "Deployed" state, not just an API call that succeeded.
  # The real cost of skipping the wait: the smoke test that runs right
  # after apply can hit a not-yet-synced edge location -- mitigated with a
  # retrying check in staging.yml, not a single immediate curl.
  price_class         = var.environment == "staging" ? "PriceClass_100" : "PriceClass_All"
  wait_for_deployment = var.environment != "staging"

  origin {
    domain_name              = aws_s3_bucket.frontend.bucket_regional_domain_name
    origin_id                = local.frontend_s3_origin_id
    origin_access_control_id = aws_cloudfront_origin_access_control.frontend.id
  }

  # vpc_origin_config, not custom_origin_config: the ALB is internal, in
  # private subnets with no public IP at all (ecs_alb.tf) -- CloudFront
  # reaches it over a private, AWS-managed ENI-based connection into the
  # VPC, never touching the public internet. This is the properly-done
  # version of "restrict this ALB to CloudFront-only," not the
  # public-ALB-plus-secret-header design it replaced: that was a real,
  # working *policy* guarantee (three layers: security-group prefix list,
  # TLS cert match, secret header), but the ALB still had a public IP,
  # technically part of internet routing space. A private ALB is a
  # *structural* guarantee instead -- there's no route to it from the
  # internet, full stop, nothing to misconfigure away later. AWS's own
  # restrict-access-to-load-balancer.html docs list VPC origins first, with
  # the custom-header approach as the fallback for when you can't move to
  # private subnets.
  #
  # The secret header (custom_header, random_password.cloudfront_origin_secret)
  # stays anyway, as one more layer even though the network path alone now
  # closes the gap the header was originally added for (a different AWS
  # account's own CloudFront distribution referencing this ALB) -- a
  # private ALB has no route for another distribution to reach at all,
  # VPC-origin or not, so this is now genuinely defense-in-depth rather
  # than the actual enforcement mechanism.
  origin {
    domain_name = aws_lb.api.dns_name
    origin_id   = local.frontend_alb_origin_id
    vpc_origin_config {
      vpc_origin_id = aws_cloudfront_vpc_origin.alb.id
    }
    custom_header {
      name  = local.cloudfront_origin_header_name
      value = random_password.cloudfront_origin_secret.result
    }
  }

  default_cache_behavior {
    allowed_methods            = ["GET", "HEAD"]
    cached_methods             = ["GET", "HEAD"]
    target_origin_id           = local.frontend_s3_origin_id
    viewer_protocol_policy     = "redirect-to-https"
    cache_policy_id            = data.aws_cloudfront_cache_policy.caching_optimized.id
    response_headers_policy_id = aws_cloudfront_response_headers_policy.no_cache_shell.id
    compress                   = true
  }

  ordered_cache_behavior {
    path_pattern               = "/assets/*"
    allowed_methods            = ["GET", "HEAD"]
    cached_methods             = ["GET", "HEAD"]
    target_origin_id           = local.frontend_s3_origin_id
    viewer_protocol_policy     = "redirect-to-https"
    cache_policy_id            = data.aws_cloudfront_cache_policy.caching_optimized.id
    response_headers_policy_id = aws_cloudfront_response_headers_policy.immutable_assets.id
    compress                   = true
  }

  # compress = false, deliberately: the search endpoint streams Server-Sent
  # Events (a live token-by-token answer), and compression on a streaming
  # response risks CloudFront buffering it waiting for enough data to
  # compress, defeating the live-streaming UX. CachingDisabled already
  # means nothing here benefits from compression's usual bandwidth win
  # anyway (every request already goes to origin, nothing is ever re-served
  # from cache). Needs a real check post-deploy against a live search
  # request -- flagged, not just assumed safe.
  ordered_cache_behavior {
    path_pattern             = "/api/*"
    allowed_methods          = ["DELETE", "GET", "HEAD", "OPTIONS", "PATCH", "POST", "PUT"]
    cached_methods           = ["GET", "HEAD"]
    target_origin_id         = local.frontend_alb_origin_id
    viewer_protocol_policy   = "https-only"
    cache_policy_id          = data.aws_cloudfront_cache_policy.caching_disabled.id
    origin_request_policy_id = data.aws_cloudfront_origin_request_policy.all_viewer.id
    compress                 = false
  }

  # /healthz stays reachable at its real, unprefixed path through the public
  # domain too (not just internally, where the ALB's own target-group health
  # check already hits it directly against the container) -- convenient for
  # quick manual/CLI checks, matching how it's actually been used throughout
  # this migration.
  ordered_cache_behavior {
    path_pattern             = "/healthz"
    allowed_methods          = ["GET", "HEAD"]
    cached_methods           = ["GET", "HEAD"]
    target_origin_id         = local.frontend_alb_origin_id
    viewer_protocol_policy   = "https-only"
    cache_policy_id          = data.aws_cloudfront_cache_policy.caching_disabled.id
    origin_request_policy_id = data.aws_cloudfront_origin_request_policy.all_viewer.id
  }

  # React Router's client-side routes (e.g. /kbs/:kbId) have no matching
  # object in the S3 bucket, so S3 returns 403 (not 404 -- this bucket has
  # no public ListBucket, so a missing key looks like an access denial, not
  # a normal 404) via CloudFront. Rewriting both to a 200 index.html is the
  # standard SPA-on-S3 fallback, replacing what server.go's old spaHandler
  # used to do by inspecting the Accept header on a single shared server.
  custom_error_response {
    error_code         = 403
    response_code      = 200
    response_page_path = "/index.html"
  }
  custom_error_response {
    error_code         = 404
    response_code      = 200
    response_page_path = "/index.html"
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  # The existing ACM cert (ecs_alb.tf's data.aws_acm_certificate.this) is
  # already in us-east-1 -- the one region CloudFront requires certs to live
  # in -- and already covers this exact domain, so no new cert is needed.
  viewer_certificate {
    acm_certificate_arn      = data.aws_acm_certificate.this.arn
    ssl_support_method       = "sni-only"
    minimum_protocol_version = "TLSv1.2_2021"
  }
}

output "frontend_bucket_name" {
  value = aws_s3_bucket.frontend.bucket
}

output "cloudfront_distribution_id" {
  value = aws_cloudfront_distribution.frontend.id
}

output "cloudfront_domain_name" {
  value = aws_cloudfront_distribution.frontend.domain_name
}
