resource "aws_instance" "web" {
  ami           = "ami-0c55b159cbfafe1f0"
  instance_type = "t2.micro"
  subnet_id     = "subnet-12345678"
  availability_zone = "us-west-2a"
  vpc_security_group_ids = ["sg-12345678", "sg-87654321"]
  
  tags = {
    Name        = "WebServer"
    Environment = "production"
  }
  
  root_block_device {
    volume_size = 100
    volume_type = "gp2"
    encrypted   = true
  }
  
  ebs_block_device {
    device_name = "/dev/sdf"
    volume_size = 200
    volume_type = "io1"
    iops        = 3000
  }
}

resource "aws_s3_bucket" "data" {
  bucket = "my-test-bucket"
  acl    = "private"
  
  versioning {
    enabled = true
  }
  
  tags = {
    Name        = "DataBucket"
    Environment = "production"
  }
}
