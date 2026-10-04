output "state_machine_arns" {
  value       = { for k, sm in aws_sfn_state_machine.this : k => sm.arn }
  description = "State machine ARN per job type (document_indexing, entity_extraction, region_classification)."
}

output "lambda_function_name" {
  value = aws_lambda_function.pipeline.function_name
}
