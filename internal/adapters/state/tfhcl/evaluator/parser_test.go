package evaluator

import (
	"context"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	portsmocks "github.com/olusolaa/infra-drift-detector/internal/core/ports/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestParseHCLFiles(t *testing.T) {
	mockLogger := portsmocks.NewLogger(t)
	mockLogger.On("Warnf", mock.Anything, mock.Anything).Return()
	mockLogger.On("Debugf", mock.Anything, mock.Anything, mock.Anything).Return()
	mockLogger.On("WithFields", mock.Anything).Return(mockLogger)

	ctx := context.Background()

	t.Run("Valid TF Files", func(t *testing.T) {
		dir := t.TempDir()
		path1 := createTestFile(t, dir, "main.tf", `resource "t" "a" {}`)
		filesMap, diags, err := parseHCLFiles(ctx, hclparse.NewParser(), dir, mockLogger)
		require.NoError(t, err)
		assert.False(t, DiagsHasFatalErrors(diags))
		require.Len(t, filesMap, 1)
		assert.Contains(t, filesMap, path1)
	})

	t.Run("No HCL Files Found", func(t *testing.T) {
		dir := t.TempDir()
		_, _, err := parseHCLFiles(ctx, hclparse.NewParser(), dir, mockLogger)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no HCL files")
	})

	t.Run("Fatal Parse Error", func(t *testing.T) {
		dir := t.TempDir()
		createTestFile(t, dir, "bad.tf", `resource "t" "bad" { = }`)
		_, diags, err := parseHCLFiles(ctx, hclparse.NewParser(), dir, mockLogger)
		require.NoError(t, err) // ParseHCLFiles itself doesn't return error for parse diags
		assert.True(t, DiagsHasFatalErrors(diags))
	})

	t.Run("Context Cancellation", func(t *testing.T) {
		dir := t.TempDir()
		createTestFile(t, dir, "a.tf", `resource "t" "a" {}`)
		createTestFile(t, dir, "b.tf", `resource "t" "b" {}`)
		ctxCancel, cancel := context.WithCancel(ctx)
		cancel()
		_, _, err := parseHCLFiles(ctxCancel, hclparse.NewParser(), dir, mockLogger)
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestFindResourceBlocksOfType(t *testing.T) {
	parser := hclparse.NewParser()
	file1, _ := parser.ParseHCL([]byte(`resource "aws_instance" "web" {}; resource "aws_s3_bucket" "logs" {}`), "f1.tf")
	file2, _ := parser.ParseHCL([]byte(`resource "aws_instance" "app" {}`), "f2.tf")
	filesMap := map[string]*hcl.File{"f1.tf": file1, "f2.tf": file2}

	t.Run("Find aws_instance", func(t *testing.T) {
		blocks, diags := FindResourceBlocksOfType(filesMap, domain.KindComputeInstance)
		assert.False(t, DiagsHasFatalErrors(diags))
		assert.Len(t, blocks, 2)
	})

	t.Run("Kind Not Found", func(t *testing.T) {
		blocks, diags := FindResourceBlocksOfType(filesMap, domain.KindDatabaseInstance)
		assert.False(t, DiagsHasFatalErrors(diags))
		assert.Empty(t, blocks)
	})
}

func TestFindSpecificResourceBlock(t *testing.T) {
	parser := hclparse.NewParser()
	path1 := "f1.tf"
	path2 := "f2.tf"
	file1, _ := parser.ParseHCL([]byte(`resource "aws_instance" "web" {}`), path1)
	file2, _ := parser.ParseHCL([]byte(`resource "aws_instance" "web" {}`), path2) // Duplicate
	filesMap := map[string]*hcl.File{path1: file1, path2: file2}

	t.Run("Found", func(t *testing.T) {
		// Need a non-duplicate setup
		singleFileMap := map[string]*hcl.File{path1: file1}
		block, diags := FindSpecificResourceBlock(singleFileMap, "aws_instance.web")
		assert.False(t, DiagsHasFatalErrors(diags))
		require.NotNil(t, block)
		assert.Equal(t, "aws_instance", block.Labels[0])
		assert.Equal(t, "web", block.Labels[1])
	})

	t.Run("Duplicate Found", func(t *testing.T) {
		block, diags := FindSpecificResourceBlock(filesMap, "aws_instance.web")
		assert.True(t, DiagsHasFatalErrors(diags))
		assert.Contains(t, diags.Error(), "Duplicate resource definition")
		assert.Nil(t, block)
	})

	t.Run("Not Found", func(t *testing.T) {
		block, diags := FindSpecificResourceBlock(filesMap, "aws_instance.db")
		assert.False(t, DiagsHasFatalErrors(diags))
		assert.Nil(t, block)
	})

	t.Run("Invalid Identifier", func(t *testing.T) {
		block, diags := FindSpecificResourceBlock(filesMap, "aws_instance")
		assert.True(t, DiagsHasFatalErrors(diags))
		assert.Contains(t, diags.Error(), "Invalid resource identifier format")
		assert.Nil(t, block)
	})
}
