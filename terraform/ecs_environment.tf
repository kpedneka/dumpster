# Environment name threaded through every account/region- or VPC-scoped
# resource below, so "staging" and "production" can each be applied as a
# fully separate, real set of AWS resources from this same config, in the
# same account, without colliding on a uniqueness constraint AWS itself
# enforces -- ECR repo names, IAM role names, the ALB and its target
# group, CloudWatch log group paths, and Secrets Manager secret paths are
# all unique per account/region; security group names and subnet CIDR
# ranges are unique per VPC, and both environments share this account's
# one default VPC (see ecs_networking.tf).
#
# No default, on purpose: matches this file set's existing pattern for
# anything where guessing wrong would be a real mistake (r2_endpoint,
# r2_scratch_endpoint) -- there's no safe environment to assume, so
# `tofu apply` without -var="environment=..." fails loudly instead of
# silently reapplying whichever one ran last.

variable "environment" {
  type        = string
  description = "Deployment environment: \"staging\" or \"production\". Threaded into every resource name below so the two can coexist in this account without collision."

  validation {
    condition     = contains(["staging", "production"], var.environment)
    error_message = "environment must be \"staging\" or \"production\"."
  }
}

locals {
  name_prefix = "dumpster-${var.environment}"

  # Private subnet CIDRs, one pair per environment. Both environments
  # live in the same default VPC, so these ranges must not overlap --
  # picked from the same unused remainder of 172.31.0.0/16 the original
  # two blocks came from.
  private_subnet_cidrs = {
    production = ["172.31.96.0/20", "172.31.112.0/20"]
    staging    = ["172.31.128.0/20", "172.31.144.0/20"]
  }
}

# Moved here from the now-deleted ecs_inference.tf (the standalone
# inference service was fully retired once region classification and
# query embedding both moved off it -- see the "Eliminate the standalone
# inference service" and "Fold region classification into the AWS Batch
# embed job" dev board cards). Kept as a shared, top-level variable since
# every remaining task definition (api, worker, cleanup) still uses it.
variable "image_tag" {
  type        = string
  description = "Tag of the images to deploy, shared across all task definitions -- set by the CD pipeline to the commit SHA being deployed."
  default     = "latest"
}
