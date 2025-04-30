package tfstate_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfstate"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

// These tests will use the exported methods from the tfstate package
func TestNewProvider(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		// Arrange
		config := tfstate.Config{
			FilePath: "testdata/valid_state.tfstate",
		}

		// Act
		provider, err := tfstate.NewProvider(config)

		// Assert
		require.NoError(t, err)
		require.NotNil(t, provider)
		assert.Equal(t, "tfstate", provider.Type())
	})

	t.Run("empty file path", func(t *testing.T) {
		// Arrange
		config := tfstate.Config{
			FilePath: "",
		}

		// Act
		provider, err := tfstate.NewProvider(config)

		// Assert
		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-empty file path")
		assert.Nil(t, provider)
	})
}

func TestProviderType(t *testing.T) {
	// Arrange
	config := tfstate.Config{
		FilePath: "testdata/valid_state.tfstate",
	}
	provider, err := tfstate.NewProvider(config)
	require.NoError(t, err)

	// Act
	providerType := provider.Type()

	// Assert
	assert.Equal(t, "tfstate", providerType)
}

func TestListResources(t *testing.T) {
	t.Run("successful listing", func(t *testing.T) {
		// Arrange
		// Create a provider with a valid state file containing known resources
		config := tfstate.Config{
			FilePath: "testdata/sample_ec2.tfstate",
		}
		provider, err := tfstate.NewProvider(config)
		require.NoError(t, err)

		// Act
		resources, err := provider.ListResources(context.Background(), domain.KindComputeInstance)

		// Assert
		require.NoError(t, err)
		assert.NotEmpty(t, resources)
		for _, resource := range resources {
			assert.Equal(t, domain.KindComputeInstance, resource.Metadata().Kind)
			assert.NotEmpty(t, resource.Metadata().ProviderAssignedID)
		}
	})

	t.Run("file not found", func(t *testing.T) {
		// Arrange
		config := tfstate.Config{
			FilePath: "testdata/nonexistent_file.tfstate",
		}
		provider, err := tfstate.NewProvider(config)
		require.NoError(t, err)

		// Act
		resources, err := provider.ListResources(context.Background(), domain.KindComputeInstance)

		// Assert
		require.Error(t, err)
		assert.Nil(t, resources)
		var appErr *errors.AppError
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, errors.CodeStateReadError, appErr.Code)
	})

	t.Run("invalid json", func(t *testing.T) {
		// Arrange
		config := tfstate.Config{
			FilePath: "testdata/invalid_json.tfstate",
		}
		provider, err := tfstate.NewProvider(config)
		require.NoError(t, err)

		// Act
		resources, err := provider.ListResources(context.Background(), domain.KindComputeInstance)

		// Assert
		require.Error(t, err)
		assert.Nil(t, resources)
		var appErr *errors.AppError
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, errors.CodeStateParseError, appErr.Code)
	})

	t.Run("empty state file", func(t *testing.T) {
		// Arrange
		config := tfstate.Config{
			FilePath: "testdata/empty.tfstate",
		}
		provider, err := tfstate.NewProvider(config)
		require.NoError(t, err)

		// Act
		resources, err := provider.ListResources(context.Background(), domain.KindComputeInstance)

		// Assert
		require.Error(t, err)
		assert.Nil(t, resources)
		var appErr *errors.AppError
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, errors.CodeStateParseError, appErr.Code)
	})

	t.Run("no resources of requested kind", func(t *testing.T) {
		// Arrange
		config := tfstate.Config{
			FilePath: "testdata/sample_ec2.tfstate",
		}
		provider, err := tfstate.NewProvider(config)
		require.NoError(t, err)

		// Act
		resources, err := provider.ListResources(context.Background(), domain.KindStorageBucket)

		// Assert
		require.NoError(t, err)
		assert.Empty(t, resources)
	})

	t.Run("context cancellation", func(t *testing.T) {
		// Arrange
		config := tfstate.Config{
			FilePath: "testdata/sample_ec2.tfstate",
		}
		provider, err := tfstate.NewProvider(config)
		require.NoError(t, err)

		// Create a canceled context
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		// Act
		resources, err := provider.ListResources(ctx, domain.KindComputeInstance)

		// Assert
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Nil(t, resources)
	})

	t.Run("cached state is reused", func(t *testing.T) {
		// Arrange
		config := tfstate.Config{
			FilePath: "testdata/sample_ec2.tfstate", // This only needs to be valid on first call
		}
		provider, err := tfstate.NewProvider(config)
		require.NoError(t, err)

		// First call to parse and cache
		_, err = provider.ListResources(context.Background(), domain.KindComputeInstance)
		require.NoError(t, err)

		// Act - second call should use cached state
		resources, err := provider.ListResources(context.Background(), domain.KindComputeInstance)

		// Assert
		require.NoError(t, err)
		assert.NotEmpty(t, resources)
	})
}

