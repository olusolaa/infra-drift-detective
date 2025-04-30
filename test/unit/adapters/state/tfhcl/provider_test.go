package tfhcl_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
)

func TestNewProvider(t *testing.T) {
	t.Run("valid configuration", func(t *testing.T) {
		// Arrange
		cfg := tfhcl.Config{
			Directory: "testdata",
		}
		mockLogger := NewTestLogger()

		// Act
		provider, err := tfhcl.NewProvider(cfg, mockLogger)

		// Assert
		require.NoError(t, err)
		require.NotNil(t, provider)
		assert.Equal(t, tfhcl.ProviderTypeTFHCL, provider.Type())
	})

	t.Run("empty directory", func(t *testing.T) {
		// Arrange
		cfg := tfhcl.Config{
			Directory: "",
		}
		mockLogger := NewTestLogger()

		// Act
		provider, err := tfhcl.NewProvider(cfg, mockLogger)

		// Assert
		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-empty directory path")
		assert.Nil(t, provider)
	})
}

func TestListResources(t *testing.T) {
	// Arrange
	cfg := tfhcl.Config{
		Directory: "testdata",
	}
	mockLogger := NewTestLogger()
	provider, err := tfhcl.NewProvider(cfg, mockLogger)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("list compute instances", func(t *testing.T) {
		// Act
		resources, err := provider.ListResources(ctx, domain.KindComputeInstance)

		// Assert
		require.NoError(t, err)
		require.Len(t, resources, 1)

		resource := resources[0]
		assert.Equal(t, domain.KindComputeInstance, resource.Metadata().Kind)
		assert.Equal(t, "aws_instance.web", resource.Metadata().SourceIdentifier)

		// Check attributes
		attrs := resource.Attributes()
		assert.Equal(t, "t2.micro", attrs[domain.ComputeInstanceTypeKey])
		assert.Equal(t, "ami-0c55b159cbfafe1f0", attrs[domain.ComputeImageIDKey])
		assert.Equal(t, "subnet-12345678", attrs[domain.ComputeSubnetIDKey])
		assert.Equal(t, "us-west-2a", attrs[domain.ComputeAvailabilityZoneKey])

		// Check security groups
		secGroups, ok := attrs[domain.ComputeSecurityGroupsKey].([]interface{})
		require.True(t, ok)
		assert.ElementsMatch(t, []interface{}{"sg-12345678", "sg-87654321"}, secGroups)

		// Check tags
		tags, ok := attrs[domain.KeyTags].(map[string]string)
		require.True(t, ok)
		assert.Equal(t, "WebServer", tags["Name"])
		assert.Equal(t, "production", tags["Environment"])
	})

	t.Run("list storage buckets", func(t *testing.T) {
		// Act
		resources, err := provider.ListResources(ctx, domain.KindStorageBucket)

		// Assert
		require.NoError(t, err)
		require.Len(t, resources, 1)

		resource := resources[0]
		assert.Equal(t, domain.KindStorageBucket, resource.Metadata().Kind)
		assert.Equal(t, "aws_s3_bucket.data", resource.Metadata().SourceIdentifier)

		// Check attributes
		attrs := resource.Attributes()
		assert.Equal(t, "private", attrs[domain.StorageBucketACLKey])

		// Check versioning
		versioning, ok := attrs[domain.StorageBucketVersioningKey].(bool)
		require.True(t, ok)
		assert.True(t, versioning)

		// Check tags
		tags, ok := attrs[domain.KeyTags].(map[string]string)
		require.True(t, ok)
		assert.Equal(t, "DataBucket", tags["Name"])
		assert.Equal(t, "production", tags["Environment"])
	})

	t.Run("list unsupported resource kind", func(t *testing.T) {
		// Act
		resources, err := provider.ListResources(ctx, "UnsupportedKind")

		// Assert
		require.NoError(t, err)
		assert.Empty(t, resources)
	})
}

func TestGetResource(t *testing.T) {
	// Arrange
	cfg := tfhcl.Config{
		Directory: "testdata",
	}
	mockLogger := NewTestLogger()
	provider, err := tfhcl.NewProvider(cfg, mockLogger)
	require.NoError(t, err)

	ctx := context.Background()

	t.Run("get compute instance", func(t *testing.T) {
		// Act
		resource, err := provider.GetResource(ctx, domain.KindComputeInstance, "aws_instance.web")

		// Assert
		require.NoError(t, err)
		require.NotNil(t, resource)

		assert.Equal(t, domain.KindComputeInstance, resource.Metadata().Kind)
		assert.Equal(t, "aws_instance.web", resource.Metadata().SourceIdentifier)

		// Check attributes
		attrs := resource.Attributes()
		assert.Equal(t, "t2.micro", attrs[domain.ComputeInstanceTypeKey])
		assert.Equal(t, "ami-0c55b159cbfafe1f0", attrs[domain.ComputeImageIDKey])
	})

	t.Run("get storage bucket", func(t *testing.T) {
		// Act
		resource, err := provider.GetResource(ctx, domain.KindStorageBucket, "aws_s3_bucket.data")

		// Assert
		require.NoError(t, err)
		require.NotNil(t, resource)

		assert.Equal(t, domain.KindStorageBucket, resource.Metadata().Kind)
		assert.Equal(t, "aws_s3_bucket.data", resource.Metadata().SourceIdentifier)

		// Check attributes
		attrs := resource.Attributes()
		assert.Equal(t, "private", attrs[domain.StorageBucketACLKey])
	})

	t.Run("get non-existent resource", func(t *testing.T) {
		// Act
		resource, err := provider.GetResource(ctx, domain.KindComputeInstance, "aws_instance.nonexistent")

		// Assert
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found in HCL files")
		assert.Nil(t, resource)
	})

	t.Run("get resource with wrong kind", func(t *testing.T) {
		// Act
		resource, err := provider.GetResource(ctx, domain.KindDatabaseInstance, "aws_instance.web")

		// Assert
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found in HCL files")
		assert.Nil(t, resource)
	})
}
