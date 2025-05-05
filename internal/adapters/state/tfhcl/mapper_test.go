// --- START OF FILE infra-drift-detector/internal/adapters/state/tfhcl/mapper_test.go ---

package tfhcl_test

import (
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/mapping"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl/evaluator"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
)

func TestMapEvaluatedHCLToDomain(t *testing.T) {
	t.Run("Map aws_instance", func(t *testing.T) {
		address := "aws_instance.web"
		evaluatedAttrs := evaluator.EvaluatedResource{
			"ami":           "ami-12345",
			"instance_type": "t3.medium",
			"tags": map[string]interface{}{
				"Name":        "web-server",
				"Environment": "prod",
				"CostCenter":  float64(12345), // Input number as float64
				"Flag":        true,           // Input bool
			},
			"root_block_device": []any{ // Note: evaluator puts blocks in slices
				map[string]interface{}{
					"volume_size": float64(50),
					"encrypted":   "true", // Input bool as string
				},
			},
			"ebs_block_device": []any{
				map[string]interface{}{
					"device_name": "/dev/sdf",
					"volume_size": "100", // Input number as string
				},
				map[string]interface{}{
					"device_name":           "/dev/sdg",
					"delete_on_termination": false, // Explicit false
				},
			},
			"ignore_me": "some other value", // Should be ignored by normalization
		}

		stateRes, err := tfhcl.MapEvaluatedHCLToDomain(domain.KindComputeInstance, address, evaluatedAttrs)
		require.NoError(t, err)
		require.NotNil(t, stateRes)

		meta := stateRes.Metadata()
		assert.Equal(t, domain.KindComputeInstance, meta.Kind)
		assert.Equal(t, address, meta.SourceIdentifier)
		assert.Equal(t, "aws", meta.ProviderType) // Derived from "aws_instance"

		attrs := stateRes.Attributes()
		require.NotNil(t, attrs)
		assert.Equal(t, "ami-12345", attrs[domain.ComputeImageIDKey])
		assert.Equal(t, "t3.medium", attrs[domain.ComputeInstanceTypeKey])

		tags, ok := attrs[domain.KeyTags].(map[string]string)
		require.True(t, ok, "Tags should be map[string]string after normalization")
		assert.Equal(t, "web-server", tags["Name"])
		assert.Equal(t, "prod", tags["Environment"])
		assert.Equal(t, "12345", tags["CostCenter"])
		assert.Equal(t, "true", tags["Flag"])
		assert.Equal(t, "web-server", attrs[domain.KeyName]) // Inferred from tags

		rootBlock, ok := attrs[domain.ComputeRootBlockDeviceKey].(map[string]any)
		require.True(t, ok, "Root block device should be map[string]any")
		assert.Equal(t, int64(50), rootBlock["volume_size"]) // Normalized
		assert.Equal(t, true, rootBlock["encrypted"])        // Normalized
		assert.Equal(t, true, rootBlock["delete_on_termination"])

		ebsSlice, ok := attrs[domain.ComputeEBSBlockDevicesKey].([]map[string]any)
		require.True(t, ok)
		require.Len(t, ebsSlice, 2)

		// Check first EBS device
		ebsBlock1 := ebsSlice[0]
		assert.Equal(t, "/dev/sdf", ebsBlock1["device_name"])
		assert.Equal(t, int64(100), ebsBlock1["volume_size"])      // Normalized
		assert.Equal(t, false, ebsBlock1["delete_on_termination"]) // Defaulted

		// Check second EBS device
		ebsBlock2 := ebsSlice[1]
		assert.Equal(t, "/dev/sdg", ebsBlock2["device_name"])
		assert.Equal(t, false, ebsBlock2["delete_on_termination"]) // Explicitly set

		_, ignoredExists := attrs["ignore_me"]
		assert.False(t, ignoredExists, "'ignore_me' attribute should not be mapped")

		// Check default nil values for computed fields if not present
		assert.Nil(t, attrs[domain.KeyID])
		assert.Nil(t, attrs[domain.KeyARN])
	})

	t.Run("Map aws_s3_bucket", func(t *testing.T) {
		address := "aws_s3_bucket.logs"
		evaluatedAttrs := evaluator.EvaluatedResource{
			"bucket": "my-logs-bucket",
			"acl":    "log-delivery-write",
			"versioning": []any{
				map[string]any{"enabled": true},
			},
			"server_side_encryption_configuration": []any{
				map[string]any{
					"rule": []any{
						map[string]any{
							"apply_server_side_encryption_by_default": []any{
								map[string]any{"sse_algorithm": "AES256"},
							},
						},
					},
				},
			},
		}
		stateRes, err := tfhcl.MapEvaluatedHCLToDomain(domain.KindStorageBucket, address, evaluatedAttrs)
		require.NoError(t, err)

		meta := stateRes.Metadata()
		assert.Equal(t, domain.KindStorageBucket, meta.Kind)
		assert.Equal(t, address, meta.SourceIdentifier)
		assert.Equal(t, "aws", meta.ProviderType)

		attrs := stateRes.Attributes()
		assert.Equal(t, "log-delivery-write", attrs[domain.StorageBucketACLKey])
		assert.True(t, attrs[domain.StorageBucketVersioningKey].(bool))
		assert.Equal(t, "my-logs-bucket", attrs[domain.KeyName]) // Inferred from ID

		encConfig, ok := attrs[domain.StorageBucketEncryptionKey].(map[string]any)
		require.True(t, ok)
		assert.Contains(t, encConfig, "apply_server_side_encryption_by_default")
		defEnc := encConfig["apply_server_side_encryption_by_default"].(map[string]any)
		assert.Equal(t, "AES256", defEnc["sse_algorithm"])
	})

	t.Run("Map Empty Evaluated Attrs", func(t *testing.T) {
		address := "aws_instance.empty"
		evaluatedAttrs := evaluator.EvaluatedResource{}

		stateRes, err := tfhcl.MapEvaluatedHCLToDomain(domain.KindComputeInstance, address, evaluatedAttrs)
		require.NoError(t, err)
		require.NotNil(t, stateRes)
		attrs := stateRes.Attributes()
		require.NotNil(t, attrs)
		assert.Nil(t, attrs[domain.KeyName]) // No Name tag to infer from
		assert.Nil(t, attrs[domain.KeyID])
		assert.Nil(t, attrs[domain.KeyARN])
	})

	t.Run("Error on Unsupported Kind for Normalization", func(t *testing.T) {
		//address := "unsupported_type.test"
		evaluatedAttrs := evaluator.EvaluatedResource{"key": "value"}

		targetAttrs := make(map[string]any)
		err := mapping.NormalizeAndCopyAttributes("UnsupportedKind", evaluatedAttrs, targetAttrs) // Use mapping directly
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no attribute mapping defined for kind: UnsupportedKind")
	})
}
