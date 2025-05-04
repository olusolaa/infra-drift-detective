terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = "us-east-1"
}

resource "aws_instance" "web_server" {
  ami           = "ami-0c55b159cbfafe1f0"
  instance_type = "t2.micro"              # WILL CAUSE DRIFT if changed in AWS

  tags = {
    Name              = "ExampleWebServer"
    Environment       = "production"
    TFResourceAddress = "aws_instance.web_server"
    BillingCode       = "PROJECT-A"
  }

  vpc_security_group_ids = ["sg-012345abcdef01234"] # Replace with a VALID Security Group ID in your test account/region

  ebs_block_device {
    device_name = "/dev/sdf"
    volume_size = 20
    volume_type = "gp3"
    throughput  = 125
    encrypted   = true
    tags = {
      Purpose = "Data"
    }
  }

  root_block_device {
    volume_size = 10
    volume_type = "gp3"
  }

  user_data = <<-EOF
              #!/bin/bash
              echo "Hello, World!" > /tmp/hello.txt
              EOF

}

# You MUST replace vpc_security_group_ids with a valid one.