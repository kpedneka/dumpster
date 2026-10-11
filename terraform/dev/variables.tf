variable "aws_region" {
  type    = string
  default = "us-east-1"
}

# --- From .env.local (passed by `make dev-deploy`) ---

variable "database_url_pooled" {
  type        = string
  sensitive   = true
  description = "DATABASE_URL_POOLED from .env.local: the Lambdas must use the database the local API reads."
}

variable "entity_types" {
  type        = string
  default     = ""
  description = "ENTITY_TYPES from .env.local; empty uses the app's default set."
}

variable "uploads_endpoint" {
  type        = string
  description = "S3_ENDPOINT from .env.local (Cloudflare R2)."
}

variable "uploads_region" {
  type    = string
  default = "auto"
}

variable "uploads_bucket" {
  type = string
}

variable "uploads_access_key" {
  type      = string
  sensitive = true
}

variable "uploads_secret_key" {
  type      = string
  sensitive = true
}

variable "uploads_use_path_style" {
  type    = string
  default = "true"
}

# --- Long-lived Batch resources (not managed here) ---

variable "embed_job_queue" {
  type    = string
  default = "dumpster-embedding-queue"
}

variable "embed_job_definition" {
  type    = string
  default = "dumpster-embedding-jobdef"
}

variable "layout_job_definition" {
  type        = string
  default     = "dumpster-region-extraction"
  description = "Runs on embed_job_queue, as on the main stack."
}

variable "entity_job_queue" {
  type    = string
  default = "dumpster-batch-gpu-pilot-queue"
}

variable "entity_job_definition" {
  type    = string
  default = "dumpster-entity-extraction"
}

variable "local_iam_user" {
  type        = string
  default     = "dumpster-worker-batch"
  description = "The IAM user whose keys are in .env.local; the local API publishes jobs with them."
}
