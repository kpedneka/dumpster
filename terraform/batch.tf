# AWS Batch resources for the worker's two background job types (entity
# extraction, ingestion-time embedding) plus the job definition PDF/image
# region extraction submits against the embedding queue -- see the "Bring
# the existing entity-extraction + embedding Batch jobs under Terraform"
# dev board card. Never Terraform-managed before this: these three job
# types were stood up by hand across earlier GPU-pilot and inference-
# consolidation work, alongside a handful of now-unused benchmarking/dev
# job queues, compute environments, and job definitions that are being
# decommissioned outside this file (deliberately not imported -- see the
# same card's notes) rather than pulled into Terraform state.
#
# Scope expanded from a real conversation on this card, not "import as-is":
# job queues and compute environments were shared across environments
# before this (the GPU one literally named "...-gpu-pilot", no longer
# accurate -- it's real production infrastructure now). Split per-
# environment here, matching every other account/region- or VPC-scoped
# resource in this config (local.name_prefix, ecs_environment.tf).
#
# Job definitions were *also* kept shared, not split, in this card's
# first version -- reasoned at the time as "no reason to duplicate the
# same image+resource template per environment." That reasoning held
# only as long as a human was the one running `tofu destroy` against
# staging and would naturally notice touching something shared. It
# stopped holding the moment the "design the staging spin-up/test/
# tear-down workflow" card's CI automation could destroy staging
# unattended, on every labeled PR -- confirmed for real, not
# hypothetically: an early, broken run of that workflow (before it had a
# shared state backend at all) deregistered these three job definitions
# as a side effect of a failed destroy, taking real staging ingestion
# down until they were re-registered by hand. Environment-scoped here
# now for exactly the same reason the queues/CEs already are: no shared
# resource this config manages should be reachable by an automated,
# unattended destroy of just one environment. Costs nothing ongoing --
# job definitions are metadata, not running compute.

# --- Shared networking / IAM lookups ---
#
# The account's 6 existing public subnets and default security group --
# same values ecs_networking.tf's header comment already confirmed via
# `aws ec2 describe-subnets` (this account has no private subnets outside
# the two this migration added for api/worker, and Batch's EC2/Fargate
# job runners have always used the original 6 public ones directly, not
# those). Hardcoded, matching this file set's existing precedent for
# real, confirmed-by-CLI account IDs (data.aws_vpc.default's id below)
# rather than a dynamic aws_subnets lookup that could silently change
# scope if someone adds an unrelated subnet to this VPC later.
locals {
  batch_subnet_ids = [
    "subnet-6241f54e", # us-east-1d
    "subnet-f59bf9bd", # us-east-1a
    "subnet-8c2da880", # us-east-1f
    "subnet-aeebe4cb", # us-east-1c
    "subnet-71cde74d", # us-east-1e
    "subnet-fe13bea4", # us-east-1b
  ]
}

data "aws_security_group" "default" {
  id = "sg-61458610" # this VPC's default security group -- confirmed via `aws ec2 describe-security-groups`, already what every existing Batch compute environment uses
}

# Batch's own service-linked role, created automatically the first time
# any Batch compute environment was ever stood up in this account (not by
# this Terraform, and not something this card's scope re-creates -- one
# service-linked role covers the whole account, not per-environment).
data "aws_iam_role" "batch_service_role" {
  name = "AWSServiceRoleForBatch"
}

# The GPU compute environment's EC2 instance profile -- also predates
# this Terraform, shared across environments deliberately: it's a
# generic "let EC2 register with ECS" role (AmazonEC2ContainerServiceforEC2Role),
# nothing environment-specific about its permissions, so splitting it
# per-environment would add a second identical role for no real benefit.
data "aws_iam_instance_profile" "batch_gpu" {
  name = "dumpster-batch-ecs-instance-profile"
}

# The account's default ECS task execution role -- what the embedding and
# region-extraction Fargate job definitions already use for ECR pull /
# CloudWatch Logs (confirmed via `aws batch describe-job-definitions`),
# distinct from this project's own dumpster-<environment>-ecs-execution
# role (ecs_iam.tf), which covers api/worker/cleanup's ECS *services*, not
# Batch jobs. Left as-is rather than repointed: no reason these Fargate
# job containers need this project's Secrets Manager access (they take
# their real work via S3 scratch objects and command-line args, not env
# secrets -- see internal/entity/awsbatch, internal/llm/awsbatch).
data "aws_iam_role" "ecs_task_execution_default" {
  name = "ecsTaskExecutionRole"
}

# --- Job definitions: now environment-scoped (see header) ---
#
# No import blocks here any more -- the historical, one-time import of
# the original shared dumpster-entity-extraction/dumpster-embedding-
# jobdef/dumpster-region-extraction (revision :2, after the incident the
# header describes) is done; these are brand new, per-environment-named
# resources with no prior real object to import from. The old shared
# names are left orphaned in AWS (INACTIVE-able via a future manual
# cleanup, not urgent -- job definitions cost nothing idle), not deleted
# by this change: nothing currently depends on them once the worker's
# env vars below repoint at the new per-environment ones.

