package tfhcl_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
)

// TestMapHCLBlockToDomain tests the mapping of HCL blocks to domain resources
func TestMapHCLBlockToDomain(t *testing.T) {
	// Since mapHCLBlockToDomain is an internal function, we'll test it indirectly
	// through the provider's ListResources and GetResource methods

	t.Run("map compute instance block", func(t *testing.T) {
		// Setup
		cfg := tfhcl.Config{
			Directory: "testdata",
		}
		mockLogger := NewTestLogger()
		provider, err := tfhcl.NewProvider(cfg, mockLogger)
		require.NoError(t, err)

		// Test via ListResources
		resources, err := provider.ListResources(context.Background(), domain.KindComputeInstance)
		require.NoError(t, err)
		require.Len(t, resources, 1)

		// Assert mappings
		resource := resources[0]
		assert.Equal(t, domain.KindComputeInstance, resource.Metadata().Kind)
		assert.Equal(t, "aws", resource.Metadata().ProviderType)
		assert.Equal(t, "aws_instance.web", resource.Metadata().SourceIdentifier)

		// Check specific mappings
		attrs := resource.Attributes()
		assert.Equal(t, "t2.micro", attrs[domain.ComputeInstanceTypeKey])
		assert.Equal(t, "ami-0c55b159cbfafe1f0", attrs[domain.ComputeImageIDKey])
		assert.Equal(t, "subnet-12345678", attrs[domain.ComputeSubnetIDKey])

		// Check tags
		tags, ok := attrs[domain.KeyTags].(map[string]string)
		require.True(t, ok)
		assert.Equal(t, "WebServer", tags["Name"])
		assert.Equal(t, "production", tags["Environment"])

		// Ensure Name is set from tags
		assert.Equal(t, "WebServer", attrs[domain.KeyName])
	})

	t.Run("map storage bucket block", func(t *testing.T) {
		// Setup
		cfg := tfhcl.Config{
			Directory: "testdata",
		}
		mockLogger := NewTestLogger()
		provider, err := tfhcl.NewProvider(cfg, mockLogger)
		require.NoError(t, err)

		// Test via GetResource
		resource, err := provider.GetResource(context.Background(), domain.KindStorageBucket, "aws_s3_bucket.data")
		require.NoError(t, err)

		// Assert mappings
		assert.Equal(t, domain.KindStorageBucket, resource.Metadata().Kind)
		assert.Equal(t, "aws", resource.Metadata().ProviderType)
		assert.Equal(t, "aws_s3_bucket.data", resource.Metadata().SourceIdentifier)

		// Check specific mappings
		attrs := resource.Attributes()
		assert.Equal(t, "private", attrs[domain.StorageBucketACLKey])

		// Check versioning is mapped
		versioning, ok := attrs[domain.StorageBucketVersioningKey].(bool)
		require.True(t, ok)
		assert.True(t, versioning)

		// Check tags
		tags, ok := attrs[domain.KeyTags].(map[string]string)
		require.True(t, ok)
		assert.Equal(t, "DataBucket", tags["Name"])
		assert.Equal(t, "production", tags["Environment"])

		// Ensure Name is set from tags
		assert.Equal(t, "DataBucket", attrs[domain.KeyName])
	})
}

// TestTfHCLResource tests the implementation of the domain.StateResource interface
func TestTfHCLResource(t *testing.T) {
	t.Run("resource implementation", func(t *testing.T) {
		// Setup
		cfg := tfhcl.Config{
			Directory: "testdata",
		}
		mockLogger := NewTestLogger()
		provider, err := tfhcl.NewProvider(cfg, mockLogger)
		require.NoError(t, err)

		// Get a resource
		resource, err := provider.GetResource(context.Background(), domain.KindComputeInstance, "aws_instance.web")
		require.NoError(t, err)

		// Test the interface methods
		metadata := resource.Metadata()
		assert.Equal(t, domain.KindComputeInstance, metadata.Kind)
		assert.Equal(t, "aws_instance.web", metadata.SourceIdentifier)

		attrs := resource.Attributes()
		assert.NotEmpty(t, attrs)
		assert.Contains(t, attrs, domain.ComputeInstanceTypeKey)
		assert.Equal(t, "t2.micro", attrs[domain.ComputeInstanceTypeKey])
	})
}
