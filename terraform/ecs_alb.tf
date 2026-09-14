# The api's ALB, target group, and HTTPS listener -- internal, not
# public-facing. Only api sits behind this -- worker and inference are
# never reached directly from outside the cluster (worker pulls from the
# queue; inference is called internally by api/worker over Service
# Connect, see ecs_inference.tf).
#
# internal = true, in the private subnets, reached only via CloudFront's
# VPC origin (frontend.tf) -- not the public-facing ALB + secret-header
# design this replaced. That design (kept working, verified for real
# against production) was a real, working defense-in-depth, but a
# *policy* guarantee: the ALB still had a public IP, technically part of
# internet routing space, protected by a security-group prefix list and a
# secret header that had to stay correctly configured. A private ALB is a
# *structural* guarantee instead -- there is no route to it from the
# internet at all, full stop, nothing to misconfigure away. AWS's own
# restrict-access-to-load-balancer.html docs list this (VPC origins) as
# the first, preferred option, with the custom-header approach as the
# fallback for when you can't move to private subnets -- we can, so this
# is the properly-done version, not a settled-for one.
#
# name_prefix + create_before_destroy, learned directly from tonight's
# real security-group replacement deadlock (ecs_networking.tf): `internal`
# and `subnets` are both ForceNew on aws_lb, and a fixed name would hit
# the exact same "can't create the new one until the old one's name frees
# up, can't free up the old one while things still reference it" ordering
# problem this fixes for the security group.
resource "aws_lb" "api" {
  name_prefix        = "api-"
  internal           = true
  load_balancer_type = "application"
  security_groups    = [aws_security_group.alb.id]
  subnets            = [aws_subnet.private_1a.id, aws_subnet.private_1b.id]

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_lb_target_group" "api" {
  name        = "${local.name_prefix}-api"
  port        = 8080
  protocol    = "HTTP"
  vpc_id      = data.aws_vpc.default.id
  target_type = "ip" # required for Fargate -- tasks are addressed by ENI IP, not instance ID

  health_check {
    path                = "/healthz"
    healthy_threshold   = 2
    unhealthy_threshold = 3
    interval            = 15
    timeout             = 5
    matcher             = "200"
  }
}

# The ACM certificate itself is still a console-only, not-Terraform step
# (validation needs DNS control at apply time, per the original infra
# plan's explicit exceptions) -- but looked up by domain here, not
# hardcoded by ARN. A real bug this avoids structurally, not just by
# convention: staging.tfvars once held a stale ARN for a certificate
# issued for the wrong hostname (staging-api.kpednekar.dev, an earlier
# placeholder name, not the real dumpster-staging.kpednekar.dev) --
# nothing caught that until a live TLS handshake failed, since an ARN is
# just an opaque string to Terraform with no relationship to the
# hostname it's actually used for. Looking it up by the domain the ALB
# is meant to serve makes that specific class of staleness impossible:
# whatever cert is ISSUED for that exact domain is what gets attached,
# every time.
data "aws_acm_certificate" "this" {
  domain      = var.acm_domain_name
  statuses    = ["ISSUED"]
  most_recent = true
}

# Default action is a fixed 403, not a forward -- see aws_lb_listener_rule.
# from_cloudfront below for the actual forwarding rule and why. This is the
# AWS-documented pattern for CloudFront-only ALB access (confirmed directly
# against restrict-access-to-load-balancer.html): only a request carrying
# the CloudFront-injected secret header gets forwarded; everything else,
# including a request that came through CloudFront's own IP ranges but
# wasn't actually sent by this distribution, hits this default and gets
# rejected.
resource "aws_lb_listener" "https" {
  load_balancer_arn = aws_lb.api.arn
  port              = 443
  protocol          = "HTTPS"
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"
  certificate_arn   = data.aws_acm_certificate.this.arn

  default_action {
    type = "fixed-response"
    fixed_response {
      content_type = "text/plain"
      message_body = "Access denied"
      status_code  = "403"
    }
  }
}

# Only requests carrying frontend.tf's CloudFront-injected secret header get
# forwarded to the real target group -- the actual enforcement half of the
# CloudFront-only restriction described on aws_lb_listener.https above.
# Priority 1 (evaluated before the listener's own default action, which
# only ever applies when no rule matches).
resource "aws_lb_listener_rule" "from_cloudfront" {
  listener_arn = aws_lb_listener.https.arn
  priority     = 1

  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.api.arn
  }

  condition {
    http_header {
      http_header_name = local.cloudfront_origin_header_name
      values           = [random_password.cloudfront_origin_secret.result]
    }
  }
}

# Plain HTTP redirects to HTTPS rather than serving anything -- no reason
# to accept unencrypted traffic even transiently.
resource "aws_lb_listener" "http_redirect" {
  load_balancer_arn = aws_lb.api.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type = "redirect"
    redirect {
      port        = "443"
      protocol    = "HTTPS"
      status_code = "HTTP_301"
    }
  }
}

variable "acm_domain_name" {
  type        = string
  description = "Exact domain name of this environment's validated, ISSUED ACM certificate (e.g. dumpster-staging.kpednekar.dev for staging) -- used to look the certificate up by data.aws_acm_certificate rather than hardcoding its ARN, so a stale or wrong-domain ARN can't silently get attached to the listener. No default: must differ between environments, and must exactly match a real certificate's domain name."
}

output "alb_dns_name" {
  value       = aws_lb.api.dns_name
  description = "Point the api. DNS record at this (or at the ALB's zone_id/dns_name via an ALIAS record if using Route 53)."
}
