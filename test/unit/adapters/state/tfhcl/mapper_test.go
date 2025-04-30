package tfhcl_test

import (
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl"
	"testing"

	"fmt" // Need fmt
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl/evaluator"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			"root_block_device": []any{
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
			},
		}

		stateRes, err := tfhcl.MapEvaluatedHCLToDomain(domain.KindComputeInstance, address, evaluatedAttrs)
		require.NoError(t, err)
		require.NotNil(t, stateRes)

		meta := stateRes.Metadata()
		assert.Equal(t, domain.KindComputeInstance, meta.Kind)
		assert.Equal(t, address, meta.SourceIdentifier)
		assert.Equal(t, "aws", meta.ProviderType)

		attrs := stateRes.Attributes()
		require.NotNil(t, attrs)
		assert.Equal(t, "ami-12345", attrs[domain.ComputeImageIDKey])
		assert.Equal(t, "t3.medium", attrs[domain.ComputeInstanceTypeKey])

		tags, ok := attrs[domain.KeyTags].(map[string]string)
		require.True(t, ok, "Tags should be map[string]string after normalization")
		assert.Equal(t, "web-server", tags["Name"])
		assert.Equal(t, "prod", tags["Environment"])
		assert.Equal(t, "12345", tags["CostCenter"]) // Check normalized string value
		assert.Equal(t, "true", tags["Flag"])        // Check normalized string value
		assert.Equal(t, "web-server", attrs[domain.KeyName])

		rootBlock, ok := attrs[domain.ComputeRootBlockDeviceKey].(map[string]any)
		require.True(t, ok, "Root block device should be map[string]any")
		// Check normalized values
		var rootSize int64 // Use int64 as normalization target type
		require.NoError(t, convertToInt64(rootBlock["volume_size"], &rootSize))
		assert.Equal(t, int64(50), rootSize)
		assert.Equal(t, true, rootBlock["encrypted"])
		assert.Equal(t, true, rootBlock["delete_on_termination"])

		ebsSlice, ok := attrs[domain.ComputeEBSBlockDevicesKey].([]map[string]any)
		require.True(t, ok)
		require.Len(t, ebsSlice, 1)
		ebsBlock := ebsSlice[0]
		assert.Equal(t, "/dev/sdf", ebsBlock["device_name"])
		var ebsSize int64 // Use int64
		require.NoError(t, convertToInt64(ebsBlock["volume_size"], &ebsSize))
		assert.Equal(t, int64(100), ebsSize)
		assert.Equal(t, false, ebsBlock["delete_on_termination"])
	})

	t.Run("Map Empty Evaluated Attrs", func(t *testing.T) {
		address := "aws_instance.empty"
		evaluatedAttrs := evaluator.EvaluatedResource{}

		stateRes, err := tfhcl.MapEvaluatedHCLToDomain(domain.KindComputeInstance, address, evaluatedAttrs)
		require.NoError(t, err)
		require.NotNil(t, stateRes)
		attrs := stateRes.Attributes()
		require.NotNil(t, attrs)
		assert.Nil(t, attrs[domain.KeyName])
		assert.Nil(t, attrs[domain.KeyID])
	})

	t.Run("Mapping Error on Normalize", func(t *testing.T) {
		address := "aws_instance.bad_norm"
		evaluatedAttrs := evaluator.EvaluatedResource{
			"tags": 123, // Invalid type for tags (number)
		}
		_, err := tfhcl.MapEvaluatedHCLToDomain(domain.KindComputeInstance, address, evaluatedAttrs)
		// Expect error from normalizeAndCopyAttributes -> normalizeTags
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed normalizing evaluated HCL attributes")
		assert.Contains(t, err.Error(), "invalid type for tags") // Check underlying error message
	})
}

func convertToInt64(value interface{}, target *int64) error {
	switch v := value.(type) {
	case int:
		*target = int64(v)
		return nil
	case int8:
		*target = int64(v)
		return nil
	case int16:
		*target = int64(v)
		return nil
	case int32:
		*target = int64(v)
		return nil
	case int64:
		*target = v
		return nil
	case uint:
		*target = int64(v)
		return nil
	case uint8:
		*target = int64(v)
		return nil
	case uint16:
		*target = int64(v)
		return nil
	case uint32:
		*target = int64(v)
		return nil
	case uint64:
		*target = int64(v)
		return nil // Potential overflow ignored here
	case float32:
		*target = int64(v)
		return nil
	case float64:
		*target = int64(v)
		return nil
	default:
		return fmt.Errorf("cannot convert type %T to int64", value)
	}
}
