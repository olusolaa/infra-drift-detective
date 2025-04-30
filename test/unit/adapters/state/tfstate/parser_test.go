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

// Since parseStateFile and findResourcesInState are not exported,
// we need to test them indirectly through the Provider's public methods
func TestStateParser(t *testing.T) {
	t.Run("valid state file", func(t *testing.T) {
		// Arrange & Act
		provider, err := tfstate.NewProvider(tfstate.Config{
			FilePath: "testdata/sample_ec2.tfstate",
		})

		// Assert - if we can successfully create a provider and list resources
		// then the parseStateFile function works correctly
		require.NoError(t, err)
		require.NotNil(t, provider)

		// Test that we can list resources (which uses the parser internally)
		resources, err := provider.ListResources(context.Background(), domain.KindComputeInstance)
		require.NoError(t, err)
		require.NotEmpty(t, resources)
	})

	t.Run("file not found", func(t *testing.T) {
		// Arrange
		provider, err := tfstate.NewProvider(tfstate.Config{
			FilePath: "testdata/nonexistent_file.tfstate",
		})
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

	t.Run("empty file", func(t *testing.T) {
		// Arrange
		provider, err := tfstate.NewProvider(tfstate.Config{
			FilePath: "testdata/empty.tfstate",
		})
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

	t.Run("invalid json", func(t *testing.T) {
		// Arrange
		provider, err := tfstate.NewProvider(tfstate.Config{
			FilePath: "testdata/invalid_json.tfstate",
		})
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

	t.Run("missing format version", func(t *testing.T) {
		// Arrange
		provider, err := tfstate.NewProvider(tfstate.Config{
			FilePath: "testdata/no_format_version.tfstate",
		})
		require.NoError(t, err)

		// Act
		resources, err := provider.ListResources(context.Background(), domain.KindComputeInstance)

		// Assert
		require.Error(t, err)
		assert.Nil(t, resources)
		var appErr *errors.AppError
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, errors.CodeStateParseError, appErr.Code)
		assert.Contains(t, err.Error(), "unexpected state input, format version is missing")
	})
}

func TestFindResourcesByKind(t *testing.T) {
	t.Run("find resources of specific kind", func(t *testing.T) {
		// Arrange
		provider, err := tfstate.NewProvider(tfstate.Config{
			FilePath: "testdata/sample_ec2.tfstate",
		})
		require.NoError(t, err)

		// Act - This indirectly tests findResourcesInState
		resources, err := provider.ListResources(context.Background(), domain.KindComputeInstance)

		// Assert
		require.NoError(t, err)
		assert.NotEmpty(t, resources)
		for _, res := range resources {
			assert.Equal(t, domain.KindComputeInstance, res.Metadata().Kind)
		}
	})

	t.Run("no resources of requested kind", func(t *testing.T) {
		// Arrange
		provider, err := tfstate.NewProvider(tfstate.Config{
			FilePath: "testdata/sample_ec2.tfstate",
		})
		require.NoError(t, err)

		// Act
		resources, err := provider.ListResources(context.Background(), domain.KindStorageBucket)

		// Assert
		require.NoError(t, err)
		assert.Empty(t, resources)
	})

	t.Run("resources in child modules", func(t *testing.T) {
		// Arrange
		provider, err := tfstate.NewProvider(tfstate.Config{
			FilePath: "testdata/nested_modules.tfstate",
		})
		require.NoError(t, err)

		// Act
		resources, err := provider.ListResources(context.Background(), domain.KindComputeInstance)

		// Assert
		require.NoError(t, err)
		assert.NotEmpty(t, resources)

		// Verify we found resources from both root and child modules
		foundInRoot := false
		foundInChild := false
		for _, res := range resources {
			if res.Metadata().SourceIdentifier == "aws_instance.root_ec2" {
				foundInRoot = true
			}
			if res.Metadata().SourceIdentifier == "module.nested.aws_instance.child_ec2" {
				foundInChild = true
			}
		}
		assert.True(t, foundInRoot, "Should find resource in root module")
		assert.True(t, foundInChild, "Should find resource in child module")
	})
}
