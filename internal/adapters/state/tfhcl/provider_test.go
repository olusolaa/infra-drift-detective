// --- START OF FILE infra-drift-detector/internal/adapters/state/tfhcl/provider_test.go ---

package tfhcl_test

import (
	"context"
	portsmocks "github.com/olusolaa/infra-drift-detector/internal/core/ports/mocks"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	apperrors "github.com/olusolaa/infra-drift-detector/internal/errors"
)

func createTestHCLFile(t *testing.T, dir, filename, content string) string {
	t.Helper()
	filePath := filepath.Join(dir, filename)
	err := os.WriteFile(filePath, []byte(content), 0644)
	require.NoError(t, err, "Failed to write test HCL file: %s", filename)
	return filePath
}

type testHCLProvider struct {
	provider *tfhcl.Provider
	dir      string
	config   tfhcl.Config
}

func setupHCLTestProvider(t *testing.T, cfg tfhcl.Config) *testHCLProvider {
	t.Helper()
	mockLogger := portsmocks.NewLogger(t)
	if cfg.Directory == "" {
		cfg.Directory = t.TempDir()
	}

	absDir, err := filepath.Abs(cfg.Directory)
	require.NoError(t, err)
	cfg.Directory = absDir

	absVarFiles := make([]string, len(cfg.VarFiles))
	for i, vf := range cfg.VarFiles {
		if vf != "" && !filepath.IsAbs(vf) {
			absVarFiles[i] = filepath.Join(cfg.Directory, filepath.Base(vf))
		} else {
			absVarFiles[i] = vf
		}
	}
	cfg.VarFiles = absVarFiles

	p, err := tfhcl.NewProvider(cfg, mockLogger)
	require.NoError(t, err)
	require.NotNil(t, p)

	return &testHCLProvider{
		provider: p,
		dir:      cfg.Directory,
		config:   cfg,
	}
}

func TestTFHCLProvider_NewProvider(t *testing.T) {
	t.Run("Valid Config", func(t *testing.T) {
		cfg := tfhcl.Config{Directory: t.TempDir()}
		tp := setupHCLTestProvider(t, cfg)
		assert.NotNil(t, tp.provider)
		assert.Equal(t, cfg.Directory, tp.config.Directory) // Check resolved absolute path
		assert.Equal(t, "default", tp.config.Workspace)
	})

	t.Run("Missing Directory", func(t *testing.T) {
		mockLogger := portsmocks.NewLogger(t)
		cfg := tfhcl.Config{}
		_, err := tfhcl.NewProvider(cfg, mockLogger)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "requires a non-empty directory")
	})

	t.Run("Invalid Directory Path", func(t *testing.T) {
		mockLogger := portsmocks.NewLogger(t)
		cfg := tfhcl.Config{Directory: "dir\x00name"}
		_, err := tfhcl.NewProvider(cfg, mockLogger)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get absolute path")
	})
}

func TestTFHCLProvider_Type(t *testing.T) {
	tp := setupHCLTestProvider(t, tfhcl.Config{})
	assert.Equal(t, tfhcl.ProviderTypeTFHCL, tp.provider.Type())
}

