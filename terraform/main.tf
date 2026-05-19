# =====================================================================
# IICPC PLATFORM · TERRAFORM (AWS, single-VM deploy)
# ---------------------------------------------------------------------
# The simplest credible cloud deploy: one t3.large EC2 with a 100GB EBS
# volume, security group exposing :80 (Caddy) and :22 (admin), an
# instance profile that lets the VM pull from ECR. docker-compose
# brings the whole stack up via user_data.
#
# Why single-VM:
#   • Hackathon-budget friendly
#   • Demos the IaC story end-to-end without burning $500 on a managed
#     K8s control plane
#   • Trivially upgradable: change instance_type, or peel services into
#     ASGs (see modules/asg in a real deploy).
#
# For multi-node deployment, the K8s manifests in /k8s/ apply to any
# cluster (EKS/GKE/AKS/kind).
# =====================================================================

terraform {
  required_version = ">= 1.5"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = var.region
}

# ── Networking — use the default VPC; one subnet per AZ is enough ─────
data "aws_vpc" "default" {
  default = true
}

data "aws_subnets" "default" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default.id]
  }
}

# ── Security group ────────────────────────────────────────────────────
resource "aws_security_group" "iicpc" {
  name        = "${var.name_prefix}-sg"
  description = "IICPC platform — HTTP + admin SSH"
  vpc_id      = data.aws_vpc.default.id

  ingress {
    description = "HTTP (Caddy)"
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }
  ingress {
    description = "HTTPS (Caddy TLS)"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }
  ingress {
    description = "SSH (admin)"
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = var.admin_cidrs
  }
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = local.tags
}

# ── IAM: let the VM pull from ECR (private image distribution) ────────
data "aws_iam_policy_document" "assume_ec2" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "iicpc" {
  name               = "${var.name_prefix}-role"
  assume_role_policy = data.aws_iam_policy_document.assume_ec2.json
  tags               = local.tags
}

resource "aws_iam_role_policy_attachment" "ecr_read" {
  role       = aws_iam_role.iicpc.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly"
}

resource "aws_iam_role_policy_attachment" "ssm" {
  # SSM Session Manager — sidesteps SSH entirely for ops convenience.
  role       = aws_iam_role.iicpc.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_instance_profile" "iicpc" {
  name = "${var.name_prefix}-instance-profile"
  role = aws_iam_role.iicpc.name
}

# ── Latest Amazon Linux 2023 AMI ──────────────────────────────────────
data "aws_ami" "al2023" {
  most_recent = true
  owners      = ["amazon"]
  filter {
    name   = "name"
    values = ["al2023-ami-2023.*-x86_64"]
  }
}

# ── EC2 instance + EBS ────────────────────────────────────────────────
resource "aws_instance" "iicpc" {
  ami                    = data.aws_ami.al2023.id
  instance_type          = var.instance_type
  subnet_id              = data.aws_subnets.default.ids[0]
  vpc_security_group_ids = [aws_security_group.iicpc.id]
  iam_instance_profile   = aws_iam_instance_profile.iicpc.name
  key_name               = var.ssh_key_name

  root_block_device {
    volume_size           = var.root_disk_gb
    volume_type           = "gp3"
    encrypted             = true
    delete_on_termination = true
  }

  user_data = templatefile("${path.module}/cloud-init.yaml", {
    repo_url = var.repo_url
  })

  tags = merge(local.tags, { Name = "${var.name_prefix}-host" })
}

# ── Elastic IP for stable URL ─────────────────────────────────────────
resource "aws_eip" "iicpc" {
  instance = aws_instance.iicpc.id
  domain   = "vpc"
  tags     = local.tags
}

locals {
  tags = {
    Project   = "iicpc-platform"
    ManagedBy = "terraform"
    Env       = var.environment
  }
}
