#!/bin/bash

# Colors for better readability
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
RESET='\033[0m'

# Trap for cleanup
function cleanup {
    echo -e "\n${YELLOW}Cleaning up resources...${RESET}"
    cleanup_resources
    
    # Unset AWS environment variables
    unset AWS_ACCESS_KEY_ID
    unset AWS_SECRET_ACCESS_KEY
    unset AWS_SESSION_TOKEN
    unset AWS_DEFAULT_REGION
    
    echo -e "${GREEN}All resources have been cleaned up.${RESET}"
    exit $?
}

trap cleanup EXIT SIGINT SIGTERM

# Set the script directory as a variable
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )"

# Encrypted credentials - will be decrypted with password
ENCRYPTED_CREDENTIALS="U2FsdGVkX1/+6FV//QZuXEtLr7tvN9JQiy5nnFTBguVTZySTyWuvF5CG1R5puTMn
BPNxTAjMhBdCNjn9Ae5p3Zi63u6dpM6nBRI+VocYlRuGpPssV8MaSYqSDCK4ebKA
oYQm++I8j5ZkATcwwhdAt3nF5gBLiaF1cMXf/8Tx85UuW4sg1UAOAKKPmTHnr/Sm
wYIKODFud61CWQLhnZ4rl6HyzLyHjdFbFGnxPSHa/zOo0nk37S8dTjBicuEg9Tzs
qrY3NgS/iUAgMpLoDDZ+JgSdWhR6Ahy7bICMxRQuDgS28EO/CMBDsX59NjCeyOJC"

EC2_INSTANCE_ID=""
BUCKET_NAME=""
TERRAFORM_APPLIED=false

# Function to decrypt credentials
function decrypt_credentials {
    local password=""
    echo -e "${YELLOW}Enter the password to decrypt AWS credentials:${RESET}"
    read -s password
    
    # Decrypt the credentials with OpenSSL
    DECRYPTED=$(echo "$ENCRYPTED_CREDENTIALS" | openssl enc -aes-256-cbc -md sha256 -a -d -salt -pass pass:"$password" 2>/dev/null)
    
    if [ $? -ne 0 ]; then
        echo -e "${RED}Failed to decrypt credentials. Incorrect password?${RESET}"
        exit 1
    fi
    
    # Process the decrypted credentials
    AWS_ACCESS_KEY=$(echo "$DECRYPTED" | grep AWS_ACCESS_KEY | cut -d= -f2)
    AWS_SECRET_KEY=$(echo "$DECRYPTED" | grep AWS_SECRET_KEY | cut -d= -f2)
    DEMO_ROLE_ARN=$(echo "$DECRYPTED" | grep DEMO_ROLE_ARN | cut -d= -f2)
    DEMO_EXTERNAL_ID=$(echo "$DECRYPTED" | grep DEMO_EXTERNAL_ID | cut -d= -f2)
    
    if [ -z "$AWS_ACCESS_KEY" ] || [ -z "$AWS_SECRET_KEY" ] || [ -z "$DEMO_ROLE_ARN" ]; then
        echo -e "${RED}Failed to extract required credentials from decrypted data.${RESET}"
        exit 1
    fi
    
    echo -e "${GREEN}Credentials decrypted successfully.${RESET}"
}

# Function to cleanup all resources created by the demo
function cleanup_resources {
    if [ "$TERRAFORM_APPLIED" = true ]; then
        echo -e "${YELLOW}Destroying Terraform resources...${RESET}"
        terraform -chdir="$SCRIPT_DIR" destroy -auto-approve
    fi
}

# Function to restore resources to original state (remove drift)
function restore_state {
    echo -e "${YELLOW}Restoring resources to their original state...${RESET}"
    terraform -chdir="$SCRIPT_DIR" apply -auto-approve
    echo -e "${GREEN}Resources restored to their original state defined in Terraform.${RESET}"
}

