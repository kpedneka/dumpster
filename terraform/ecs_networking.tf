# Networking for the ECS migration: two new private subnets for api/worker
# tasks, plus a single small NAT instance for their outbound internet
# access (ECR pulls, Secrets Manager, CloudWatch Logs, Batch API, and --
# since Postgres is staying on Neon for now, see the ECS Migration dev
# board's cost discussion -- the app's actual database traffic too).
#
# Reuses the account's existing default VPC (vpc-b46ec5cd) rather than
# creating a new one: every AWS Batch compute environment already in this
# account (dumpster-batch-gpu-pilot, dumpster-embedding-ce,
# dumpster-embed-bench-ce) runs in it, and splitting api/worker into a
# separate VPC would need peering or shared resources to reach the same
# Batch queues for no real benefit. That VPC's existing 6 subnets are all
# public, though (confirmed directly via `aws ec2 describe-subnets` --
# this account has no private subnets today) -- these two new ones are
# private on purpose, so api/worker tasks have no direct internet route at
# all, only reachable via the ALB inbound and the NAT instance outbound.
#
# Single NAT instance, not a NAT Gateway or one-per-AZ: matches this
# project's existing "accept simpler tradeoffs at personal scale" posture
# (see the Cost Comparison page's own NAT instance vs. NAT Gateway
# reasoning) -- a NAT Gateway is ~10x the cost for redundancy this app's
# actual traffic doesn't need yet. Single point of failure, accepted
# deliberately, not an oversight.

data "aws_vpc" "default" {
  id = "vpc-b46ec5cd"
}

# The two existing public subnets a NAT instance and private-subnet
# route table live alongside -- picked to match the AZs the new private
# subnets use below, so each private subnet's default route stays within
# the same AZ as its NAT path (cheaper and lower-latency than crossing
# AZs for every outbound packet).
data "aws_subnet" "public_1a" {
  id = "subnet-f59bf9bd" # us-east-1a
}

data "aws_subnet" "public_1b" {
  id = "subnet-fe13bea4" # us-east-1b
}

# New private subnets. CIDR ranges picked from the unused remainder of
# the default VPC's 172.31.0.0/16 -- the existing 6 public subnets only
# occupy 6 of the 16 possible /20 blocks in that range, so this doesn't
# touch anything already in use. Ranges come from local.private_subnet_cidrs
# (ecs_environment.tf) so staging and production don't overlap when both
# exist in this same VPC at once.
resource "aws_subnet" "private_1a" {
  vpc_id            = data.aws_vpc.default.id
  cidr_block        = local.private_subnet_cidrs[var.environment][0]
  availability_zone = "us-east-1a"

  tags = {
    Name = "${local.name_prefix}-private-1a"
  }
}

resource "aws_subnet" "private_1b" {
  vpc_id            = data.aws_vpc.default.id
  cidr_block        = local.private_subnet_cidrs[var.environment][1]
  availability_zone = "us-east-1b"

  tags = {
    Name = "${local.name_prefix}-private-1b"
  }
}

# --- NAT instance ---
#
# A plain Amazon Linux 2023 instance configured as a NAT via user_data,
# not one of the old marketplace "NAT AMI" listings (deprecated) -- this
# is the current standard DIY approach. source_dest_check must be
# disabled, or the instance drops any packet not addressed to itself,
# which is the entire point of a NAT.

# arm64, matching var.nat_instance_type's default (t4g.nano, a Graviton
# family) -- an earlier architecture mismatch here was caught by a real
# `tofu apply` (RunInstances rejects an AMI/instance-type mismatch),
# not by `tofu plan`/`validate`, which don't cross-check the two at all.
#
# Uses AWS's own SSM parameter for "the latest base AL2023 AMI",
# specifically -- not a name-wildcard `data.aws_ami` filter. A real,
# observed failure: "al2023-ami-*-arm64" also matches the ECS-optimized
# variant (al2023-ami-ecs-hvm-...-arm64) and several others (minimal,
# EKS-optimized), and `most_recent = true` silently picked the
# ECS-optimized one -- which auto-runs Docker and the ECS container
# agent on boot. That agent has no cluster to register with, and Docker
# managing its own iptables rules alongside this instance's hand-written
# NAT rules on a t4g.nano is a real, plausible source of the
# intermittent connectivity failures this caused (confirmed live via
# SSM: the instance's iptables nat table had Docker/ECS-agent chains
# -- the 169.254.170.2 credential-proxy DNAT rule -- that a plain NAT
# instance has no business running). This path names the exact,
# non-ECS, non-minimal base image and can't drift onto a different
# image family the way a wildcard can.
data "aws_ssm_parameter" "al2023_arm64" {
  name = "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64"
}

