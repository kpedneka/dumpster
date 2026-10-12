# Lifecycle (cleanup) policies for the account's ECR repositories.
#
# The repositories themselves are created by hand, once, and only looked
# up by the environment stacks (see ../ecs_ecr.tf for why: staging's
# destroy/recreate cycle must not delete them). Their cleanup rules live
# here rather than in an environment stack for the same reason -- staging's
# rules have to keep working while staging itself is torn down, which is
# exactly when its repositories keep accumulating images from past runs.
#
# Every rule set keeps the newest N tagged images and drops untagged ones
# after a day. The untagged rule is safe for CI's multi-platform pushes:
# staging's images are OCI image indexes whose real image and build
# attestation are untagged child manifests, but ECR never expires an image
# a manifest list still references, and expires attestations on its own
# once their subject image is gone. In practice the rule only removes
# children orphaned by an expired index, and the old image a mutable
# `latest` tag (the Batch repositories) no longer points at.

locals {
  # Repository name => how many tagged images to keep.
  #
  # staging: every deploy-staging run pushes a fresh pr-<n>-<sha>-... tag,
  # and staging only ever runs the one it just built.
  # production: tagged production-<sha>, one per release; ten leaves plenty
  # of rollback targets.
  # Batch job images: one mutable `latest` tag each, so only the untagged
  # rule ever does anything.
  ecr_keep_tagged = {
    "dumpster-staging-runtime"           = 5
    "dumpster-staging-embed-sidecar"     = 5
    "dumpster-staging-otel-collector"    = 5
    "dumpster-production-runtime"        = 10
    "dumpster-production-embed-sidecar"  = 10
    "dumpster-production-otel-collector" = 10
    "dumpster-embedding"                 = 1
    "dumpster-entity-extraction"         = 1
    "dumpster-region-extraction"         = 1
  }
}

resource "aws_ecr_lifecycle_policy" "this" {
  for_each   = local.ecr_keep_tagged
  repository = each.key

  policy = jsonencode({
    rules = [
      {
        rulePriority = 1
        description  = "Keep the newest ${each.value} tagged images"
        selection = {
          tagStatus      = "tagged"
          tagPatternList = ["*"]
          countType      = "imageCountMoreThan"
          countNumber    = each.value
        }
        action = { type = "expire" }
      },
      {
        rulePriority = 2
        description  = "Remove untagged images a day after they were pushed"
        selection = {
          tagStatus   = "untagged"
          countType   = "sinceImagePushed"
          countUnit   = "days"
          countNumber = 1
        }
        action = { type = "expire" }
      },
    ]
  })
}