# Function to run the drift detector
function run_drift_detector {
    echo -e "${YELLOW}Running drift detection...${RESET}"
    
    # Ensure valid credentials before running
    ensure_valid_aws_credentials || {
        echo -e "${RED}Failed to get valid AWS credentials. Cannot run drift detector.${RESET}"
        return 1
    }
    
    # Get the project root directory
    PROJECT_ROOT="$( cd "$( dirname "${BASH_SOURCE[0]}" )/.." &> /dev/null && pwd )"
    
    # Set the path for the binary
    ANALYSER_PATH="$PROJECT_ROOT/drift-analyser"
    
    # Check if the binary exists, build only if needed
    if [ ! -f "$ANALYSER_PATH" ]; then
        echo -e "${YELLOW}Binary not found. Building drift-analyser...${RESET}"
        (cd "$PROJECT_ROOT" && go build -o drift-analyser ./cmd)
        
        if [ $? -ne 0 ]; then
            echo -e "${RED}Error: Failed to build drift-analyser binary${RESET}"
            return 1
        fi
    fi
    
    # Run the drift-analyser with the demo-specific config
    CONFIG_FILE="$SCRIPT_DIR/demo_config.yaml"
    
    # Verify the config file exists
    if [ ! -f "$CONFIG_FILE" ]; then
        echo -e "${RED}Error: Config file not found at $CONFIG_FILE${RESET}"
        return 1
    fi
    
    echo "Using configuration file: $CONFIG_FILE"
    
    # Print the credentials being used (masked for security)
    echo -e "${YELLOW}Using AWS credentials:${RESET}"
    echo "AWS_ACCESS_KEY_ID: ${AWS_ACCESS_KEY_ID:0:4}...${AWS_ACCESS_KEY_ID:(-4)}"
    echo "AWS_SECRET_ACCESS_KEY: ${AWS_SECRET_ACCESS_KEY:0:1}...${AWS_SECRET_ACCESS_KEY:(-4)}"
    echo "AWS_SESSION_TOKEN: [SESSION TOKEN PRESENT]"
    echo "AWS_DEFAULT_REGION: $AWS_DEFAULT_REGION"
    
    # Run with debug level logging to see what's happening
    echo -e "${YELLOW}Running drift-analyser with debug logging...${RESET}"
    (cd "$SCRIPT_DIR" && $ANALYSER_PATH -c $CONFIG_FILE --log-level=debug)
    DRIFT_RESULT=$?
    
    # Offer to show resource verification
    echo
    read -p "Would you like to verify the actual resource state? (y/n): " verify_choice
    if [[ "$verify_choice" =~ ^[Yy] ]]; then
        verify_demo_resources
    fi
}

# Utility function to ensure credentials are valid before any AWS operation
function ensure_valid_aws_credentials {
    echo -e "${YELLOW}Ensuring valid AWS credentials...${RESET}"
    
    # Try to use current credentials
    if aws sts get-caller-identity &>/dev/null; then
        echo -e "${GREEN}Current credentials are valid.${RESET}"
        return 0
    fi
    
    echo -e "${YELLOW}Credentials are invalid or expired. Getting fresh credentials...${RESET}"
    
    # Decrypt credentials if encrypted
    decrypt_credentials
    
    # Get fresh credentials
    DEMO_ROLE_ARN="arn:aws:iam::$DEMO_ACCOUNT_ID:role/$DEMO_ROLE_NAME"
    echo -e "${YELLOW}Assuming role $DEMO_ROLE_ARN...${RESET}"
    
    local ASSUME_OUTPUT=$(aws sts assume-role \
      --role-arn "$DEMO_ROLE_ARN" \
      --role-session-name "DriftDemo-$(date +%s)" \
      --external-id "$DEMO_EXTERNAL_ID" \
      --output json 2>&1)
      
    if [ $? -ne 0 ]; then
        echo -e "${RED}Failed to assume role: $ASSUME_OUTPUT${RESET}"
        return 1
    fi
      
    # Extract and export the credentials
    export AWS_ACCESS_KEY_ID=$(echo "$ASSUME_OUTPUT" | jq -r '.Credentials.AccessKeyId')
    export AWS_SECRET_ACCESS_KEY=$(echo "$ASSUME_OUTPUT" | jq -r '.Credentials.SecretAccessKey')
    export AWS_SESSION_TOKEN=$(echo "$ASSUME_OUTPUT" | jq -r '.Credentials.SessionToken')
    local AWS_EXPIRATION=$(echo "$ASSUME_OUTPUT" | jq -r '.Credentials.Expiration')
    
    echo -e "${GREEN}Successfully obtained fresh credentials.${RESET}"
    echo -e "${YELLOW}Credentials will expire at: $AWS_EXPIRATION${RESET}"
    
    # Verify the new credentials work
    aws sts get-caller-identity
    
    return 0
}

