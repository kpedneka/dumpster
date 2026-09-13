# One ECR repository, not three: cmd/api, cmd/worker, and cmd/cleanup all
# build into the same "runtime" image (Dockerfile's runtime stage) --
# confirmed directly, and cmd/cleanup's build step was added to that
# stage as part of this same change, matching how api/worker already
# shared it. ECS task definitions differ by container command
# (/bin/api, /bin/worker, /bin/cleanup), not by image.
#
# Looked up by name (data source), not created/managed by this config --
# same reasoning, and the same real incident class, as ecs_secrets.tf's
# Secrets Manager entries: staging gets destroyed and recreated routinely
# (the whole point of the staging CI workflow, card #12), and as a
# `resource` with force_delete=true for staging, every real destroy
# deleted the repository itself, not just its images -- confirmed for
# real, not hypothetically: the first CI run whose `tofu apply` and
# `tofu destroy` both actually completed left the very next run's image
# push failing with "repository ... does not exist," since that push
# happens *before* apply would have recreated it. A container registry
# is closer to durable artifact storage than to the ephemeral compute
# this config's destroy cycle is meant to tear down -- same category
# Secrets Manager already moved into for the identical reason.
#
# The tradeoff: each repository needs a real, one-time bootstrap per
# environment BEFORE the first `tofu apply` against that environment --
# before, not after:
#   aws ecr create-repository --repository-name dumpster-staging-runtime --image-tag-mutability IMMUTABLE
#   aws ecr put-image-scanning-configuration --repository-name dumpster-staging-runtime --image-scanning-configuration scanOnPush=true
# (substitute dumpster-staging-embed-sidecar and "production" as
# appropriate). Never needed again after that, regardless of how many
# times staging itself gets destroyed and recreated -- the whole point
# of this change, and scan-on-push/immutability become one-time
# repository settings this config no longer manages, not something lost.

data "aws_ecr_repository" "runtime" {
  name = "${local.name_prefix}-runtime"
}

output "ecr_repository_url" {
  value       = data.aws_ecr_repository.runtime.repository_url
  description = "Push the built runtime image here, tagged by commit SHA, as part of the CD pipeline (see the staging/CD design card)."
}

# The query-embedding sidecar's image (Dockerfile.batch-embed-job's
# `sidecar` target -- see ecs_api.tf). A separate repo from `runtime`,
# not a repurposing of ecs_inference.tf's `inference` repo: the
# standalone inference service is still live and still needed for
# cmd/worker's /regions calls until a separate, later card folds region
# classification into the AWS Batch embed job -- this repo exists
# alongside it, not instead of it, for now.
data "aws_ecr_repository" "embed_sidecar" {
  name = "${local.name_prefix}-embed-sidecar"
}

output "embed_sidecar_repository_url" {
  value       = data.aws_ecr_repository.embed_sidecar.repository_url
  description = "Push the sidecar image here (docker build -f Dockerfile.batch-embed-job --target sidecar ...), tagged by commit SHA."
}
