package domain

const (
	KeyName   = "name"
	KeyARN    = "arn"
	KeyID     = "id"
	KeyTags   = "tags"
	KeyRegion = "region"
	TagPrefix = "tag:"

	ComputeInstanceTypeKey       = "instance_type"
	ComputeImageIDKey            = "image_id"
	ComputeSubnetIDKey           = "subnet_id"
	ComputeSecurityGroupsKey     = "security_groups"
	ComputeIAMInstanceProfileKey = "iam_instance_profile"
	ComputeRootBlockDeviceKey    = "root_block_device"
	ComputeEBSBlockDevicesKey    = "ebs_block_devices"
	ComputeUserDataKey           = "user_data"
	ComputeAvailabilityZoneKey   = "availability_zone"

	StorageBucketACLKey            = "acl"
	StorageBucketVersioningKey     = "versioning_enabled"
	StorageBucketLifecycleRulesKey = "lifecycle_rules"
	StorageBucketLoggingKey        = "logging"
	StorageBucketWebsiteKey        = "website"
	StorageBucketCorsRulesKey      = "cors_rules"
	StorageBucketPolicyKey         = "policy"
	StorageBucketEncryptionKey     = "server_side_encryption_configuration"
)