# Function to verify and display resource state
function verify_demo_resources {
    echo -e "${YELLOW}Verifying resources...${RESET}"
    
    # Refresh credentials if needed - check if tokens are expired
    ensure_valid_aws_credentials
    
    # Show EC2 instance details from AWS using aws-cli
    echo -e "${BLUE}EC2 instance details from AWS:${RESET}"
    
    # Get instance ID from terraform output
    INSTANCE_ID=$(cd "$SCRIPT_DIR" && terraform output -raw instance_id)
    
    # Show instance details
    aws ec2 describe-instances --instance-ids "$INSTANCE_ID" \
        --query 'Reservations[*].Instances[*].{ID:InstanceId,Type:InstanceType,State:State.Name,Tags:Tags}' \
        --output table
    echo
}

# Function to check network connectivity
function check_aws_connectivity {
  echo -e "${YELLOW}Checking connectivity to AWS...${RESET}"
  
  # Try to connect to AWS STS endpoint
  if ! curl --connect-timeout 5 -s https://sts.amazonaws.com > /dev/null; then
    echo -e "${RED}ERROR: Cannot connect to AWS services. Please check your internet connection.${RESET}"
    return 1
  fi
}

# Function to handle role assumption
function assume_demo_role {
  # Attempt to assume the role
  local ASSUME_OUTPUT
  echo -e "${YELLOW}Attempting to assume role: $DEMO_ROLE_ARN${RESET}"
  ASSUME_OUTPUT=$(aws sts assume-role \
    --role-arn "$DEMO_ROLE_ARN" \
    --role-session-name "DriftDemo-$(date +%s)" \
    --external-id "$DEMO_EXTERNAL_ID" \
    --output json 2>&1)
  
  local ASSUME_STATUS=$?
  if [ $ASSUME_STATUS -ne 0 ]; then
    echo -e "${RED}ERROR: Failed to assume role.${RESET}"
    echo "$ASSUME_OUTPUT"
    return 1
  fi
  
  # Extract and export the temporary credentials
  export AWS_ACCESS_KEY_ID=$(echo "$ASSUME_OUTPUT" | jq -r '.Credentials.AccessKeyId')
  export AWS_SECRET_ACCESS_KEY=$(echo "$ASSUME_OUTPUT" | jq -r '.Credentials.SecretAccessKey')
  export AWS_SESSION_TOKEN=$(echo "$ASSUME_OUTPUT" | jq -r '.Credentials.SessionToken')
  
  echo -e "${GREEN}Successfully assumed the drift detection demo role.${RESET}"
  echo -e "${YELLOW}Credentials will expire at: $(echo "$ASSUME_OUTPUT" | jq -r '.Credentials.Expiration')${RESET}"
  
  # Verify the credentials work
  echo -e "${YELLOW}Verifying assumed credentials...${RESET}"
  aws sts get-caller-identity
  
  return 0
}

