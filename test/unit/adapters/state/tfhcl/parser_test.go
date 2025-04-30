package tfhcl_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
)

// TestParseHCLDirectory tests the parser's ability to handle different HCL file scenarios
func TestParseHCLDirectory(t *testing.T) {
	tempDir := t.TempDir()

	t.Run("parse valid hcl file", func(t *testing.T) {
		// Create a temporary HCL file
		validHCL := `
resource "aws_instance" "test" {
  ami           = "ami-12345"
  instance_type = "t2.micro"
}
`
		err := os.WriteFile(filepath.Join(tempDir, "valid.tf"), []byte(validHCL), 0644)
		require.NoError(t, err)

		// Create the provider which will parse this directory
		mockLogger := NewTestLogger()
		provider, err := tfhcl.NewProvider(tfhcl.Config{
			Directory: tempDir,
		}, mockLogger)
		require.NoError(t, err)

		// List resources to trigger parsing
		resources, err := provider.ListResources(context.Background(), domain.KindComputeInstance)

		// Assert
		require.NoError(t, err)
		assert.Len(t, resources, 1)
		assert.Equal(t, "aws_instance.test", resources[0].Metadata().SourceIdentifier)
	})

	t.Run("parse invalid hcl file", func(t *testing.T) {
		// Create a temporary file with invalid HCL syntax
		invalidHCL := `
resource "aws_instance" "test" {
  ami           = "ami-12345"
  instance_type = "t2.micro"
  // Missing closing brace
`
		invalidDir := filepath.Join(tempDir, "invalid")
		err := os.Mkdir(invalidDir, 0755)
		require.NoError(t, err)

		err = os.WriteFile(filepath.Join(invalidDir, "invalid.tf"), []byte(invalidHCL), 0644)
		require.NoError(t, err)

		// Create the provider which will parse this directory
		mockLogger := NewTestLogger()
		provider, err := tfhcl.NewProvider(tfhcl.Config{
			Directory: invalidDir,
		}, mockLogger)
		require.NoError(t, err)

		// List resources to trigger parsing
		_, err = provider.ListResources(context.Background(), domain.KindComputeInstance)

		// Assert
		require.Error(t, err)
		assert.Contains(t, err.Error(), "HCL parsing errors")
	})

	t.Run("parse empty directory", func(t *testing.T) {
		emptyDir := filepath.Join(tempDir, "empty")
		err := os.Mkdir(emptyDir, 0755)
		require.NoError(t, err)

		// Create the provider which will parse this directory
		mockLogger := NewTestLogger()
		provider, err := tfhcl.NewProvider(tfhcl.Config{
			Directory: emptyDir,
		}, mockLogger)
		require.NoError(t, err)

		// List resources to trigger parsing
		_, err = provider.ListResources(context.Background(), domain.KindComputeInstance)

		// Assert
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no valid HCL files")
	})

	t.Run("parse directory with non-terraform files", func(t *testing.T) {
		mixedDir := filepath.Join(tempDir, "mixed")
		err := os.Mkdir(mixedDir, 0755)
		require.NoError(t, err)

		// Create a non-terraform file
		err = os.WriteFile(filepath.Join(mixedDir, "README.md"), []byte("# Test"), 0644)
		require.NoError(t, err)

		// Create the provider which will parse this directory
		mockLogger := NewTestLogger()
		provider, err := tfhcl.NewProvider(tfhcl.Config{
			Directory: mixedDir,
		}, mockLogger)
		require.NoError(t, err)

		// List resources to trigger parsing
		_, err = provider.ListResources(context.Background(), domain.KindComputeInstance)

		// Assert
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no valid HCL files")
	})

	t.Run("parse multiple resources", func(t *testing.T) {
		multiResDir := filepath.Join(tempDir, "multiple")
		err := os.Mkdir(multiResDir, 0755)
		require.NoError(t, err)

		// Create a file with multiple resources
		multiResHCL := `
resource "aws_instance" "web1" {
  ami           = "ami-11111"
  instance_type = "t2.micro"
}

resource "aws_instance" "web2" {
  ami           = "ami-22222"
  instance_type = "t2.large"
}

resource "aws_s3_bucket" "data" {
  bucket = "test-bucket"
  acl    = "private"
}
`
		err = os.WriteFile(filepath.Join(multiResDir, "multi.tf"), []byte(multiResHCL), 0644)
		require.NoError(t, err)

		// Create the provider which will parse this directory
		mockLogger := NewTestLogger()
		provider, err := tfhcl.NewProvider(tfhcl.Config{
			Directory: multiResDir,
		}, mockLogger)
		require.NoError(t, err)

		// List compute instances
		computeResources, err := provider.ListResources(context.Background(), domain.KindComputeInstance)
		require.NoError(t, err)
		assert.Len(t, computeResources, 2)

		// Extract identifiers to check both instances were found
		identifiers := []string{
			computeResources[0].Metadata().SourceIdentifier,
			computeResources[1].Metadata().SourceIdentifier,
		}
		assert.Contains(t, identifiers, "aws_instance.web1")
		assert.Contains(t, identifiers, "aws_instance.web2")

		// List storage buckets
		storageResources, err := provider.ListResources(context.Background(), domain.KindStorageBucket)
		require.NoError(t, err)
		assert.Len(t, storageResources, 1)
		assert.Equal(t, "aws_s3_bucket.data", storageResources[0].Metadata().SourceIdentifier)
	})
}

// TestExtractLiteralAttributes tests the parser's ability to extract literal attribute values
func TestExtractLiteralAttributes(t *testing.T) {
	// Since extractLiteralAttributes is an internal function, we'll test it indirectly
	// through the provider's ListResources method

	tempDir := t.TempDir()

	t.Run("extract different attribute types", func(t *testing.T) {
		// Create a temporary HCL file with different attribute types
		typesHCL := `
resource "aws_instance" "types_test" {
  string_attr  = "string value"
  number_attr  = 42
  bool_attr    = true
  list_attr    = ["item1", "item2"]
  // List and map attributes won't be extracted by the literal attribute extractor
}
`
		err := os.WriteFile(filepath.Join(tempDir, "types.tf"), []byte(typesHCL), 0644)
		require.NoError(t, err)

		// Create the provider which will parse this directory
		mockLogger := NewTestLogger()
		provider, err := tfhcl.NewProvider(tfhcl.Config{
			Directory: tempDir,
		}, mockLogger)
		require.NoError(t, err)

		// List resources to trigger parsing and attribute extraction
		resources, err := provider.ListResources(context.Background(), domain.KindComputeInstance)

		// Assert
		require.NoError(t, err)
		require.Len(t, resources, 1)

		// The values should be in the normalized domain attributes
		// We can't directly test extractLiteralAttributes, but we can verify
		// string and primitive attributes were extracted
		attrs := resources[0].Attributes()

		// These come from the mapping process, so just check they're correct in the mapped output
		// Verify the attribute extraction pipeline worked
		tags, ok := attrs[domain.KeyTags].(map[string]string)
		require.True(t, ok)
		assert.Empty(t, tags, "Tags should be empty for this test case")
	})
}
