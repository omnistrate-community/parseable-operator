# Per-instance object store for a Parseable cluster, provisioned in the
# customer's AWS account. Omnistrate renders the {{ $sys.* }} expressions
# before running OpenTofu; every output is consumable by the parseableCluster
# resource as {{ $parseableStorage.out.<name> }}.

terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.0"
    }
    random = {
      source  = "hashicorp/random"
      version = ">= 3.5"
    }
  }
}

provider "aws" {
  region = "{{ $sys.deploymentCell.region }}"
}

locals {
  # $sys.id is unique per instance; bucket and IAM names share the
  # "parseable-" prefix that the CUSTOM_TERRAFORM_POLICY is scoped to.
  name = substr(lower("parseable-{{ $sys.id }}"), 0, 63)
  tags = {
    "omnistrate.com/instance-id" = "{{ $sys.id }}"
    "managed-by"                 = "omnistrate"
    "app"                        = "parseable"
  }
}

resource "aws_s3_bucket" "data" {
  bucket = local.name
  # Instance deletion must be able to remove the bucket; deleting the
  # Parseable instance deletes its stored logs.
  force_destroy = true
  tags          = local.tags
}

resource "aws_s3_bucket_public_access_block" "data" {
  bucket                  = aws_s3_bucket.data.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_ownership_controls" "data" {
  bucket = aws_s3_bucket.data.id
  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "data" {
  bucket = aws_s3_bucket.data.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_iam_user" "parseable" {
  name = local.name
  tags = local.tags
}

resource "aws_iam_user_policy" "parseable" {
  name = "parseable-bucket-access"
  user = aws_iam_user.parseable.name
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect   = "Allow"
        Action   = ["s3:ListBucket", "s3:GetBucketLocation", "s3:ListBucketMultipartUploads"]
        Resource = aws_s3_bucket.data.arn
      },
      {
        Effect = "Allow"
        Action = [
          "s3:GetObject",
          "s3:PutObject",
          "s3:DeleteObject",
          "s3:AbortMultipartUpload",
          "s3:ListMultipartUploadParts",
        ]
        Resource = "${aws_s3_bucket.data.arn}/*"
      },
    ]
  })
}

resource "aws_iam_access_key" "parseable" {
  user = aws_iam_user.parseable.name
}

# Shared secret authenticating intra-cluster requests between Parseable nodes.
resource "random_password" "cluster_secret" {
  length  = 32
  special = false
}

output "bucket" {
  value = aws_s3_bucket.data.id
}

output "region" {
  value = aws_s3_bucket.data.region
}

output "s3_url" {
  value = "https://s3.${aws_s3_bucket.data.region}.amazonaws.com"
}

output "access_key_id" {
  value     = aws_iam_access_key.parseable.id
  sensitive = true
}

output "secret_access_key" {
  value     = aws_iam_access_key.parseable.secret
  sensitive = true
}

output "cluster_secret" {
  value     = random_password.cluster_secret.result
  sensitive = true
}