# Function to introduce drift to EC2 instance
function create_ec2_drift {
    # Check and refresh credentials if expired
    ensure_valid_aws_credentials

    echo -e "${BLUE}Choose how to create drift in the EC2 instance:${RESET}"
    echo "1) Add a custom tag to the instance"
    echo "2) Change the instance type"
    echo "3) Modify security group"
    echo "4) Enable termination protection"
    echo "5) Back to main menu"
    
    read -p "Enter choice [1-5]: " ec2_choice
    
    case $ec2_choice in
        1)
            echo -e "${YELLOW}Adding a custom tag to the instance...${RESET}"
            aws ec2 create-tags --resources $EC2_INSTANCE_ID --tags Key=DriftDemo,Value=CustomTag
            echo -e "${GREEN}Tag added. This creates drift since this tag isn't in the Terraform definition.${RESET}"
            ;;
        2)
            echo -e "${YELLOW}Changing instance type (this will stop the instance first)...${RESET}"
            aws ec2 stop-instances --instance-ids $EC2_INSTANCE_ID
            
            # Wait for the instance to stop
            echo "Waiting for instance to stop..."
            aws ec2 wait instance-stopped --instance-ids $EC2_INSTANCE_ID
            
            # Change the instance type
            aws ec2 modify-instance-attribute --instance-id $EC2_INSTANCE_ID --instance-type "{\"Value\": \"t2.small\"}"
            
            echo -e "${GREEN}Instance type changed to t2.small. This creates drift since Terraform defines it as t2.micro.${RESET}"
            
            # Ask if user wants to restart the instance
            read -p "Do you want to start the instance again? (y/n): " restart
            if [[ $restart == "y" || $restart == "Y" ]]; then
                aws ec2 start-instances --instance-ids $EC2_INSTANCE_ID
                echo "Instance is starting..."
            fi
            ;;
        3)
            echo -e "${YELLOW}Creating a new security group and attaching it to the instance...${RESET}"
            
            # Create a new security group
            NEW_SG_ID=$(aws ec2 create-security-group \
                --group-name "drift-demo-sg-$(date +%s)" \
                --description "Security group created for drift demo" \
                --vpc-id $(aws ec2 describe-instances \
                    --instance-ids $EC2_INSTANCE_ID \
                    --query 'Reservations[0].Instances[0].VpcId' \
                    --output text) \
                --query 'GroupId' \
                --output text)
            
            # Add a rule to the security group
            aws ec2 authorize-security-group-ingress \
                --group-id $NEW_SG_ID \
                --protocol tcp \
                --port 8080 \
                --cidr 0.0.0.0/0
                
            # Get current security groups
            CURRENT_SGS=$(aws ec2 describe-instances \
                --instance-ids $EC2_INSTANCE_ID \
                --query 'Reservations[0].Instances[0].SecurityGroups[*].GroupId' \
                --output text)
            
            # Add the new security group to the instance (keeping existing ones)
            aws ec2 modify-instance-attribute \
                --instance-id $EC2_INSTANCE_ID \
                --groups $CURRENT_SGS $NEW_SG_ID
                
            echo -e "${GREEN}Added new security group $NEW_SG_ID with port 8080 open.${RESET}"
            echo -e "${GREEN}This creates drift since this security group isn't in the Terraform definition.${RESET}"
            ;;
        4)
            echo -e "${YELLOW}Enabling termination protection...${RESET}"
            aws ec2 modify-instance-attribute \
                --instance-id $EC2_INSTANCE_ID \
                --disable-api-termination
                
            echo -e "${GREEN}Termination protection enabled. This creates drift since Terraform doesn't define this setting.${RESET}"
            ;;
        5)
            return
            ;;
        *)
            echo -e "${RED}Invalid option.${RESET}"
            ;;
    esac
}

# Function to create drift in S3 bucket
function create_s3_drift {
    # Check and refresh credentials if expired
    ensure_valid_aws_credentials
    
    echo -e "${BLUE}Choose how to create drift in the S3 bucket:${RESET}"
    echo "1) Upload a new file to the bucket"
    echo "2) Enable versioning on the bucket"
    echo "3) Add a public read policy to the bucket"
    echo "4) Add a custom tag to the bucket"
    echo "5) Back to main menu"
    
    read -p "Enter choice [1-5]: " s3_choice
    
    case $s3_choice in
        1)
            echo -e "${YELLOW}Creating a test file and uploading to S3...${RESET}"
            echo "This is a test file to demonstrate drift detection." > /tmp/test_drift.txt
            aws s3 cp /tmp/test_drift.txt s3://$BUCKET_NAME/
            rm /tmp/test_drift.txt
            echo -e "${GREEN}File uploaded. This creates drift since this file isn't in the Terraform definition.${RESET}"
            ;;
        2)
            echo -e "${YELLOW}Enabling versioning on the bucket...${RESET}"
            aws s3api put-bucket-versioning --bucket $BUCKET_NAME --versioning-configuration Status=Enabled
            echo -e "${GREEN}Versioning enabled. This creates drift since Terraform doesn't define versioning.${RESET}"
            ;;
        3)
            echo -e "${YELLOW}Adding a public read policy to the bucket...${RESET}"
            policy=$(cat <<EOF
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "PublicRead",
      "Effect": "Allow",
      "Principal": "*",
      "Action": "s3:GetObject",
      "Resource": "arn:aws:s3:::$BUCKET_NAME/*"
    }
  ]
}
EOF
)
            echo "$policy" > /tmp/bucket_policy.json
            aws s3api put-bucket-policy --bucket $BUCKET_NAME --policy file:///tmp/bucket_policy.json
            rm /tmp/bucket_policy.json
            echo -e "${GREEN}Bucket policy changed. This creates drift since Terraform doesn't define this policy.${RESET}"
            ;;
        4)
            echo -e "${YELLOW}Adding a custom tag to the bucket...${RESET}"
            # Get current tags first to preserve the TFResourceAddress tag
            aws s3api put-bucket-tagging --bucket $BUCKET_NAME --tagging 'TagSet=[{Key=TFResourceAddress,Value=aws_s3_bucket.demo_bucket},{Key=S3Drift,Value=TestDrift}]'
            echo -e "${GREEN}Tag added. This creates drift since this tag isn't in the Terraform definition.${RESET}"
            ;;
        5)
            return
            ;;
        *)
            echo -e "${RED}Invalid option.${RESET}"
            ;;
    esac
}

