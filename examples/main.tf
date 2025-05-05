terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = "eu-west-1"
}

# Get latest Amazon Linux 2023 AMI
data "aws_ami" "amazon_linux" {
  most_recent = true
  owners      = ["amazon"]

  filter {
    name   = "name"
    values = ["al2023-ami-*-x86_64"]
  }

  filter {
    name   = "virtualization-type"
    values = ["hvm"]
  }
}

# Get default VPC details for reference
data "aws_vpc" "default" {
  default = true
}

# Get default subnet in the first AZ
data "aws_subnets" "default" {
  filter {
    name   = "vpc-id"
    values = [data.aws_vpc.default.id]
  }
  filter {
    name   = "default-for-az"
    values = ["true"]
  }
}

# Random suffix to ensure unique resource names
resource "random_string" "suffix" {
  length  = 8
  special = false
  upper   = false
}

# Security group for the EC2 instance
resource "aws_security_group" "demo_sg" {
  name        = "drift-demo-sg-${random_string.suffix.result}"
  description = "Security group for drift detection demo"
  vpc_id      = data.aws_vpc.default.id

  # SSH access
  ingress {
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
    description = "SSH"
  }

  # HTTP access
  ingress {
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
    description = "HTTP"
  }

  # Outbound internet access
  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
    description = "All outbound traffic"
  }

  tags = {
    Name = "drift-demo-sg-${random_string.suffix.result}"
    TFResourceAddress = "aws_security_group.demo_sg"
  }
}

# EC2 instance for drift detection demo
resource "aws_instance" "demo_instance" {
  ami                    = data.aws_ami.amazon_linux.id
  instance_type          = "t2.micro"
  vpc_security_group_ids = [aws_security_group.demo_sg.id]
  subnet_id              = length(data.aws_subnets.default.ids) > 0 ? tolist(data.aws_subnets.default.ids)[0] : null

  root_block_device {
    volume_size = 30  # Increased from 8 to 30 to meet AMI requirements
    volume_type = "gp3"
    encrypted   = true
  }

  tags = {
    Name = "drift-demo-${random_string.suffix.result}"
    Environment = "Testing"
    Purpose = "Drift Detection Demo"
    TFResourceAddress = "aws_instance.demo_instance"
  }
}

# S3 bucket for drift detection demo
resource "aws_s3_bucket" "demo_bucket" {
  bucket_prefix = "drift-demo-"
  
  tags = {
    Name = "drift-demo-${random_string.suffix.result}"
    Environment = "Testing"
    Purpose = "Drift Detection Demo"
    TFResourceAddress = "aws_s3_bucket.demo_bucket"
  }
}

# Block public access to the bucket
resource "aws_s3_bucket_public_access_block" "demo_bucket_block" {
  bucket = aws_s3_bucket.demo_bucket.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

# Configure simple lifecycle rule
resource "aws_s3_bucket_lifecycle_configuration" "demo_bucket_lifecycle" {
  bucket = aws_s3_bucket.demo_bucket.id

  rule {
    id     = "expire-old-objects"
    status = "Enabled"

    filter {
      prefix = ""  # Apply to all objects
    }

    expiration {
      days = 365
    }
  }
}

# Configure server-side encryption for the bucket
resource "aws_s3_bucket_server_side_encryption_configuration" "demo_bucket_encryption" {
  bucket = aws_s3_bucket.demo_bucket.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# Output the resource IDs
output "instance_id" {
  value       = aws_instance.demo_instance.id
  description = "The ID of the EC2 instance"
}

output "instance_public_ip" {
  value       = aws_instance.demo_instance.public_ip
  description = "The public IP address of the EC2 instance"
}

output "security_group_id" {
  value       = aws_security_group.demo_sg.id
  description = "The ID of the security group"
}

output "bucket_name" {
  value       = aws_s3_bucket.demo_bucket.id
  description = "The name of the S3 bucket"
}

output "bucket_arn" {
  value       = aws_s3_bucket.demo_bucket.arn
  description = "The ARN of the S3 bucket"
}