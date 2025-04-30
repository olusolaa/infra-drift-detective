package tfstate_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfstate"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
)

// Since mapStateResourceToDomain is not directly accessible,
// we will test it indirectly through the Provider's public methods
func TestResourceMapping(t *testing.T) {
	t.Run("EC2 instance mapping", func(t *testing.T) {
		// Arrange
		provider, err := tfstate.NewProvider(tfstate.Config{
			FilePath: "testdata/sample_ec2.tfstate",
		})
		require.NoError(t, err)

		// Act
		resources, err := provider.ListResources(context.Background(), domain.KindComputeInstance)

		// Assert
		require.NoError(t, err)
		require.NotEmpty(t, resources)

		// Check that at least one resource matches our expected EC2 instance
		var found bool
		for _, res := range resources {
			meta := res.Metadata()
			if meta.ProviderAssignedID == "i-1234567890abcdef0" {
				found = true

				// Verify that the mapping is correct
				assert.Equal(t, domain.KindComputeInstance, meta.Kind)
				assert.Equal(t, "aws", meta.ProviderType)
				assert.Equal(t, "aws_instance.web", meta.SourceIdentifier)

				// Check attributes
				attrs := res.Attributes()
				assert.Equal(t, "i-1234567890abcdef0", attrs[domain.KeyID])
				assert.Equal(t, "t2.micro", attrs[domain.ComputeInstanceTypeKey])
				assert.Equal(t, "ami-0c55b159cbfafe1f0", attrs[domain.ComputeImageIDKey])
				assert.Equal(t, "us-west-2a", attrs[domain.ComputeAvailabilityZoneKey])

				// Verify tags
				tags, ok := attrs[domain.KeyTags].(map[string]string)
				require.True(t, ok, "tags should be converted to map[string]string")
				assert.Equal(t, "HelloWorld", tags["Name"])
				assert.Equal(t, "test", tags["Environment"])
			}
		}
		assert.True(t, found, "Should find the expected EC2 resource")
	})

	t.Run("module-based EC2 instance mapping", func(t *testing.T) {
		// Arrange
		provider, err := tfstate.NewProvider(tfstate.Config{
			FilePath: "testdata/sample_ec2.tfstate",
		})
		require.NoError(t, err)

		// Act
		resources, err := provider.ListResources(context.Background(), domain.KindComputeInstance)

		// Assert
		require.NoError(t, err)
		require.NotEmpty(t, resources)

		// Check for module resource
		var found bool
		for _, res := range resources {
			meta := res.Metadata()
			if meta.ProviderAssignedID == "i-0987654321fedcba0" {
				found = true

				// Verify metadata
				assert.Equal(t, domain.KindComputeInstance, meta.Kind)
				assert.Equal(t, "aws", meta.ProviderType)
				assert.Equal(t, "module.ec2_instances.aws_instance.web[0]", meta.SourceIdentifier)

				// Verify attributes
				attrs := res.Attributes()
				assert.Equal(t, "i-0987654321fedcba0", attrs[domain.KeyID])
				assert.Equal(t, "t2.small", attrs[domain.ComputeInstanceTypeKey])
				assert.Equal(t, "us-west-2b", attrs[domain.ComputeAvailabilityZoneKey])

				// Verify tags
				tags, ok := attrs[domain.KeyTags].(map[string]string)
				require.True(t, ok, "tags should be converted to map[string]string")
				assert.Equal(t, "ModuleInstance", tags["Name"])
				assert.Equal(t, "staging", tags["Environment"])
			}
		}
		assert.True(t, found, "Should find the module-based EC2 resource")
	})

	t.Run("different provider name formats", func(t *testing.T) {
		testCases := []struct {
			resourceId   string
			providerType string
		}{
			{"i-1234567890abcdef0", "aws"}, // registry.terraform.io/hashicorp/aws
			{"i-0987654321fedcba0", "aws"}, // registry.terraform.io/hashicorp/aws in module
		}

		// Arrange
		provider, err := tfstate.NewProvider(tfstate.Config{
			FilePath: "testdata/sample_ec2.tfstate",
		})
		require.NoError(t, err)

		// Act
		resources, err := provider.ListResources(context.Background(), domain.KindComputeInstance)

		// Assert
		require.NoError(t, err)
		for _, tc := range testCases {
			var found bool
			for _, res := range resources {
				if res.Metadata().ProviderAssignedID == tc.resourceId {
					found = true
					assert.Equal(t, tc.providerType, res.Metadata().ProviderType,
						"Resource %s should have provider type %s", tc.resourceId, tc.providerType)
				}
			}
			assert.True(t, found, "Should find resource with ID %s", tc.resourceId)
		}
	})

	t.Run("resource metadata and attributes accessors", func(t *testing.T) {
		// Arrange
		provider, err := tfstate.NewProvider(tfstate.Config{
			FilePath: "testdata/sample_ec2.tfstate",
		})
		require.NoError(t, err)

		// Act
		resources, err := provider.ListResources(context.Background(), domain.KindComputeInstance)

		// Assert
		require.NoError(t, err)
		require.NotEmpty(t, resources)

		// Verify the accessors work correctly
		resource := resources[0]

		// Metadata accessor should return consistent values
		meta1 := resource.Metadata()
		meta2 := resource.Metadata()
		assert.Equal(t, meta1, meta2, "Multiple calls to Metadata() should return same data")

		// Attributes accessor should return consistent values
		attrs1 := resource.Attributes()
		attrs2 := resource.Attributes()
		assert.Equal(t, attrs1, attrs2, "Multiple calls to Attributes() should return same data")
	})
}
