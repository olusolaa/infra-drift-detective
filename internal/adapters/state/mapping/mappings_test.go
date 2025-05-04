package mapping

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
)

func TestMapTfTypeToDomainKind(t *testing.T) {
	testCases := []struct {
		name         string
		tfType       string
		expectedKind domain.ResourceKind
		expectError  bool
	}{
		{"aws_instance", "aws_instance", domain.KindComputeInstance, false},
		{"aws_s3_bucket", "aws_s3_bucket", domain.KindStorageBucket, false},
		{"aws_db_instance", "aws_db_instance", domain.KindDatabaseInstance, false},
		{"unsupported_type", "aws_vpc", "", true},
		{"empty_type", "", "", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			kind, err := MapTfTypeToDomainKind(tc.tfType)

			if tc.expectError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "unsupported Terraform resource type")
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedKind, kind)
			}
		})
	}
}

func TestNormalizeAndCopyAttributes_ComputeInstance(t *testing.T) {
	kind := domain.KindComputeInstance
	targetAttrs := make(map[string]any)

	t.Run("Full Attributes", func(t *testing.T) {
		rawAttrs := map[string]any{
			"id":                     "i-123",
			"instance_type":          "t3.medium",
			"ami":                    "ami-abc",
			"subnet_id":              "subnet-abc",
			"availability_zone":      "us-east-1a",
			"vpc_security_group_ids": []any{"sg-abc", "sg-def"},
			"iam_instance_profile":   "profile-name",
			"user_data":              "some data",
			"tags": map[string]any{
				"Name": "Test", "Env": "Dev", "NumericTag": 123.0, "BoolTag": true, "NilTag": nil,
			},
			"root_block_device": []any{map[string]any{
				"volume_size":           50.0, // Number as float
				"volume_type":           "gp3",
				"encrypted":             "true", // Bool as string
				"delete_on_termination": "false",
			}},
			"ebs_block_device": []any{
				map[string]any{"device_name": "/dev/sdf", "volume_size": "100", "iops": 3000.0}, // Number as string, float
				map[string]any{"device_name": "/dev/sdg", "volume_size": 200},                   // Number as int
			},
		}
		targetAttrs = make(map[string]any)
		err := NormalizeAndCopyAttributes(kind, rawAttrs, targetAttrs)
		require.NoError(t, err)

		assert.Equal(t, "i-123", targetAttrs[domain.KeyID])
		assert.Equal(t, "t3.medium", targetAttrs[domain.ComputeInstanceTypeKey])
		assert.Equal(t, "ami-abc", targetAttrs[domain.ComputeImageIDKey])
		assert.Equal(t, "subnet-abc", targetAttrs[domain.ComputeSubnetIDKey])
		assert.Equal(t, "us-east-1a", targetAttrs[domain.ComputeAvailabilityZoneKey])
		assert.Equal(t, "profile-name", targetAttrs[domain.ComputeIAMInstanceProfileKey])
		assert.Equal(t, "some data", targetAttrs[domain.ComputeUserDataKey])
		assert.Equal(t, []string{"sg-abc", "sg-def"}, targetAttrs[domain.ComputeSecurityGroupsKey])

		expectedTags := map[string]string{"Name": "Test", "Env": "Dev", "NumericTag": "123.000000", "BoolTag": "true", "NilTag": ""}
		assert.Equal(t, expectedTags, targetAttrs[domain.KeyTags])
		assert.Equal(t, "Test", targetAttrs[domain.KeyName])

		rootBlock, ok := targetAttrs[domain.ComputeRootBlockDeviceKey].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, int64(50), rootBlock["volume_size"])
		assert.Equal(t, "gp3", rootBlock["volume_type"])
		assert.Equal(t, true, rootBlock["encrypted"])
		assert.Equal(t, false, rootBlock["delete_on_termination"]) // Explicit false overrides root default

		ebsBlocks, ok := targetAttrs[domain.ComputeEBSBlockDevicesKey].([]any)
		require.True(t, ok)
		require.Len(t, ebsBlocks, 2)
		ebs1, _ := ebsBlocks[0].(map[string]any)
		ebs2, _ := ebsBlocks[1].(map[string]any)
		assert.Equal(t, "/dev/sdf", ebs1["device_name"])
		assert.Equal(t, int64(100), ebs1["volume_size"])
		assert.Equal(t, int64(3000), ebs1["iops"])
		assert.Equal(t, "/dev/sdg", ebs2["device_name"])
		assert.Equal(t, int64(200), ebs2["volume_size"])
		assert.Equal(t, false, ebs1["delete_on_termination"]) // Default for non-root
		assert.Equal(t, false, ebs2["delete_on_termination"])
	})

	t.Run("Minimal Attributes", func(t *testing.T) {
		rawAttrs := map[string]any{"id": "i-456", "instance_type": "m5.large"}
		targetAttrs = make(map[string]any)
		err := NormalizeAndCopyAttributes(kind, rawAttrs, targetAttrs)
		require.NoError(t, err)
		assert.Equal(t, "i-456", targetAttrs[domain.KeyID])
		assert.Equal(t, "m5.large", targetAttrs[domain.ComputeInstanceTypeKey])
		_, nameExists := targetAttrs[domain.KeyName]
		assert.False(t, nameExists) // No Name tag
		_, tagsExists := targetAttrs[domain.KeyTags]
		assert.False(t, tagsExists) // No tags attribute
	})

	t.Run("Nil Raw Attributes", func(t *testing.T) {
		targetAttrs = make(map[string]any)
		err := NormalizeAndCopyAttributes(kind, nil, targetAttrs)
		require.NoError(t, err)
		assert.Empty(t, targetAttrs)
	})

	t.Run("Invalid Tag Type", func(t *testing.T) {
		rawAttrs := map[string]any{"tags": 123} // Invalid type
		targetAttrs = make(map[string]any)
		err := NormalizeAndCopyAttributes(kind, rawAttrs, targetAttrs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to normalize attribute 'tags'")
		assert.Contains(t, err.Error(), "invalid type for tags")
	})

	t.Run("Invalid Block Device Item Type", func(t *testing.T) {
		rawAttrs := map[string]any{"ebs_block_device": []any{"not-a-map"}}
		targetAttrs = make(map[string]any)
		err := NormalizeAndCopyAttributes(kind, rawAttrs, targetAttrs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to normalize attribute 'ebs_block_devices'")
		assert.Contains(t, err.Error(), "block device item at index 0 is not a map")
	})

	t.Run("Invalid Numeric Value in Block", func(t *testing.T) {
		rawAttrs := map[string]any{"root_block_device": []any{map[string]any{
			"volume_size": "not-a-number",
		}}}
		targetAttrs = make(map[string]any)
		err := NormalizeAndCopyAttributes(kind, rawAttrs, targetAttrs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "attribute 'volume_size' has non-numeric string value")
	})

	t.Run("Invalid Boolean Value in Block", func(t *testing.T) {
		rawAttrs := map[string]any{"root_block_device": []any{map[string]any{
			"encrypted": "maybe",
		}}}
		targetAttrs = make(map[string]any)
		err := NormalizeAndCopyAttributes(kind, rawAttrs, targetAttrs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "attribute 'encrypted' has non-boolean string value")
	})

	t.Run("Block Device without DeleteOnTermination", func(t *testing.T) {
		rawAttrs := map[string]any{
			"root_block_device": []any{map[string]any{"volume_size": 10}}, // No delete_on_termination
		}
		targetAttrs = make(map[string]any)
		err := NormalizeAndCopyAttributes(kind, rawAttrs, targetAttrs)
		require.NoError(t, err)
		rootBlock, ok := targetAttrs[domain.ComputeRootBlockDeviceKey].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, true, rootBlock["delete_on_termination"]) // Should default to true for root
	})

	t.Run("Invalid Security Groups Type", func(t *testing.T) {
		rawAttrs := map[string]any{"vpc_security_group_ids": []any{123}} // Slice of ints
		targetAttrs = make(map[string]any)
		err := NormalizeAndCopyAttributes(kind, rawAttrs, targetAttrs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to normalize attribute 'security_groups'")
		assert.Contains(t, err.Error(), "item at index 0 is not a string")
	})

}

func TestNormalizeAndCopyAttributes_S3Bucket(t *testing.T) {
	kind := domain.KindStorageBucket
	targetAttrs := make(map[string]any)

	t.Run("Full Attributes", func(t *testing.T) {
		rawAttrs := map[string]any{
			"id":     "my-bucket-id",
			"region": "eu-central-1",
			"arn":    "arn:aws:s3:::my-bucket-id",
			"acl":    "log-delivery-write",
			"tags":   map[string]any{"Name": "Logs", "Dept": "Ops"},
			"versioning": []any{map[string]any{
				"enabled":    true,
				"mfa_delete": false, // Often present in state
			}},
			"server_side_encryption_configuration": []any{map[string]any{
				"bucket_key_enabled": "false", // Bool as string
				"rule": []any{map[string]any{
					"apply_server_side_encryption_by_default": []any{map[string]any{
						"sse_algorithm":     "aws:kms",
						"kms_master_key_id": "arn:aws:kms:eu-central-1:111:key/abc",
					}},
				}},
			}},
			"logging": []any{map[string]any{
				"target_bucket": "dest-logs",
				"target_prefix": "logs/",
			}},
			"lifecycle_rule": []any{
				map[string]any{"id": "rule1", "enabled": true, "prefix": "tmp/"},
			},
		}
		targetAttrs = make(map[string]any)
		err := NormalizeAndCopyAttributes(kind, rawAttrs, targetAttrs)
		require.NoError(t, err)

		assert.Equal(t, "my-bucket-id", targetAttrs[domain.KeyID])
		assert.Equal(t, "eu-central-1", targetAttrs[domain.KeyRegion])
		assert.Equal(t, "arn:aws:s3:::my-bucket-id", targetAttrs[domain.KeyARN])
		assert.Equal(t, "log-delivery-write", targetAttrs[domain.StorageBucketACLKey])

		assert.Equal(t, map[string]string{"Name": "Logs", "Dept": "Ops"}, targetAttrs[domain.KeyTags])
		assert.Equal(t, "Logs", targetAttrs[domain.KeyName]) // Inferred from Name tag

		assert.Equal(t, true, targetAttrs[domain.StorageBucketVersioningKey])

		encConfig, ok := targetAttrs[domain.StorageBucketEncryptionKey].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, false, encConfig["bucket_key_enabled"])
		defEnc, ok := encConfig["apply_server_side_encryption_by_default"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "aws:kms", defEnc["sse_algorithm"])
		assert.Equal(t, "arn:aws:kms:eu-central-1:111:key/abc", defEnc["kms_master_key_id"])

		logConfig, ok := targetAttrs[domain.StorageBucketLoggingKey].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "dest-logs", logConfig["target_bucket"])
		assert.Equal(t, "logs/", logConfig["target_prefix"])

		lifeRules, ok := targetAttrs[domain.StorageBucketLifecycleRulesKey].([]any)
		require.True(t, ok)
		require.Len(t, lifeRules, 1)
		rule1, _ := lifeRules[0].(map[string]any)
		assert.Equal(t, "rule1", rule1["id"])
	})

	t.Run("Bucket Name Inference", func(t *testing.T) {
		rawAttrs := map[string]any{"id": "bucket-name-from-id"} // No Name tag
		targetAttrs = make(map[string]any)
		err := NormalizeAndCopyAttributes(kind, rawAttrs, targetAttrs)
		require.NoError(t, err)
		assert.Equal(t, "bucket-name-from-id", targetAttrs[domain.KeyName])
	})

	t.Run("Invalid Versioning Block", func(t *testing.T) {
		rawAttrs := map[string]any{"versioning": []any{"not-a-map"}}
		targetAttrs = make(map[string]any)
		err := NormalizeAndCopyAttributes(kind, rawAttrs, targetAttrs)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "versioning block item is not a map")
	})
}

func TestNormalizeAndCopyAttributes_UnsupportedKind(t *testing.T) {
	targetAttrs := make(map[string]any)
	err := NormalizeAndCopyAttributes("aws_vpc", map[string]any{"id": "vpc-123"}, targetAttrs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no attribute mapping defined for kind")
}
