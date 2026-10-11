output "state_machine_arns" {
  value       = { for k, sm in aws_sfn_state_machine.this : k => sm.arn }
  description = "State machine ARN per job type (document_indexing, entity_extraction, region_classification)."
}

output "lambda_function_name" {
  value = aws_lambda_function.pipeline.function_name
}

output "job_queue_urls" {
  value       = { for k, q in aws_sqs_queue.job : k => q.url }
  description = "SQS queue URL per Lambda job type (edge_extraction, canonicalization)."
}

output "job_queue_arns" {
  value = { for k, q in aws_sqs_queue.job : k => q.arn }
}

output "malware_scan_timeout_queue_url" {
  value       = aws_sqs_queue.malware_scan_timeout.url
  description = "Where the API sends each upload's malware scan timeout message (MALWARE_SCAN_TIMEOUT_QUEUE_URL)."
}

output "malware_scan_timeout_queue_arn" {
  value = aws_sqs_queue.malware_scan_timeout.arn
}