resource "aws_batch_job_definition" "entity_extraction" {
  name = "${local.name_prefix}-entity-extraction"
  type = "container"

  container_properties = jsonencode({
    image  = "${data.aws_caller_identity.current.account_id}.dkr.ecr.${var.aws_region}.amazonaws.com/dumpster-entity-extraction:latest"
    vcpus  = 4
    memory = 14000
    resourceRequirements = [
      { type = "GPU", value = "1" }
    ]
  })

  retry_strategy {
    attempts = 1
  }

  timeout {
    attempt_duration_seconds = 900
  }
}

resource "aws_batch_job_definition" "embedding" {
  name                  = "${local.name_prefix}-embedding"
  type                  = "container"
  platform_capabilities = ["FARGATE"]

  container_properties = jsonencode({
    image            = "${data.aws_caller_identity.current.account_id}.dkr.ecr.${var.aws_region}.amazonaws.com/dumpster-embedding:latest"
    executionRoleArn = data.aws_iam_role.ecs_task_execution_default.arn
    resourceRequirements = [
      { type = "VCPU", value = "4" },
      { type = "MEMORY", value = "8192" },
    ]
    networkConfiguration = {
      assignPublicIp = "ENABLED"
    }
    fargatePlatformConfiguration = {
      platformVersion = "LATEST"
    }
  })
}

resource "aws_batch_job_definition" "region_extraction" {
  name                  = "${local.name_prefix}-region-extraction"
  type                  = "container"
  platform_capabilities = ["FARGATE"]

  container_properties = jsonencode({
    image            = "${data.aws_caller_identity.current.account_id}.dkr.ecr.${var.aws_region}.amazonaws.com/dumpster-region-extraction:latest"
    executionRoleArn = data.aws_iam_role.ecs_task_execution_default.arn
    resourceRequirements = [
      { type = "VCPU", value = "1" },
      { type = "MEMORY", value = "2048" },
    ]
    networkConfiguration = {
      assignPublicIp = "ENABLED"
    }
    fargatePlatformConfiguration = {
      platformVersion = "LATEST"
    }
  })
}

# --- Entity extraction: GPU, EC2, per-environment (new -- see header) ---

resource "aws_batch_compute_environment" "entity_extraction" {
  name         = "${local.name_prefix}-entity-extraction-ce"
  type         = "MANAGED"
  service_role = data.aws_iam_role.batch_service_role.arn

  compute_resources {
    type                = "EC2"
    allocation_strategy = "BEST_FIT"
    instance_role       = data.aws_iam_instance_profile.batch_gpu.arn
    instance_type       = ["g4dn.xlarge"]
    # min_vcpus = 0, matching the existing dumpster-batch-gpu-pilot CE:
    # idle GPU capacity costs nothing, which is exactly why splitting
    # this per-environment (rather than staying shared, as embedding's
    # note used to argue) is safe -- staging's own CE sits at zero cost
    # whenever staging isn't actively running an ingestion job.
    min_vcpus          = 0
    max_vcpus          = 16
    security_group_ids = [data.aws_security_group.default.id]
    subnets            = local.batch_subnet_ids
  }
}

resource "aws_batch_job_queue" "entity_extraction" {
  name     = "${local.name_prefix}-entity-extraction-queue"
  state    = "ENABLED"
  priority = 1

  compute_environment_order {
    order               = 1
    compute_environment = aws_batch_compute_environment.entity_extraction.arn
  }
}

# --- Embedding: Fargate, per-environment (new -- see header) ---
#
# Fargate-backed, unlike entity extraction's EC2 GPU environment -- no
# idle cost at all (billed per job run, not per provisioned instance),
# which is exactly why this split was uncontroversial even before
# confirming the GPU side's min_vcpus=0 already made splitting that one
# free too. Region extraction (below) rides this same per-environment
# queue rather than getting a dedicated compute environment -- same
# "not worth a 4th idle environment for a CPU-only, same-profile job
# type" reasoning ecs_worker.tf's regions variables already used to
# justify sharing with embedding, just applied per-environment now
# instead of across all environments at once.

resource "aws_batch_compute_environment" "embedding" {
  name         = "${local.name_prefix}-embedding-ce"
  type         = "MANAGED"
  service_role = data.aws_iam_role.batch_service_role.arn

  compute_resources {
    type               = "FARGATE"
    max_vcpus          = 16
    security_group_ids = [data.aws_security_group.default.id]
    subnets            = local.batch_subnet_ids
  }
}

resource "aws_batch_job_queue" "embedding" {
  name     = "${local.name_prefix}-embedding-queue"
  state    = "ENABLED"
  priority = 1

  compute_environment_order {
    order               = 1
    compute_environment = aws_batch_compute_environment.embedding.arn
  }
}

output "entity_extraction_job_queue_name" {
  value = aws_batch_job_queue.entity_extraction.name
}

output "embedding_job_queue_name" {
  value = aws_batch_job_queue.embedding.name
}
