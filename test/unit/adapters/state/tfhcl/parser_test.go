package tfhcl

import (
	"context"
	"fmt"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl"
	"github.com/olusolaa/infra-drift-detector/test/testutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl/evaluator" // For DiagsHasFatalErrors
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Re-use test helpers from provider_test.go if defined in separate file, else redefine
// func createTestDir(t *testing.T) string { ... }
// func createTestHCLFile(t *testing.T, dir, filename, content string) string { ... }

func TestParseHCLDirectory(t *testing.T) {
	mockLogger := testutil.NewMockLogger()
	ctx := context.Background()

	t.Run("Valid TF Files", func(t *testing.T) {
		dir := createTestDir(t)
		defer cleanupTestDir(t, dir)
		path1 := createTestHCLFile(t, dir, "main.tf", `resource "t" "a" {}`)
		path2 := createTestHCLFile(t, dir, "other.tf", `resource "t" "b" {}`)

		filesMap, diags, err := tfhcl.ParseHCLDirectory(ctx, dir, mockLogger)
		require.NoError(t, err)
		assert.False(t, evaluator.DiagsHasFatalErrors(diags))
		require.Len(t, filesMap, 2)
		assert.Contains(t, filesMap, path1)
		assert.Contains(t, filesMap, path2)
		assert.NotNil(t, filesMap[path1])
		assert.NotNil(t, filesMap[path2])
	})

	t.Run("Valid TF and TF.JSON Files", func(t *testing.T) {
		dir := createTestDir(t)
		defer cleanupTestDir(t, dir)
		path1 := createTestHCLFile(t, dir, "main.tf", `resource "t" "tf" {}`)
		path2 := createTestHCLFile(t, dir, "data.tf.json", `{"resource": {"t": {"json": {}}}}`)

		filesMap, diags, err := tfhcl.ParseHCLDirectory(ctx, dir, mockLogger)
		require.NoError(t, err)
		assert.False(t, evaluator.DiagsHasFatalErrors(diags))
		require.Len(t, filesMap, 2)
		assert.Contains(t, filesMap, path1)
		assert.Contains(t, filesMap, path2)
	})

	t.Run("Directory Not Found", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "nonexistent")
		_, _, err := tfhcl.ParseHCLDirectory(ctx, dir, mockLogger)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to read HCL directory")
	})

	t.Run("Empty Directory", func(t *testing.T) {
		dir := createTestDir(t)
		defer cleanupTestDir(t, dir)
		_, _, err := tfhcl.ParseHCLDirectory(ctx, dir, mockLogger)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no HCL files (.tf, .tf.json) found")
	})

	t.Run("Directory with Only Non-HCL Files", func(t *testing.T) {
		dir := createTestDir(t)
		defer cleanupTestDir(t, dir)
		createTestHCLFile(t, dir, "README.md", `# Test`)
		_, _, err := tfhcl.ParseHCLDirectory(ctx, dir, mockLogger)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no HCL files (.tf, .tf.json) found")
	})

	t.Run("Syntax Error in One File", func(t *testing.T) {
		dir := createTestDir(t)
		defer cleanupTestDir(t, dir)
		path1 := createTestHCLFile(t, dir, "good.tf", `resource "t" "good" {}`)
		createTestHCLFile(t, dir, "bad.tf", `resource "t" "bad" { = }`) // Syntax error

		filesMap, diags, err := tfhcl.ParseHCLDirectory(ctx, dir, mockLogger)
		require.Error(t, err) // Returns fatal error because one file failed parsing critically
		assert.True(t, evaluator.DiagsHasFatalErrors(diags))
		assert.Contains(t, diags.Error(), "bad.tf")
		assert.Contains(t, diags.Error(), "Operator expected") // Example syntax error detail
		require.NotNil(t, filesMap)                            // Should still return map with successfully parsed files
		assert.Contains(t, filesMap, path1)                    // Good file should be present
		assert.Len(t, filesMap, 1)                             // Only the good file
	})

	t.Run("Invalid JSON in TF.JSON File", func(t *testing.T) {
		dir := createTestDir(t)
		defer cleanupTestDir(t, dir)
		createTestHCLFile(t, dir, "bad.tf.json", `{"resource": {"t": {"json": {}}`) // Missing closing brace

		filesMap, diags, err := tfhcl.ParseHCLDirectory(ctx, dir, mockLogger)
		require.Error(t, err)
		assert.True(t, evaluator.DiagsHasFatalErrors(diags))
		assert.Contains(t, diags.Error(), "bad.tf.json")
		assert.Contains(t, diags.Error(), "Syntax error") // JSON syntax error detail
		assert.Empty(t, filesMap)                         // No files parsed successfully
	})
}