func TestTFHCLProvider_ListResources(t *testing.T) {
	ctx := context.Background()

	t.Run("Success Simple", func(t *testing.T) {
		tp := setupHCLTestProvider(t, tfhcl.Config{})
		createTestHCLFile(t, tp.dir, "main.tf", `
            resource "aws_instance" "web" { instance_type = "t1.micro" }
            resource "aws_s3_bucket" "data" { bucket = "b1" }
            resource "aws_instance" "app" { instance_type = "t2.micro" }
        `)

		resources, err := tp.provider.ListResources(ctx, domain.KindComputeInstance)
		require.NoError(t, err)
		require.Len(t, resources, 2)
		ids := []string{resources[0].Metadata().SourceIdentifier, resources[1].Metadata().SourceIdentifier}
		assert.Contains(t, ids, "aws_instance.web")
		assert.Contains(t, ids, "aws_instance.app")
	})

	t.Run("Success With Vars and Locals", func(t *testing.T) {
		varFile := createTestHCLFile(t, t.TempDir(), "dev.tfvars", `region_code = "usw2"`) // Create var file in separate temp dir first
		tp := setupHCLTestProvider(t, tfhcl.Config{VarFiles: []string{varFile}})           // Pass the var file path
		createTestHCLFile(t, tp.dir, "vars.tf", `variable "region_code" { default = "use1" }`)
		createTestHCLFile(t, tp.dir, "main.tf", `
            locals { az = "${var.region_code}-az1" }
            resource "aws_instance" "main" { availability_zone = local.az }
        `)

		resources, err := tp.provider.ListResources(ctx, domain.KindComputeInstance)
		require.NoError(t, err)
		require.Len(t, resources, 1)
		assert.Equal(t, "aws_instance.main", resources[0].Metadata().SourceIdentifier)
		assert.Equal(t, "usw2-az1", resources[0].Attributes()[domain.ComputeAvailabilityZoneKey])
	})

	t.Run("Initialization Error - Parse Error", func(t *testing.T) {
		tp := setupHCLTestProvider(t, tfhcl.Config{})
		createTestHCLFile(t, tp.dir, "bad.tf", `resource "a" "b" { = }`)

		_, err := tp.provider.ListResources(ctx, domain.KindComputeInstance)
		require.Error(t, err)
		assert.ErrorContains(t, err, "HCL provider initialization failed")
		assert.ErrorContains(t, err, "fatal parsing errors")

		var appErr *apperrors.AppError
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, apperrors.CodeStateReadError, appErr.Code)
	})

	t.Run("Initialization Error - Missing Required Variable", func(t *testing.T) {
		tp := setupHCLTestProvider(t, tfhcl.Config{})
		createTestHCLFile(t, tp.dir, "vars.tf", `variable "needed" {}`) // No default, no tfvars
		createTestHCLFile(t, tp.dir, "main.tf", `resource "a" "b" { val = var.needed }`)

		_, err := tp.provider.ListResources(ctx, "a") // Requesting a dummy kind
		require.Error(t, err)
		assert.ErrorContains(t, err, "HCL provider initialization failed")
		assert.ErrorContains(t, err, "Missing required variable")
		assert.ErrorContains(t, err, `"needed"`)
	})

	t.Run("Resource Evaluation Error", func(t *testing.T) {
		tp := setupHCLTestProvider(t, tfhcl.Config{})
		createTestHCLFile(t, tp.dir, "main.tf", `
            resource "aws_instance" "good" { instance_type = "t2.nano" }
            resource "aws_instance" "bad" { instance_type = var.nope }
        `)

		resources, err := tp.provider.ListResources(ctx, domain.KindComputeInstance)
		require.NoError(t, err) // ListResources itself shouldn't fail, just skip the bad resource
		require.Len(t, resources, 1)
		assert.Equal(t, "aws_instance.good", resources[0].Metadata().SourceIdentifier)
	})

	t.Run("Context Cancellation During Init", func(t *testing.T) {
		tp := setupHCLTestProvider(t, tfhcl.Config{})
		createTestHCLFile(t, tp.dir, "main.tf", `resource "a" "b" {}`) // File needed to trigger init
		ctxCancel, cancel := context.WithCancel(ctx)
		cancel()

		_, err := tp.provider.ListResources(ctxCancel, "a")
		require.ErrorIs(t, err, context.Canceled)
	})

	t.Run("No Resources of Kind Found", func(t *testing.T) {
		tp := setupHCLTestProvider(t, tfhcl.Config{})
		createTestHCLFile(t, tp.dir, "main.tf", `resource "aws_s3_bucket" "data" { bucket = "b1" }`)

		resources, err := tp.provider.ListResources(ctx, domain.KindComputeInstance)
		require.NoError(t, err)
		assert.Empty(t, resources)
	})

}

