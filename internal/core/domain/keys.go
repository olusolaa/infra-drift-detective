package domain

const (
	// Common Keys
	KeyName   = "name"
	KeyARN    = "arn"
	KeyID     = "id"
	KeyTags   = "tags" // Expects map[string]string
	KeyRegion = "region"
	TagPrefix = "tag:" // Prefix for accessing specific tags in filters/attributes map

	// Compute Instance Keys
	ComputeInstanceTypeKey       = "instance_type"
	ComputeImageIDKey            = "image_id"
	ComputeSubnetIDKey           = "subnet_id"
	ComputeSecurityGroupsKey     = "security_groups" // Expects []string
	ComputeIAMInstanceProfileKey = "iam_instance_profile"
	ComputeRootBlockDeviceKey    = "root_block_device" // Expects map[string]any or specific struct
	ComputeEBSBlockDevicesKey    = "ebs_block_devices" // Expects []map[string]any or specific struct slice
	ComputeUserDataKey           = "user_data"
	ComputeAvailabilityZoneKey   = "availability_zone"

	// Storage Bucket Keys
	StorageBucketACLKey            = "acl"
	StorageBucketVersioningKey     = "versioning_enabled"                   // Expects bool
	StorageBucketLifecycleRulesKey = "lifecycle_rules"                      // Expects complex structure (e.g., []map[string]any)
	StorageBucketLoggingKey        = "logging"                              // Expects map[string]any
	StorageBucketWebsiteKey        = "website"                              // Expects map[string]any
	StorageBucketCorsRulesKey      = "cors_rules"                           // Expects complex structure
	StorageBucketPolicyKey         = "policy"                               // Expects string (JSON policy)
	StorageBucketEncryptionKey     = "server_side_encryption_configuration" // Expects map[string]any

	// Database Instance Keys
	DBInstanceClassKey         = "instance_class"
	DBEngineKey                = "engine"
	DBEngineVersionKey         = "engine_version"
	DBAllocatedStorageKey      = "allocated_storage" // Expects int
	DBUsernameKey              = "username"
	DBMultiAZKey               = "multi_az"            // Expects bool
	DBPubliclyAccessibleKey    = "publicly_accessible" // Expects bool
	DBStorageEncryptedKey      = "storage_encrypted"   // Expects bool
	DBParameterGroupNameKey    = "parameter_group_name"
	DBOptionGroupNameKey       = "option_group_name"
	DBSubnetGroupNameKey       = "db_subnet_group_name"
	DBVPCSecurityGroupIDsKey   = "vpc_security_group_ids"  // Expects []string
	DBBackupRetentionPeriodKey = "backup_retention_period" // Expects int

	// Add other standardized keys as needed
)