# Main function to run the demo
function run_demo {
    clear
    echo -e "${GREEN}==========================================${RESET}"
    echo -e "${GREEN}     Infra Drift Detective Demo${RESET}"
    echo -e "${GREEN}==========================================${RESET}"
    echo
    
    # Check AWS connectivity
    check_aws_connectivity
    
    # Print instructions
    echo -e "${YELLOW}This demo demonstrates how the drift detector works${RESET}"
    echo -e "${YELLOW}by creating infrastructure with Terraform and then${RESET}"
    echo -e "${YELLOW}making changes outside of Terraform to create drift.${RESET}"
    echo
    
    # First, ensure we have credentials
    decrypt_credentials
    
    # Set up AWS credentials
    export AWS_ACCESS_KEY_ID="$AWS_ACCESS_KEY"
    export AWS_SECRET_ACCESS_KEY="$AWS_SECRET_KEY"
    export AWS_DEFAULT_REGION="eu-west-1"
    
    # Assume the role
    assume_demo_role || exit 1
    
    # Apply Terraform
    echo -e "${YELLOW}Creating infrastructure with Terraform...${RESET}"
    terraform -chdir="$SCRIPT_DIR" init
    
    if ! terraform -chdir="$SCRIPT_DIR" apply -auto-approve; then
        echo -e "${RED}Failed to apply Terraform configuration.${RESET}"
        exit 1
    fi
    
    TERRAFORM_APPLIED=true
    EC2_INSTANCE_ID=$(terraform -chdir="$SCRIPT_DIR" output -raw instance_id)
    BUCKET_NAME=$(terraform -chdir="$SCRIPT_DIR" output -raw bucket_name)
    
    echo -e "${GREEN}Infrastructure created successfully!${RESET}"
    echo -e "${BLUE}EC2 Instance:${RESET} $EC2_INSTANCE_ID"
    echo -e "${BLUE}S3 Bucket:${RESET} $BUCKET_NAME"
    echo
    
    while true; do
        echo -e "${YELLOW}What would you like to do?${RESET}"
        echo "1. Create drift in EC2 instance"
        echo "2. Create drift in S3 bucket"
        echo "3. Verify resource state"
        echo "4. Run drift detection"
        echo "5. Restore resources to original state"
        echo "6. Exit demo"
        echo
        
        read -p "Select an option (1-6): " option
        
        case $option in
            1)
                create_ec2_drift
                ;;
            2)
                create_s3_drift
                ;;
            3)
                verify_demo_resources
                ;;
            4)
                run_drift_detector
                ;;
            5)
                restore_state
                ;;
            6)
                echo -e "${GREEN}Exiting demo...${RESET}"
                exit 0
                ;;
            *)
                echo -e "${RED}Invalid option.${RESET}"
                ;;
        esac
        
        echo
    done
}

# Start the demo
run_demo