func TestTFHCLProvider_GetResource(t *testing.T) {
	ctx := context.Background()
	tp := setupHCLTestProvider(t, tfhcl.Config{})
	createTestHCLFile(t, tp.dir, "main.tf", `
        resource "aws_instance" "web" { instance_type = "t1.micro" }
        resource "aws_s3_bucket" "data" { bucket = "b1" }
    `)
	createTestHCLFile(t, tp.dir, "dup.tf", `
        resource "aws_instance" "web" { instance_type = "t2.nano" } # Duplicate!
    `)

	t.Run("Success", func(t *testing.T) {
		tpSingle := setupHCLTestProvider(t, tfhcl.Config{})
		createTestHCLFile(t, tpSingle.dir, "main.tf", `
            resource "aws_instance" "web" { instance_type = "t1.micro" }
            resource "aws_s3_bucket" "data" { bucket = "b1" }
        `)
		res, err := tpSingle.provider.GetResource(ctx, domain.KindComputeInstance, "aws_instance.web")
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Equal(t, "aws_instance.web", res.Metadata().SourceIdentifier)
		assert.Equal(t, "t1.micro", res.Attributes()[domain.ComputeInstanceTypeKey])
	})

	t.Run("Not Found - Wrong ID", func(t *testing.T) {
		tpSingle := setupHCLTestProvider(t, tfhcl.Config{})
		createTestHCLFile(t, tpSingle.dir, "main.tf", `resource "aws_instance" "web" {}`)
		_, err := tpSingle.provider.GetResource(ctx, domain.KindComputeInstance, "aws_instance.db")
		require.Error(t, err)
		var appErr *apperrors.AppError
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, apperrors.CodeResourceNotFound, appErr.Code)
	})

	t.Run("Not Found - Wrong Kind", func(t *testing.T) {
		tpSingle := setupHCLTestProvider(t, tfhcl.Config{})
		createTestHCLFile(t, tpSingle.dir, "main.tf", `resource "aws_instance" "web" {}`)
		_, err := tpSingle.provider.GetResource(ctx, domain.KindStorageBucket, "aws_instance.web")
		require.Error(t, err)
		var appErr *apperrors.AppError
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, apperrors.CodeResourceNotFound, appErr.Code)
		assert.ErrorContains(t, err, "expected 'StorageBucket'")
	})

	t.Run("Initialization Error", func(t *testing.T) {
		tpErr := setupHCLTestProvider(t, tfhcl.Config{})
		createTestHCLFile(t, tpErr.dir, "bad.tf", `resource "a" "b" { = }`)
		_, err := tpErr.provider.GetResource(ctx, domain.KindComputeInstance, "a.b")
		require.Error(t, err)
		assert.ErrorContains(t, err, "HCL provider initialization failed")
	})

	t.Run("Resource Evaluation Error", func(t *testing.T) {
		tpEvalErr := setupHCLTestProvider(t, tfhcl.Config{})
		createTestHCLFile(t, tpEvalErr.dir, "main.tf", `resource "aws_instance" "web" { instance_type = var.nope }`)
		_, err := tpEvalErr.provider.GetResource(ctx, domain.KindComputeInstance, "aws_instance.web")
		require.Error(t, err)
		assert.ErrorContains(t, err, "Errors evaluating target HCL block")
		assert.ErrorContains(t, err, "var.nope")
	})

	t.Run("Duplicate Resource Definition Error", func(t *testing.T) {
		_, err := tp.provider.GetResource(ctx, domain.KindComputeInstance, "aws_instance.web")
		require.Error(t, err)
		assert.ErrorContains(t, err, "Fatal error finding specific HCL block")
		assert.ErrorContains(t, err, "Duplicate resource definition")
	})

	t.Run("Context Cancellation", func(t *testing.T) {
		tpSingle := setupHCLTestProvider(t, tfhcl.Config{})
		createTestHCLFile(t, tpSingle.dir, "main.tf", `resource "aws_instance" "web" {}`)
		ctxCancel, cancel := context.WithTimeout(ctx, 1*time.Millisecond) // Short timeout
		defer cancel()
		_, err := tpSingle.provider.GetResource(ctxCancel, domain.KindComputeInstance, "aws_instance.web")
		require.Error(t, err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})
}