resource "aws_security_group" "nat_instance" {
  name        = "${local.name_prefix}-nat-instance"
  description = "Allows inbound traffic from the private subnets only, for NAT forwarding"
  vpc_id      = data.aws_vpc.default.id

  ingress {
    description = "All traffic from the private subnets needing outbound internet access"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = [aws_subnet.private_1a.cidr_block, aws_subnet.private_1b.cidr_block]
  }

  egress {
    description = "NAT forwards outbound to the internet"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Name = "${local.name_prefix}-nat-instance"
  }
}

# SSM access for the NAT instance -- added after a real debugging dead
# end: this instance has no SSH key and no console output ever populated
# for a freshly-replaced instance (a real, observed AWS quirk, not
# assumed), leaving no way at all to inspect live network/iptables state
# when something's wrong. SSM avoids the alternatives this project has
# otherwise steered away from (an open port 22, a managed SSH key pair)
# -- no inbound port at all, auth via IAM instead of a keypair.
data "aws_iam_policy_document" "nat_ssm_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "nat_ssm" {
  name               = "${local.name_prefix}-nat-ssm"
  assume_role_policy = data.aws_iam_policy_document.nat_ssm_assume.json
}

resource "aws_iam_role_policy_attachment" "nat_ssm" {
  role       = aws_iam_role.nat_ssm.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_instance_profile" "nat_ssm" {
  name = "${local.name_prefix}-nat-ssm"
  role = aws_iam_role.nat_ssm.name
}

resource "aws_instance" "nat" {
  ami                    = data.aws_ssm_parameter.al2023_arm64.value
  instance_type          = var.nat_instance_type
  subnet_id              = data.aws_subnet.public_1a.id
  vpc_security_group_ids = [aws_security_group.nat_instance.id]
  iam_instance_profile   = aws_iam_instance_profile.nat_ssm.name
  source_dest_check      = false # required for any NAT instance -- otherwise it drops forwarded traffic

  user_data = <<-EOF
    #!/bin/bash
    set -e
    sysctl -w net.ipv4.ip_forward=1
    echo "net.ipv4.ip_forward = 1" > /etc/sysctl.d/99-nat.conf
    # The base AL2023 AMI doesn't ship iptables at all -- confirmed live
    # via SSM ("iptables: command not found") once the AMI source was
    # fixed to stop resolving to the ECS-optimized variant, which does
    # bundle it (for Docker's own use). iptables-nft is AL2023's modern,
    # nftables-backed implementation of the iptables command;
    # iptables-services provides the systemd unit the "systemctl enable
    # iptables" line below depends on for persisting the rule at boot.
    # Without this line, `set -e` aborted the whole script the moment it
    # reached the first iptables call -- ip_forward was already enabled
    # by then (it runs earlier), but MASQUERADE was never added and
    # nothing after that line, including the iptables-save persistence
    # step, ever ran.
    dnf install -y iptables-nft iptables-services
    # AL2023 on Nitro-based instances (every current generation, t4g
    # included) uses predictable network interface naming and names the
    # primary ENI "ens5", not "eth0" -- a real bug caught only by a live
    # apply: hardcoding eth0 here made the MASQUERADE rule match nothing,
    # so packets got forwarded but never had their source address
    # rewritten, and return traffic from the internet had nowhere to go
    # back to. Resolving the actual default-route interface at boot
    # avoids hardcoding a name that's wrong on this AMI generation (or
    # the next one).
    IFACE=$(ip -o -4 route show to default | awk '{print $5}')
    iptables -t nat -A POSTROUTING -o "$IFACE" -j MASQUERADE
    iptables-save > /etc/sysconfig/iptables
    systemctl enable iptables 2>/dev/null || true
  EOF

  tags = {
    Name = "${local.name_prefix}-nat-instance"
  }
}

variable "nat_instance_type" {
  type        = string
  description = "Instance type for the NAT instance. t4g.nano is the cost baseline this was priced against; bump to t4g.micro/small if it can't keep up with real sustained traffic (Neon DB queries especially) -- cheap to resize, not worth over-provisioning up front."
  default     = "t4g.nano"
}

# --- Private route table: 0.0.0.0/0 -> the NAT instance's network interface ---

resource "aws_route_table" "private" {
  vpc_id = data.aws_vpc.default.id

  tags = {
    Name = "${local.name_prefix}-private"
  }
}

# A separate aws_route resource, not an inline `route` block on the table
# above -- the inline form is known to fight any other process managing
# routes on the same table. Routes through network_interface_id, not
# instance_id -- this AWS provider version rejects instance_id as a route
# target entirely (caught by tofu validate, not assumed); an
# instance-targeted route is actually modeled against the instance's
# primary ENI underneath, and only that ENI reference is accepted as
# input now.
resource "aws_route" "private_to_nat" {
  route_table_id         = aws_route_table.private.id
  destination_cidr_block = "0.0.0.0/0"
  network_interface_id   = aws_instance.nat.primary_network_interface_id
}

