# The pipeline-only dev stack: real Step Functions state machines and
# Lambdas for local development (`make run`) to publish jobs to, so local
# runs the same pipeline staging and production do, with no
# development-only orchestration code. See the "Pipeline-only dev stack in
# AWS + make dev-deploy" dev board card.
#
# Only the pipeline (terraform/modules/pipeline) plus uploads and scratch
# buckets: no VPC, ECS, ALB or CloudFront, and nothing that bills while
# idle. Everything else local dev already uses stays as it is:
#   - the database is the one .env.local points at;
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

# --- Uploads bucket: documents uploaded through the local API ---
#
# The dev counterpart of the environments' documents bucket
# (../s3_storage.tf), with the same hardening. On AWS S3 rather than
# Cloudflare R2 so dev runs the same storage path staging and production
# do, and so S3-based protections (such as malware scanning) apply here
# too. Dev data is disposable, hence force_destroy.

resource "aws_s3_bucket" "uploads" {
  bucket        = "${local.name_prefix}-uploads-${data.aws_caller_identity.current.account_id}"
  force_destroy = true
}

resource "aws_s3_bucket_public_access_block" "uploads" {
  bucket                  = aws_s3_bucket.uploads.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "uploads" {
  bucket = aws_s3_bucket.uploads.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# Backstop only, as in ../s3_storage.tf: the session sweep deletes a
# session's objects before its row.
resource "aws_s3_bucket_lifecycle_configuration" "uploads" {
  bucket = aws_s3_bucket.uploads.id
  rule {
    id     = "orphan-object-backstop"
    status = "Enabled"
    filter {}
    expiration {
      days = 2
    }
  }
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

data "archive_file" "lambda_malware_scan" {
  type        = "zip"
  source_file = "${path.module}/../../build/lambda-malware-scan/bootstrap"
  output_path = "${path.module}/../../build/dev/lambda-malware-scan.zip"
}

# The dev environment's own tagged Bedrock inference profile, for the
# stateless-jobs Lambda and the local API (`make dev-env` prints its ARN
# for .env.local). Tagged environment=dev so this usage stays out of
# production's Bedrock budget (../bedrock_budget.tf).
resource "aws_bedrock_inference_profile" "dev" {
  name        = local.name_prefix
  description = "dumpster dev Claude Sonnet 5 tagged for cost tracking"

  model_source {
    copy_from = "arn:aws:bedrock:${var.aws_region}:${data.aws_caller_identity.current.account_id}:inference-profile/us.anthropic.claude-sonnet-5"
  }

  tags = {
    environment = "dev"
    app         = "dumpster"
  }
}

locals {
  # The profile and every hop it routes through; Bedrock checks permission
  # at each one (see ../ecs_iam.tf).
  bedrock_model_arns = [
    aws_bedrock_inference_profile.dev.arn,
    "arn:aws:bedrock:${var.aws_region}:${data.aws_caller_identity.current.account_id}:inference-profile/us.anthropic.claude-sonnet-5",
    "arn:aws:bedrock:*::foundation-model/anthropic.claude-sonnet-5",
  ]
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
    S3_REGION                    = var.aws_region
    S3_BUCKET                    = aws_s3_bucket.uploads.bucket
    S3_USE_PATH_STYLE            = "false"
    S3_SCRATCH_REGION            = var.aws_region
    S3_SCRATCH_BUCKET            = aws_s3_bucket.scratch.bucket
    S3_SCRATCH_USE_PATH_STYLE    = "false"
    ENTITY_TYPES                 = var.entity_types
    ENTITY_EXTRACTION_BATCH_SIZE = "50"
  }

  malware_scan_lambda_zip_path = data.archive_file.lambda_malware_scan.output_path
  malware_scan_lambda_zip_hash = data.archive_file.lambda_malware_scan.output_base64sha256

  stateless_lambda_zip_path = data.archive_file.lambda_stateless_jobs.output_path
  stateless_lambda_zip_hash = data.archive_file.lambda_stateless_jobs.output_base64sha256
  stateless_lambda_environment = {
    DATABASE_URL_POOLED = var.database_url_pooled
    BEDROCK_MODEL_ID    = aws_bedrock_inference_profile.dev.arn
  }
  bedrock_model_arns = local.bedrock_model_arns

  uploads_bucket_arn = aws_s3_bucket.uploads.arn
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

# --- Local API access ---
#
# After cutover the local API (`make run`) publishes jobs to this stack
# with the AWS keys in .env.local, which belong to an existing IAM user
# that isn't managed here. Grant that user exactly what the deployed API's
# task role has for publishing jobs, storing uploads and calling Bedrock.
data "aws_iam_user" "local" {
  user_name = var.local_iam_user
}

data "aws_iam_policy_document" "local_publish" {
  statement {
    sid       = "StartPipelineExecutions"
    actions   = ["states:StartExecution"]
    resources = values(module.pipeline.state_machine_arns)
  }
  statement {
    sid       = "UploadsObjects"
    actions   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"]
    resources = ["${aws_s3_bucket.uploads.arn}/*"]
  }
  statement {
    sid       = "UploadsList"
    actions   = ["s3:ListBucket"]
    resources = [aws_s3_bucket.uploads.arn]
  }
  statement {
    sid = "InvokeBedrock"
    actions = [
      "bedrock:InvokeModel",
      "bedrock:InvokeModelWithResponseStream",
      "bedrock:Converse",
      "bedrock:ConverseStream",
    ]
    resources = local.bedrock_model_arns
  }
}

# A managed policy, not an inline one: IAM caps a user's inline policies at
# 2,048 bytes combined, and this user's existing Batch access already uses
# most of that.
resource "aws_iam_policy" "local_publish" {
  name   = "${local.name_prefix}-local-publish"
  policy = data.aws_iam_policy_document.local_publish.json
}

resource "aws_iam_user_policy_attachment" "local_publish" {
  user       = data.aws_iam_user.local.user_name
  policy_arn = aws_iam_policy.local_publish.arn
}
