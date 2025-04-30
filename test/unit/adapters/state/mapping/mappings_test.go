package mapping_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/mapping"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
)

func TestMapTfTypeToDomainKind(t *testing.T) {
	testCases := []struct {
		name         string
		tfType       string
		expectedKind domain.ResourceKind
		expectError  bool
	}{
		{
			name:         "aws_instance",
			tfType:       "aws_instance",
			expectedKind: domain.KindComputeInstance,
			expectError:  false,
		},
		{
			name:         "aws_s3_bucket",
			tfType:       "aws_s3_bucket",
			expectedKind: domain.KindStorageBucket,
			expectError:  false,
		},
		{
			name:         "aws_db_instance",
			tfType:       "aws_db_instance",
			expectedKind: domain.KindDatabaseInstance,
			expectError:  false,
		},
		{
			name:         "unsupported_type",
			tfType:       "unknown_resource_type",
			expectedKind: "",
			expectError:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Act
			kind, err := mapping.MapTfTypeToDomainKind(tc.tfType)

			// Assert
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

func TestNormalizeAndCopyAttributes(t *testing.T) {
	t.Run("compute instance attributes", func(t *testing.T) {
		// Arrange
		rawAttrs := map[string]any{
			"id":                     "i-1234567890abcdef0",
			"instance_type":          "t2.micro",
			"ami":                    "ami-0c55b159cbfafe1f0",
			"subnet_id":              "subnet-12345678",
			"availability_zone":      "us-west-2a",
			"vpc_security_group_ids": []interface{}{"sg-12345678", "sg-87654321"},
			"tags": map[string]interface{}{
				"Name":        "TestInstance",
				"Environment": "test",
			},
			"root_block_device": []interface{}{
				map[string]interface{}{
					"volume_size": float64(100),
					"volume_type": "gp2",
					"encrypted":   true,
				},
			},
			"ebs_block_device": []interface{}{
				map[string]interface{}{
					"device_name": "/dev/sdf",
					"volume_size": float64(200),
					"volume_type": "io1",
					"iops":        float64(3000),
				},
			},
		}
		targetAttrs := make(map[string]any)

		// Act
		err := mapping.NormalizeAndCopyAttributes(domain.KindComputeInstance, rawAttrs, targetAttrs)

		// Assert
		require.NoError(t, err)

		// Check direct field mapping
		assert.Equal(t, "i-1234567890abcdef0", targetAttrs[domain.KeyID])
		assert.Equal(t, "t2.micro", targetAttrs[domain.ComputeInstanceTypeKey])
		assert.Equal(t, "ami-0c55b159cbfafe1f0", targetAttrs[domain.ComputeImageIDKey])
		assert.Equal(t, "subnet-12345678", targetAttrs[domain.ComputeSubnetIDKey])
		assert.Equal(t, "us-west-2a", targetAttrs[domain.ComputeAvailabilityZoneKey])

		// Check security groups slice
		secGroups, ok := targetAttrs[domain.ComputeSecurityGroupsKey].([]interface{})
		require.True(t, ok)
		assert.Len(t, secGroups, 2)
		assert.Contains(t, secGroups, "sg-12345678")
		assert.Contains(t, secGroups, "sg-87654321")

		// Check tags normalization
		tags, ok := targetAttrs[domain.KeyTags].(map[string]string)
		require.True(t, ok)
		assert.Equal(t, "TestInstance", tags["Name"])
		assert.Equal(t, "test", tags["Environment"])

		// Check Name field inference from tags
		assert.Equal(t, "TestInstance", targetAttrs[domain.KeyName])

		// Check root block device normalization
		rootDevice, ok := targetAttrs[domain.ComputeRootBlockDeviceKey].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, float64(100), rootDevice["volume_size"])
		assert.Equal(t, "gp2", rootDevice["volume_type"])
		assert.Equal(t, true, rootDevice["encrypted"])

		// Check EBS block devices normalization
		ebsDevices, ok := targetAttrs[domain.ComputeEBSBlockDevicesKey].([]map[string]any)
		require.True(t, ok)
		require.Len(t, ebsDevices, 1)
		assert.Equal(t, "/dev/sdf", ebsDevices[0]["device_name"])
		assert.Equal(t, float64(200), ebsDevices[0]["volume_size"])
		assert.Equal(t, "io1", ebsDevices[0]["volume_type"])
		assert.Equal(t, float64(3000), ebsDevices[0]["iops"])
	})

	t.Run("storage bucket attributes", func(t *testing.T) {
		// Arrange
		rawAttrs := map[string]any{
			"id":     "my-test-bucket",
			"bucket": "my-test-bucket",
			"region": "us-west-2",
			"acl":    "private",
			"versioning": []interface{}{
				map[string]interface{}{
					"enabled": true,
				},
			},
			"tags": map[string]interface{}{
				"Name":        "TestBucket",
				"Environment": "test",
			},
		}
		targetAttrs := make(map[string]any)

		// Act
		err := mapping.NormalizeAndCopyAttributes(domain.KindStorageBucket, rawAttrs, targetAttrs)

		// Assert
		require.NoError(t, err)

		// Check direct field mapping
		assert.Equal(t, "my-test-bucket", targetAttrs[domain.KeyID])
		assert.Equal(t, "private", targetAttrs[domain.StorageBucketACLKey])
		assert.Equal(t, "us-west-2", targetAttrs[domain.KeyRegion])

		// Check versioning normalization
		versioning, ok := targetAttrs[domain.StorageBucketVersioningKey].(bool)
		require.True(t, ok)
		assert.True(t, versioning)

		// Check tags normalization
		tags, ok := targetAttrs[domain.KeyTags].(map[string]string)
		require.True(t, ok)
		assert.Equal(t, "TestBucket", tags["Name"])
		assert.Equal(t, "test", tags["Environment"])

		// Check Name field inference from tags
		assert.Equal(t, "TestBucket", targetAttrs[domain.KeyName])
	})

	t.Run("unsupported kind", func(t *testing.T) {
		// Arrange
		rawAttrs := map[string]any{"id": "test"}
		targetAttrs := make(map[string]any)

		// Act
		err := mapping.NormalizeAndCopyAttributes("UnsupportedKind", rawAttrs, targetAttrs)

		// Assert
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no attribute mapping defined for kind")
		assert.Empty(t, targetAttrs)
	})

	t.Run("missing attribute in source", func(t *testing.T) {
		// Arrange - create attributes with some expected fields missing
		rawAttrs := map[string]any{
			"id": "i-1234567890abcdef0",
			// no instance_type, ami, etc.
		}
		targetAttrs := make(map[string]any)

		// Act
		err := mapping.NormalizeAndCopyAttributes(domain.KindComputeInstance, rawAttrs, targetAttrs)

		// Assert - should not error on missing fields
		require.NoError(t, err)
		assert.Equal(t, "i-1234567890abcdef0", targetAttrs[domain.KeyID])
		// Other mapped fields should be absent
		_, hasInstanceType := targetAttrs[domain.ComputeInstanceTypeKey]
		assert.False(t, hasInstanceType)
	})
}
