package tfhcl

import (
	"context"
	"fmt"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl"
	"github.com/olusolaa/infra-drift-detector/test/testutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl/evaluator"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "tfhcl-parser-test-")
	require.NoError(t, err)
	return dir
}

func createTestHCLFile(t *testing.T, dir, filename, content string) string {
	t.Helper()
	filePath := filepath.Join(dir, filename)
	err := os.WriteFile(filePath, []byte(content), 0644)
	require.NoError(t, err)
	return filePath
}

func cleanupTestDir(t *testing.T, dir string) {
	t.Helper()
	os.RemoveAll(dir)
}

func TestParseHCLDirectory(t *testing.T) {
	mockLogger := testutil.NewMockLogger()
	ctx := context.Background()

	t.Run("Valid TF Files", func(t *testing.T) {
		dir := createTestDir(t)
		defer cleanupTestDir(t, dir)
		path1 := createTestHCLFile(t, dir, "main.tf", `resource "t" "a" {}`)
		createTestHCLFile(t, dir, "other.tf", `resource "t" "b" {}`)

		filesMap, diags, err := tfhcl.ParseHCLDirectory(ctx, dir, mockLogger)
		require.NoError(t, err)
		assert.False(t, evaluator.DiagsHasFatalErrors(diags))
		require.Len(t, filesMap, 2)
		assert.Contains(t, filesMap, path1)
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
		createTestHCLFile(t, dir, "bad.tf", `resource "t" "bad" { = }`)

		filesMap, diags, err := tfhcl.ParseHCLDirectory(ctx, dir, mockLogger)
		require.Error(t, err)
		assert.ErrorContains(t, err, "fatal errors encountered during HCL parsing")
		require.True(t, diags.HasErrors())
		assert.True(t, evaluator.DiagsHasFatalErrors(diags))
		assert.Contains(t, diags.Error(), "bad.tf")
		require.NotNil(t, filesMap)
		assert.Len(t, filesMap, 1)
		assert.Contains(t, filesMap, path1)
	})

	t.Run("Invalid JSON in TF.JSON File", func(t *testing.T) {
		dir := createTestDir(t)
		defer cleanupTestDir(t, dir)
		createTestHCLFile(t, dir, "bad.tf.json", `{"resource": {"t": {"json": {}}`)

		filesMap, diags, err := tfhcl.ParseHCLDirectory(ctx, dir, mockLogger)
		require.Error(t, err)
		assert.ErrorContains(t, err, "fatal errors encountered during HCL parsing")
		require.True(t, diags.HasErrors())
		assert.True(t, evaluator.DiagsHasFatalErrors(diags))
		assert.Contains(t, diags.Error(), "bad.tf.json")
		assert.Contains(t, diags.Error(), "Unclosed object")
		assert.Empty(t, filesMap)
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
	path3 := createTestHCLFile(t, dir, "f3.tf.json", `
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
		assert.Contains(t, findDiags.Error(), "aws_instance.web1")
		// Should contain the non-duplicate blocks found *before* the duplicate error stopped processing that address
		assert.Len(t, blocks, 2)    // web2, web3
		assert.Len(t, addresses, 2) // web2, web3 (duplicate web1 is skipped)
		assert.NotContains(t, addresses, "aws_instance.web1")
		assert.Contains(t, addresses, "aws_instance.web2")
		assert.Contains(t, addresses, "aws_instance.web3")
		assert.Equal(t, fmt.Sprintf("%s::%s", path2, "aws_instance.web2"), addresses["aws_instance.web2"])
		assert.Equal(t, fmt.Sprintf("%s::%s", path3, "aws_instance.web3"), addresses["aws_instance.web3"])
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

	path1 := createTestHCLFile(t, dir, "f1.tf", `resource "aws_instance" "specific" { ami = "f1-ami" }`)
	createTestHCLFile(t, dir, "f2.tf", `resource "aws_instance" "another" { ami = "f2-ami" }`)
	path3 := createTestHCLFile(t, dir, "f3_dup.tf", `resource "aws_instance" "specific" { ami = "f3-ami" }`)

	filesMap, _, _ := tfhcl.ParseHCLDirectory(ctx, dir, mockLogger)

	t.Run("Found", func(t *testing.T) {
		block, diags := tfhcl.FindSpecificResourceBlock(filesMap, "aws_instance.another")
		assert.False(t, diags.HasErrors(), diags.Error())
		require.NotNil(t, block)
		assert.Equal(t, "aws_instance", block.Labels[0])
		assert.Equal(t, "another", block.Labels[1])
	})

	t.Run("Duplicate Found", func(t *testing.T) {
		block, diags := tfhcl.FindSpecificResourceBlock(filesMap, "aws_instance.specific")
		require.True(t, diags.HasErrors())
		assert.True(t, evaluator.DiagsHasFatalErrors(diags))
		assert.Contains(t, diags.Error(), "Duplicate resource definition")
		assert.Contains(t, diags.Error(), "aws_instance.specific")
		assert.Contains(t, diags.Error(), path1)
		assert.Contains(t, diags.Error(), path3)
		assert.Nil(t, block)
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