func TestFindResourceBlocks(t *testing.T) {
	mockLogger := testutil.NewMockLogger()
	ctx := context.Background()
	dir := createTestDir(t)
	defer cleanupTestDir(t, dir)

	path1 := createTestHCLFile(t, dir, "f1.tf", `
        resource "aws_instance" "web1" { ami = "ami-1" }
        resource "aws_s3_bucket" "logs" { bucket = "log-bucket" }
    `)
	path2 := createTestHCLFile(t, dir, "f2.tf", `
        resource "aws_instance" "web2" { ami = "ami-2" }
        resource "aws_instance" "web1" { instance_type = "t3.micro" }
    `)
	createTestHCLFile(t, dir, "f3.tf.json", `
        {"resource": {"aws_instance": {"web3": {"ami": "ami-3"}}}}
    `)

	filesMap, parseDiags, err := tfhcl.ParseHCLDirectory(ctx, dir, mockLogger)
	require.NoError(t, err)
	require.False(t, evaluator.DiagsHasFatalErrors(parseDiags))

	t.Run("Find aws_instance", func(t *testing.T) {
		blocks, addresses, findDiags := tfhcl.FindResourceBlocks(filesMap, domain.KindComputeInstance)
		assert.True(t, findDiags.HasErrors())
		assert.True(t, evaluator.DiagsHasFatalErrors(findDiags))
		assert.Contains(t, findDiags.Error(), "Duplicate resource address")
		assert.Len(t, blocks, 3) // Check count found
		assert.Len(t, addresses, 3)
		assert.Contains(t, addresses, "aws_instance.web1")
		assert.Contains(t, addresses, "aws_instance.web2")
		assert.Contains(t, addresses, "aws_instance.web3")
		assert.Equal(t, fmt.Sprintf("%s::%s", path1, "aws_instance.web1"), addresses["aws_instance.web1"])
		assert.Equal(t, fmt.Sprintf("%s::%s", path2, "aws_instance.web2"), addresses["aws_instance.web2"])
		// Note: Duplicate web1 from path2 is skipped, address maps to the one from path1
	})

	t.Run("Find aws_s3_bucket", func(t *testing.T) {
		blocks, addresses, findDiags := tfhcl.FindResourceBlocks(filesMap, domain.KindStorageBucket)
		assert.False(t, findDiags.HasErrors())
		assert.Len(t, blocks, 1)
		assert.Equal(t, "aws_s3_bucket", blocks[0].Labels[0])
		assert.Len(t, addresses, 1)
		assert.Contains(t, addresses, "aws_s3_bucket.logs")
		assert.Equal(t, fmt.Sprintf("%s::%s", path1, "aws_s3_bucket.logs"), addresses["aws_s3_bucket.logs"])
	})

	t.Run("Kind Not Found", func(t *testing.T) {
		blocks, _, findDiags := tfhcl.FindResourceBlocks(filesMap, domain.KindDatabaseInstance)
		assert.False(t, findDiags.HasErrors())
		assert.Empty(t, blocks)
	})
}

func TestFindSpecificResourceBlock(t *testing.T) {
	mockLogger := testutil.NewMockLogger()
	ctx := context.Background()
	dir := createTestDir(t)
	defer cleanupTestDir(t, dir)

	createTestHCLFile(t, dir, "f2.tf", `resource "aws_instance" "another" { ami = "f2-ami" }`)
	createTestHCLFile(t, dir, "f3_dup.tf", `resource "aws_instance" "specific" { ami = "f3-ami" }`) // Duplicate

	filesMap, _, _ := tfhcl.ParseHCLDirectory(ctx, dir, mockLogger) // Ignore diags/err for this test focus

	t.Run("Found", func(t *testing.T) {
		block, diags := tfhcl.FindSpecificResourceBlock(filesMap, "aws_instance.another")
		assert.False(t, diags.HasErrors(), diags.Error())
		require.NotNil(t, block)
		assert.Equal(t, "aws_instance", block.Labels[0])
		assert.Equal(t, "another", block.Labels[1])
	})

	t.Run("Duplicate Found", func(t *testing.T) {
		block, diags := tfhcl.FindSpecificResourceBlock(filesMap, "aws_instance.specific")
		assert.True(t, diags.HasErrors())
		assert.True(t, evaluator.DiagsHasFatalErrors(diags))
		assert.Contains(t, diags.Error(), "Duplicate resource definition")
		assert.Contains(t, diags.Error(), "aws_instance.specific")
		assert.Nil(t, block) // Should return nil on fatal duplicate error
	})

	t.Run("Not Found", func(t *testing.T) {
		block, diags := tfhcl.FindSpecificResourceBlock(filesMap, "aws_instance.nonexistent")
		assert.False(t, diags.HasErrors())
		assert.Nil(t, block)
	})

	t.Run("Invalid Identifier Format", func(t *testing.T) {
		block, diags := tfhcl.FindSpecificResourceBlock(filesMap, "aws_instance-invalid")
		assert.True(t, diags.HasErrors())
		assert.True(t, evaluator.DiagsHasFatalErrors(diags))
		assert.Contains(t, diags.Error(), "Invalid resource identifier")
		assert.Nil(t, block)
	})
}

// Cleanup helper
func cleanupTestDir(t *testing.T, dir string) {
	t.Helper()
	os.RemoveAll(dir)
}