resource "aws_route_table_association" "private_1a" {
  subnet_id      = aws_subnet.private_1a.id
  route_table_id = aws_route_table.private.id
}

resource "aws_route_table_association" "private_1b" {
  subnet_id      = aws_subnet.private_1b.id
  route_table_id = aws_route_table.private.id
}

# --- Security groups for the ALB and the ECS tasks behind it ---
#
# Task port is 8080, matching cmd/api's HTTPPort default -- see
# internal/config/config.go.

# CloudFront's own origin-facing IP ranges, AWS-managed and kept up to date
# by AWS itself -- the standard way to scope an origin's security group to
# "CloudFront only" without hardcoding or maintaining an IP list by hand.
# Both environments' ALBs are internal now (no public IP at all -- see
# ecs_alb.tf's VPC-origins migration), so this is defense-in-depth on top
# of that private network path, not the actual enforcement -- but harmless
# and cheap to keep, and it also feeds ecs_alb.tf's origin-verification
# header story.
#
# There used to be a second ingress source here too: var.staging_allowed_cidrs,
# an IP allowlist for staging.yml's direct-ALB smoke test (bypassing
# CloudFront on purpose to test the backend independently). Removed
# entirely once the ALB went internal -- there's no public IP left for any
# CIDR to reach, so the variable, its validation, and the CI logic that
# computed it (staging.yml's "Compute allowed CIDRs" step and
# teardown-on-unlabel's "Compute this runner's public IP" step, which only
# existed to satisfy that variable's validation during `tofu destroy`)
# were all genuinely dead, not just simplified.
data "aws_ec2_managed_prefix_list" "cloudfront" {
  name = "com.amazonaws.global.cloudfront.origin-facing"
}

resource "aws_security_group" "alb" {
  # name_prefix, not a fixed name -- real bug hit on the first apply of this
  # description text: AWS requires unique SG names per VPC, and this
  # resource's own description change forces replacement (AWS won't let you
  # update a security group's description in place). With a fixed name and
  # no create_before_destroy, Terraform's default destroy-then-create order
  # tried to delete the old SG before the new one existed to take over the
  # ALB's/ecs_tasks's references to it -- a genuine ordering deadlock
  # (DependencyViolation), not a transient AWS timing issue. name_prefix
  # lets AWS generate a unique suffix so the new SG can exist briefly
  # alongside the old one.
  name_prefix = "${local.name_prefix}-alb-"
  description = "CloudFront origin-facing IPs only -- both ALBs are internal now, see ecs_alb.tf."
  vpc_id      = data.aws_vpc.default.id

  lifecycle {
    create_before_destroy = true
  }

  ingress {
    description     = "HTTPS -- CloudFronts origin-facing prefix list only"
    from_port       = 443
    to_port         = 443
    protocol        = "tcp"
    prefix_list_ids = [data.aws_ec2_managed_prefix_list.cloudfront.id]
  }

  egress {
    description = "To the api tasks only"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Name = "${local.name_prefix}-alb"
  }
}

resource "aws_security_group" "ecs_tasks" {
  name        = "${local.name_prefix}-ecs-tasks"
  description = "api/worker tasks: inbound only from the ALB, outbound to the NAT path for everything else"
  vpc_id      = data.aws_vpc.default.id

  ingress {
    description     = "From the ALB only"
    from_port       = 8080
    to_port         = 8080
    protocol        = "tcp"
    security_groups = [aws_security_group.alb.id]
  }

  egress {
    description = "Outbound via the NAT instance (Neon, ECR, Secrets Manager, CloudWatch, Batch API)"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = {
    Name = "${local.name_prefix}-ecs-tasks"
  }
}

output "private_subnet_ids" {
  value       = [aws_subnet.private_1a.id, aws_subnet.private_1b.id]
  description = "Pass these to the ECS service/task definitions in ecs_api.tf / ecs_worker.tf"
}

output "alb_security_group_id" {
  value = aws_security_group.alb.id
}

output "ecs_tasks_security_group_id" {
  value = aws_security_group.ecs_tasks.id
}

output "nat_instance_public_ip" {
  value       = aws_instance.nat.public_ip
  description = "Useful for verifying real NAT traffic is flowing once applied"
}

output "nat_instance_id" {
  value       = aws_instance.nat.id
  description = "Target for scripts/wait_for_nat_ready.sh's SSM readiness check -- the NAT instance isn't confirmable via routing alone (tofu apply doesn't wait for its user_data to finish), so this lets a caller poll it directly."
}