func TestGetResource(t *testing.T) {
	t.Run("successfully retrieve existing resource", func(t *testing.T) {
		// Arrange
		config := tfstate.Config{
			FilePath: "testdata/sample_ec2.tfstate",
		}
		provider, err := tfstate.NewProvider(config)
		require.NoError(t, err)

		// Known resource address from the test file
		resourceAddress := "module.ec2_instances.aws_instance.web[0]"

		// Act
		resource, err := provider.GetResource(context.Background(), domain.KindComputeInstance, resourceAddress)

		// Assert
		require.NoError(t, err)
		require.NotNil(t, resource)
		assert.Equal(t, domain.KindComputeInstance, resource.Metadata().Kind)
		assert.Equal(t, resourceAddress, resource.Metadata().SourceIdentifier)
	})

	t.Run("resource not found", func(t *testing.T) {
		// Arrange
		config := tfstate.Config{
			FilePath: "testdata/sample_ec2.tfstate",
		}
		provider, err := tfstate.NewProvider(config)
		require.NoError(t, err)

		// Non-existent resource address
		resourceAddress := "non_existent_resource"

		// Act
		resource, err := provider.GetResource(context.Background(), domain.KindComputeInstance, resourceAddress)

		// Assert
		require.Error(t, err)
		assert.Nil(t, resource)
		var appErr *errors.AppError
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, errors.CodeResourceNotFound, appErr.Code)
	})

	t.Run("file not found", func(t *testing.T) {
		// Arrange
		config := tfstate.Config{
			FilePath: "testdata/nonexistent_file.tfstate",
		}
		provider, err := tfstate.NewProvider(config)
		require.NoError(t, err)

		// Act
		resource, err := provider.GetResource(context.Background(), domain.KindComputeInstance, "any_id")

		// Assert
		require.Error(t, err)
		assert.Nil(t, resource)
		var appErr *errors.AppError
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, errors.CodeStateReadError, appErr.Code)
	})

	t.Run("empty state values", func(t *testing.T) {
		// Arrange
		config := tfstate.Config{
			FilePath: "testdata/empty_values.tfstate",
		}
		provider, err := tfstate.NewProvider(config)
		require.NoError(t, err)

		// Act
		resource, err := provider.GetResource(context.Background(), domain.KindComputeInstance, "any_id")

		// Assert
		require.Error(t, err)
		assert.Nil(t, resource)
		var appErr *errors.AppError
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, errors.CodeResourceNotFound, appErr.Code)
	})

	t.Run("context cancellation", func(t *testing.T) {
		// Arrange
		config := tfstate.Config{
			FilePath: "testdata/sample_ec2.tfstate",
		}
		provider, err := tfstate.NewProvider(config)
		require.NoError(t, err)

		// Create a canceled context
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		// Act
		resource, err := provider.GetResource(ctx, domain.KindComputeInstance, "any_id")

		// Assert
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Nil(t, resource)
	})
}
