package evaluator

import (
	"context"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl/evaluator"
	"github.com/olusolaa/infra-drift-detector/test/testutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func createTestFile(t *testing.T, dir, filename, content string) string {
	t.Helper()
	filePath := filepath.Join(dir, filename)
	err := os.WriteFile(filePath, []byte(content), 0644)
	require.NoError(t, err, "Failed to write test file: %s", filename)
	return filePath
}

func parseTestBody(t *testing.T, content string) hcl.Body {
	t.Helper()
	parser := hclparse.NewParser()
	file, diags := parser.ParseHCL([]byte(content), "test.tf")
	require.False(t, diags.HasErrors(), "Failed to parse test HCL body: %s", diags.Error())
	require.NotNil(t, file)
	require.NotNil(t, file.Body)
	return file.Body
}

func TestBuildEvalContext(t *testing.T) {
	mockLogger := testutil.NewMockLogger()
	ctx := context.Background()
	baseDir := t.TempDir()

	t.Run("No Vars No Locals", func(t *testing.T) {
		body := parseTestBody(t, `resource "test" "a" {}`)
		evalCtx, diags := evaluator.BuildEvalContext(ctx, body, nil, baseDir, "default", mockLogger)
		require.False(t, diags.HasErrors(), diags.Error())
		require.NotNil(t, evalCtx)

		pathVar, ok := evalCtx.Variables["path"]
		require.True(t, ok)
		require.Equal(t, cty.Object, pathVar.Type().FriendlyName())
		modulePath, _ := filepath.Abs(baseDir)
		assert.Equal(t, modulePath, pathVar.GetAttr("module").AsString())

		tfVar, ok := evalCtx.Variables["terraform"]
		require.True(t, ok)
		assert.Equal(t, "default", tfVar.GetAttr("workspace").AsString())

		varVar, ok := evalCtx.Variables["var"]
		require.True(t, ok)
		assert.True(t, varVar.Type().IsObjectType())
		assert.Equal(t, 0, varVar.LengthInt())

		localVal, ok := evalCtx.Variables["local"]
		require.True(t, ok)
		assert.True(t, localVal.Type().IsObjectType())
		assert.Equal(t, 0, localVal.LengthInt())
	})

	t.Run("With Valid Vars File", func(t *testing.T) {
		varsContent := `
			region = "us-west-2"
			count  = 3
		`
		varsFile := createTestFile(t, baseDir, "test.tfvars", varsContent)
		body := parseTestBody(t, `resource "test" "a" {}`)

		evalCtx, diags := evaluator.BuildEvalContext(ctx, body, []string{varsFile}, baseDir, "prod", mockLogger)
		require.False(t, diags.HasErrors(), diags.Error())
		require.NotNil(t, evalCtx)

		varVar, ok := evalCtx.Variables["var"]
		require.True(t, ok)
		assert.Equal(t, 2, varVar.LengthInt())
		assert.Equal(t, "us-west-2", varVar.GetAttr("region").AsString())
		assert.True(t, cty.Number.Equals(varVar.GetAttr("count").Type()))
		countVal, _ := varVar.GetAttr("count").AsBigFloat().Float64()
		assert.Equal(t, float64(3), countVal)
	})

	t.Run("With Multiple Vars Files", func(t *testing.T) {
		varsContent1 := `region = "us-west-1"`
		varsContent2 := `count = 5`
		varsFile1 := createTestFile(t, baseDir, "a.tfvars", varsContent1)
		varsFile2 := createTestFile(t, baseDir, "b.tfvars", varsContent2)
		body := parseTestBody(t, `resource "test" "a" {}`)

		evalCtx, diags := evaluator.BuildEvalContext(ctx, body, []string{varsFile1, varsFile2}, baseDir, "prod", mockLogger)
		require.False(t, diags.HasErrors(), diags.Error())
		require.NotNil(t, evalCtx)

		varVar := evalCtx.Variables["var"]
		assert.Equal(t, 2, varVar.LengthInt())
		assert.Equal(t, "us-west-1", varVar.GetAttr("region").AsString())
		countVal, _ := varVar.GetAttr("count").AsBigFloat().Float64()
		assert.Equal(t, float64(5), countVal)
	})

	t.Run("With Invalid Vars Path", func(t *testing.T) {
		body := parseTestBody(t, `resource "test" "a" {}`)
		_, diags := evaluator.BuildEvalContext(ctx, body, []string{"nonexistent.tfvars"}, baseDir, "default", mockLogger)
		assert.True(t, diags.HasErrors())
		assert.True(t, evaluator.DiagsHasFatalErrors(diags))
		assert.Contains(t, diags.Error(), "Cannot read variables file")
	})

	t.Run("With Vars Syntax Error", func(t *testing.T) {
		varsFile := createTestFile(t, baseDir, "bad.tfvars", `name = "test`)
		body := parseTestBody(t, `resource "test" "a" {}`)
		_, diags := evaluator.BuildEvalContext(ctx, body, []string{varsFile}, baseDir, "default", mockLogger)
		assert.True(t, diags.HasErrors())
		assert.True(t, evaluator.DiagsHasFatalErrors(diags))
		assert.Contains(t, diags.Error(), "bad.tfvars")
	})

	t.Run("With Simple Locals", func(t *testing.T) {
		body := parseTestBody(t, `
			locals {
				service_name = "my-service"
				is_enabled   = true
			}
			resource "test" "a" {}
		`)
		evalCtx, diags := evaluator.BuildEvalContext(ctx, body, nil, baseDir, "default", mockLogger)
		require.False(t, diags.HasErrors(), diags.Error())
		require.NotNil(t, evalCtx)

		localVal := evalCtx.Variables["local"]
		require.True(t, localVal.Type().IsObjectType())
		assert.Equal(t, 2, localVal.LengthInt())
		assert.Equal(t, "my-service", localVal.GetAttr("service_name").AsString())
		assert.Equal(t, true, localVal.GetAttr("is_enabled").True())
	})

	t.Run("With Locals Referencing Vars", func(t *testing.T) {
		varsContent := `prefix = "prod"`
		varsFile := createTestFile(t, baseDir, "vars.tfvars", varsContent)
		body := parseTestBody(t, `
			locals {
				full_name = "${var.prefix}-app"
			}
			resource "test" "a" {}
		`)
		evalCtx, diags := evaluator.BuildEvalContext(ctx, body, []string{varsFile}, baseDir, "default", mockLogger)
		require.False(t, diags.HasErrors(), diags.Error())
		require.NotNil(t, evalCtx)

		localVal := evalCtx.Variables["local"]
		require.Equal(t, 1, localVal.LengthInt())
		assert.Equal(t, "prod-app", localVal.GetAttr("full_name").AsString())
	})

	t.Run("With Locals Evaluation Error", func(t *testing.T) {
		body := parseTestBody(t, `
			locals {
				bad = var.nonexistent
			}
			resource "test" "a" {}
		`)
		evalCtx, diags := evaluator.BuildEvalContext(ctx, body, nil, baseDir, "default", mockLogger)
		assert.True(t, diags.HasErrors())
		assert.True(t, evaluator.DiagsHasFatalErrors(diags))
		assert.Nil(t, evalCtx)
		assert.Contains(t, diags.Error(), "var.nonexistent")
	})

	t.Run("With Duplicate Locals", func(t *testing.T) {
		body := parseTestBody(t, `
			locals {
				name = "a"
			}
            locals {
                name = "b"
            }
			resource "test" "a" {}
		`)
		evalCtx, diags := evaluator.BuildEvalContext(ctx, body, nil, baseDir, "default", mockLogger)
		assert.True(t, diags.HasErrors())
		assert.True(t, evaluator.DiagsHasFatalErrors(diags))
		assert.Nil(t, evalCtx)
		assert.Contains(t, diags.Error(), "Duplicate local value definition")
		assert.Contains(t, diags.Error(), `named "name"`)
	})
}
