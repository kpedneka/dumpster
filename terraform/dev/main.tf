# The pipeline-only dev stack: real Step Functions state machines and
# Lambdas for local development (`make run`) to publish jobs to, so local
# runs the same pipeline staging and production do, with no
# development-only orchestration code. See the "Pipeline-only dev stack in
# AWS + make dev-deploy" dev board card.
#
# Only the pipeline (terraform/modules/pipeline) plus a scratch bucket:
# no VPC, ECS, ALB or CloudFront, and nothing that bills while idle.
# Everything else local dev already uses stays as it is:
#   - the database and the uploads bucket (Cloudflare R2) are the ones
#     .env.local points at;
#   - the Batch queues and job definitions are the long-lived ones local
#     dev and the GPU pilot already use, not created here.
#
# Deployed with `make dev-deploy` using your own AWS profile, never by CI.
# Secrets come from .env.local through TF_VAR_* (see the Makefile), so
# they live in one place.

terraform {
  required_version = ">= 1.8"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
    archive = {
      source  = "hashicorp/archive"
      version = "~> 2.0"
    }
  }

  # Same state bucket as the main stack, under its own key.
  backend "s3" {
    bucket       = "dumpster-terraform-state-973010535819"
    key          = "dev/pipeline.tfstate"
    region       = "us-east-1"
    use_lockfile = true
  }
}

provider "aws" {
  region = var.aws_region
}

data "aws_caller_identity" "current" {}

locals {
  name_prefix = "dumpster-dev"
}

# --- Scratch bucket: Batch jobs' input/result handoff ---

resource "aws_s3_bucket" "scratch" {
  bucket        = "${local.name_prefix}-scratch-${data.aws_caller_identity.current.account_id}"
  force_destroy = true
}

resource "aws_s3_bucket_public_access_block" "scratch" {
  bucket                  = aws_s3_bucket.scratch.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# Backstop only: the pipeline deletes its handoff objects when a run
# finishes. Same 1-day window as the main stack's scratch bucket.
resource "aws_s3_bucket_lifecycle_configuration" "scratch" {
  bucket = aws_s3_bucket.scratch.id
  rule {
    id     = "scratch-backstop"
    status = "Enabled"
    filter {}
    expiration {
      days = 1
    }
  }
}

# --- Existing Batch resources ---

data "aws_batch_job_queue" "embedding" {
  name = var.embed_job_queue
}

data "aws_batch_job_queue" "gpu" {
  name = var.entity_job_queue
}

# --- The pipeline ---

data "archive_file" "lambda_pipeline" {
  type        = "zip"
  source_file = "${path.module}/../../build/lambda-pipeline/bootstrap"
  output_path = "${path.module}/../../build/dev/lambda-pipeline.zip"
}

data "archive_file" "lambda_stateless_jobs" {
  type        = "zip"
  source_file = "${path.module}/../../build/lambda-stateless-jobs/bootstrap"
  output_path = "${path.module}/../../build/dev/lambda-stateless-jobs.zip"
}

module "pipeline" {
  source = "../modules/pipeline"

  name_prefix = local.name_prefix
  aws_region  = var.aws_region
  account_id  = data.aws_caller_identity.current.account_id

  lambda_zip_path = data.archive_file.lambda_pipeline.output_path
  lambda_zip_hash = data.archive_file.lambda_pipeline.output_base64sha256
  lambda_environment = {
    DATABASE_URL_POOLED          = var.database_url_pooled
    S3_ENDPOINT                  = var.uploads_endpoint
    S3_REGION                    = var.uploads_region
    S3_BUCKET                    = var.uploads_bucket
    S3_ACCESS_KEY                = var.uploads_access_key
    S3_SECRET_KEY                = var.uploads_secret_key
    S3_USE_PATH_STYLE            = var.uploads_use_path_style
    S3_SCRATCH_REGION            = var.aws_region
    S3_SCRATCH_BUCKET            = aws_s3_bucket.scratch.bucket
    S3_SCRATCH_USE_PATH_STYLE    = "false"
    ENTITY_TYPES                 = var.entity_types
    ENTITY_EXTRACTION_BATCH_SIZE = "50"
  }

  stateless_lambda_zip_path = data.archive_file.lambda_stateless_jobs.output_path
  stateless_lambda_zip_hash = data.archive_file.lambda_stateless_jobs.output_base64sha256
  stateless_lambda_environment = {
    DATABASE_URL_POOLED = var.database_url_pooled
    LLM_PROVIDER        = "anthropic"
    ANTHROPIC_API_KEY   = var.anthropic_api_key
    ANTHROPIC_MODEL     = var.anthropic_model
  }

  # Uploads are on R2, reached with the keys above, so no AWS bucket grant.
  uploads_bucket_arn = null
  scratch_bucket_arn = aws_s3_bucket.scratch.arn

  batch = {
    embed_job_queue_arn        = data.aws_batch_job_queue.embedding.arn
    embed_job_definition_name  = var.embed_job_definition
    layout_job_queue_arn       = data.aws_batch_job_queue.embedding.arn
    layout_job_definition_name = var.layout_job_definition
    entity_job_queue_arn       = data.aws_batch_job_queue.gpu.arn
    entity_job_definition_name = var.entity_job_definition
  }
}
