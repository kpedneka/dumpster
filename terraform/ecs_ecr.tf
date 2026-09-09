# One ECR repository, not three: cmd/api, cmd/worker, and cmd/cleanup all
# build into the same "runtime" image (Dockerfile's runtime stage) --
# confirmed directly, and cmd/cleanup's build step was added to that
# stage as part of this same change, matching how api/worker already
# shared it. ECS task definitions differ by container command
# (/bin/api, /bin/worker, /bin/cleanup), not by image.

resource "aws_ecr_repository" "runtime" {
  name                 = "${local.name_prefix}-runtime"
  image_tag_mutability = "IMMUTABLE"

  # Staging only: ECR refuses to delete a non-empty repository by
  # default, and every staging cycle pushes a real image before testing
  # -- without this, `tofu destroy` fails on this resource every single
  # time (caught for real on the first actual teardown). Production
  # keeps the safe default: force-deleting a repo holding real deployed
  # images should never be one command away.
  force_delete = var.environment == "staging"

  image_scanning_configuration {
    scan_on_push = true
  }
}

output "ecr_repository_url" {
  value       = aws_ecr_repository.runtime.repository_url
  description = "Push the built runtime image here, tagged by commit SHA, as part of the CD pipeline (see the staging/CD design card)."
}
