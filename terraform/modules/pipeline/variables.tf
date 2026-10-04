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

variable "lambda_environment" {
  type        = map(string)
  sensitive   = true
  description = "Environment for the pipeline Lambda (database URL, S3 buckets, entity types, ...). The state machine ARNs and queue URLs it publishes to are added by this module."
}

variable "uploads_bucket_arn" {
  type        = string
  description = "Bucket user documents live in. The Lambda reads uploads and presigns them for the layout job."
}

variable "scratch_bucket_arn" {
  type        = string
  description = "Bucket for Batch jobs' input/result handoff objects."
}

variable "downstream_queues" {
  type = object({
    edge_extraction_arn  = string
    edge_extraction_url  = string
    canonicalization_arn = string
    canonicalization_url = string
  })
  description = "The SQS queues the pipeline publishes edge extraction and canonicalization to."
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
