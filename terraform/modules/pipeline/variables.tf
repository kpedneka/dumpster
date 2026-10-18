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
  description = "Environment for the pipeline Lambda (database URL, S3 buckets, entity types, ...). The state machine ARNs it publishes to are added by this module."
}

variable "uploads_bucket_arn" {
  type        = string
  description = "AWS bucket user documents live in: the pipeline Lambda reads uploads and presigns them for the layout job, and malware_scan.tf scans it and owns its bucket policy."
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

variable "malware_scan_lambda_zip_path" {
  type        = string
  description = "Zip of cmd/lambda-malware-scan's bootstrap binary."
}

variable "malware_scan_lambda_zip_hash" {
  type = string
}

variable "malware_scan_timeout_seconds" {
  type        = number
  default     = 300
  description = "How long an upload waits for its malware scan before it's failed: the timeout state machine's Wait. Five minutes, against scans that finish in seconds, for uploads capped at 32 MiB (raise it if a real scan ever comes close)."

  validation {
    condition     = var.malware_scan_timeout_seconds > 0
    error_message = "The malware scan timeout must be positive."
  }
}

variable "vpc_config" {
  type = object({
    subnet_ids         = list(string)
    security_group_ids = list(string)
  })
  default     = null
  description = "Private subnets and security groups for the module's Lambdas, so their traffic goes through the VPC's NAT and egress controls. Null leaves them outside any VPC (the dev stack, which has none)."
}
