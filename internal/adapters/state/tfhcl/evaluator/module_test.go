// --- START OF FILE infra-drift-detector/internal/adapters/state/tfhcl/evaluator/module_test.go ---

package evaluator

import (
	"context"
	portsmocks "github.com/olusolaa/infra-drift-detector/internal/core/ports/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

// --- Test Helper ---
func createTestFile(t *testing.T, dir, filename, content string) string {
	t.Helper()
	filePath := filepath.Join(dir, filename)
	err := os.WriteFile(filePath, []byte(content), 0644)
	require.NoError(t, err, "Failed to write test file: %s", filename)
	return filePath
}

func TestLoadModule_Variables(t *testing.T) {
	mockLogger := portsmocks.NewLogger(t)
	mockLogger.On("WithFields", mock.Anything).Return(mockLogger).Maybe()
	mockLogger.On("Debugf", mock.Anything, mock.Anything, mock.Anything).Return().Maybe() // Adjust arg count
	mockLogger.On("Warnf", mock.Anything, mock.Anything, mock.Anything).Return().Maybe()  // Adjust arg count
	mockLogger.On("Errorf", mock.Anything, mock.Anything, mock.Anything).Return().Maybe()
	ctx := context.Background()

	t.Run("Defaults Only", func(t *testing.T) {
		dir := t.TempDir()
		createTestFile(t, dir, "vars.tf", `
            variable "region" { default = "eu-central-1" }
        `)
		_, mod, err := LoadModule(ctx, dir, nil, "default", mockLogger)
		require.NoError(t, err) // Should pass now
		require.NotNil(t, mod)
		evalCtx := mod.EvalContext()
		require.NotNil(t, evalCtx)
		varMap := evalCtx.Variables["var"]
		assert.Equal(t, "eu-central-1", varMap.GetAttr("region").AsString())
	})

	t.Run("Tfvars Override", func(t *testing.T) {
		dir := t.TempDir()
		createTestFile(t, dir, "vars.tf", `variable "region" { default = "us-east-1" }`)
		varFile := createTestFile(t, dir, "prod.tfvars", `region = "us-west-2"`)
		_, mod, err := LoadModule(ctx, dir, []string{varFile}, "prod", mockLogger)
		require.NoError(t, err) // Should pass now
		require.NotNil(t, mod)
		evalCtx := mod.EvalContext()
		require.NotNil(t, evalCtx)
		assert.Equal(t, "us-west-2", evalCtx.Variables["var"].GetAttr("region").AsString())
	})

	t.Run("Tfvars Type Conversion", func(t *testing.T) {
		dir := t.TempDir()
		createTestFile(t, dir, "vars.tf", `variable "num" { type = number }`) // Type influences conversion
		varFile := createTestFile(t, dir, "num.tfvars", `num = "123"`)
		_, mod, err := LoadModule(ctx, dir, []string{varFile}, "default", mockLogger)
		require.NoError(t, err) // Conversion should succeed during load
		require.NotNil(t, mod)
		evalCtx := mod.EvalContext()
		require.NotNil(t, evalCtx)
		numVal := evalCtx.Variables["var"].GetAttr("num")
		require.NotNil(t, numVal, "Variable 'num' should exist in context")
	})

	t.Run("Missing Required Var", func(t *testing.T) {
		dir := t.TempDir()
		createTestFile(t, dir, "vars.tf", `variable "required_var" {}`)
		_, _, err := LoadModule(ctx, dir, nil, "default", mockLogger)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Missing required variable")
	})

	t.Run("Correct Variable Type Decoding (Conversion)", func(t *testing.T) {
		dir := t.TempDir()
		createTestFile(t, dir, "vars.tf", `
            variable "string_var" { type = string }
            variable "number_var" { type = number }
            variable "bool_var" { type = bool }
        `)
		varFile := createTestFile(t, dir, "types.tfvars", `
            string_var = 123
            number_var = "456"
            bool_var = "true"
        `)
		_, mod, err := LoadModule(ctx, dir, []string{varFile}, "default", mockLogger)
		require.NoError(t, err)
		require.NotNil(t, mod)
		evalCtx := mod.EvalContext()
		require.NotNil(t, evalCtx)
	})
}

func TestLoadModule_Locals(t *testing.T) {
	mockLogger := portsmocks.NewLogger(t)
	mockLogger.On("WithFields", mock.Anything).Return(mockLogger).Maybe()
	mockLogger.On("Debugf", mock.Anything, mock.Anything, mock.Anything).Return().Maybe() // Adjust arg count
	mockLogger.On("Warnf", mock.Anything, mock.Anything, mock.Anything).Return().Maybe()  // Adjust arg count
	mockLogger.On("Errorf", mock.Anything, mock.Anything, mock.Anything).Return().Maybe()
	ctx := context.Background()

	t.Run("Simple Locals", func(t *testing.T) {
		dir := t.TempDir()
		createTestFile(t, dir, "locals.tf", `locals { name = "service-a"}`)
		_, mod, err := LoadModule(ctx, dir, nil, "default", mockLogger)
		require.NoError(t, err) // Should pass now
		require.NotNil(t, mod)
		evalCtx := mod.EvalContext()
		require.NotNil(t, evalCtx)
		localMap := evalCtx.Variables["local"]
		require.NotNil(t, localMap)
	})

	t.Run("Locals Referencing Vars", func(t *testing.T) {
		dir := t.TempDir()
		createTestFile(t, dir, "vars.tf", `variable "env" { default = "dev" }`)
		createTestFile(t, dir, "locals.tf", `locals { full_name = "app-${var.env}" }`)
		_, mod, err := LoadModule(ctx, dir, nil, "default", mockLogger)
		require.NoError(t, err) // Should pass now
		require.NotNil(t, mod)
		evalCtx := mod.EvalContext()
		require.NotNil(t, evalCtx)
		require.NotNil(t, evalCtx.Variables["local"])
		assert.Equal(t, "app-dev", evalCtx.Variables["local"].GetAttr("full_name").AsString())
	})

	t.Run("Locals Evaluation Error", func(t *testing.T) {
		dir := t.TempDir()
		createTestFile(t, dir, "locals.tf", `locals { bad = var.nope }`) // var.nope doesn't exist
		_, _, err := LoadModule(ctx, dir, nil, "default", mockLogger)
		require.Error(t, err)                                             // Error during LoadModule due to eval errors in locals
		assert.Contains(t, err.Error(), "fatal errors evaluating locals") // Check the wrapped error source
		assert.Contains(t, err.Error(), "Unsupported attribute")
	})

	t.Run("Duplicate Locals", func(t *testing.T) {
		dir := t.TempDir()
		createTestFile(t, dir, "locals1.tf", `locals { name = "a" }`)
		createTestFile(t, dir, "locals2.tf", `locals { name = "b" }`)
		_, _, err := LoadModule(ctx, dir, nil, "default", mockLogger)
		require.Error(t, err) // Error during LoadModule due to duplicate definition in locals
		assert.ErrorContains(t, err, "fatal errors evaluating locals")
		assert.ErrorContains(t, err, "Duplicate local value definition")
	})
}
