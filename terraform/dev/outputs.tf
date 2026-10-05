output "dotenv" {
  description = "Lines for .env.local so the local API publishes to this stack (`make dev-env` prints them)."
  value       = <<-EOT
    DOCUMENT_INDEXING_STATE_MACHINE_ARN=${module.pipeline.state_machine_arns["document_indexing"]}
    ENTITY_EXTRACTION_STATE_MACHINE_ARN=${module.pipeline.state_machine_arns["entity_extraction"]}
    REGION_CLASSIFICATION_STATE_MACHINE_ARN=${module.pipeline.state_machine_arns["region_classification"]}
    EDGE_EXTRACTION_QUEUE_URL=${module.pipeline.job_queue_urls["edge_extraction"]}
    CANONICALIZATION_QUEUE_URL=${module.pipeline.job_queue_urls["canonicalization"]}
    S3_SCRATCH_REGION=${var.aws_region}
    S3_SCRATCH_BUCKET=${aws_s3_bucket.scratch.bucket}
    S3_SCRATCH_USE_PATH_STYLE=false
  EOT
}
