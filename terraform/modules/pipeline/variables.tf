variable "name_prefix" {
  type        = string
  description = "Prefix for every resource name, e.g. dumpster-staging."
}

variable "aws_region" {
  type = string
}

variable "account_id" {
  type = string
}

variable "lambda_zip_path" {
  type        = string
  description = "Path to the zipped cmd/lambda-pipeline bootstrap binary (linux/arm64)."
}

variable "lambda_zip_hash" {
  type        = string
  description = "base64 SHA-256 of lambda_zip_path, so a code change redeploys the function."
}

variable "stateless_lambda_zip_path" {
  type        = string
  description = "Path to the zipped cmd/lambda-stateless-jobs bootstrap binary (linux/arm64)."
}

variable "stateless_lambda_zip_hash" {
  type = string
}

variable "stateless_lambda_environment" {
  type        = map(string)
  sensitive   = true
  description = "Environment for the stateless-jobs Lambda (database URL and Bedrock model)."
}

variable "bedrock_model_arns" {
  type        = list(string)
  default     = []
  description = "Bedrock inference profiles and models the stateless-jobs Lambda may invoke: the environment's own profile and every hop it routes through."
}

variable "lambda_environment" {
  type        = map(string)
  sensitive   = true
  description = "Environment for the pipeline Lambda (database URL, S3 buckets, entity types, ...). The state machine ARNs and queue URLs it publishes to are added by this module."
}

variable "uploads_bucket_arn" {
  type        = string
  default     = null
  description = "AWS bucket user documents live in, if they're on AWS S3: the pipeline Lambda reads uploads and presigns them for the layout job. Null when uploads are elsewhere (local dev uses Cloudflare R2 with keys in lambda_environment)."
}

variable "scratch_bucket_arn" {
  type        = string
  description = "Bucket for Batch jobs' input/result handoff objects."
}

variable "batch" {
  type = object({
    embed_job_queue_arn        = string
    embed_job_definition_name  = string
    layout_job_queue_arn       = string
    layout_job_definition_name = string
    entity_job_queue_arn       = string
    entity_job_definition_name = string
  })
  description = "AWS Batch queues and job definitions the state machines submit to."
}

variable "log_retention_days" {
  type    = number
  default = 60
}
