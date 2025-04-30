package tfhcl

import (
	"context"
	stderrors "errors"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl"
	"github.com/olusolaa/infra-drift-detector/test/testutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl/evaluator"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestHCLFile(t *testing.T, dir, filename, content string) string {
	t.Helper()
	filePath := filepath.Join(dir, filename)
	err := os.WriteFile(filePath, []byte(content), 0644)
	require.NoError(t, err, "Failed to write test HCL file: %s", filename)
	return filePath
}

func createTestDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "tfhcl-test-")
	require.NoError(t, err, "Failed to create temp dir for testing")
	return dir
}

type testHCLProvider struct {
	provider *tfhcl.Provider
	dir      string
	cleanup  func()
}

func setupHCLTestProvider(t *testing.T, cfg tfhcl.Config) *testHCLProvider {
	t.Helper()
	mockLogger := testutil.NewMockLogger()
	if cfg.Directory == "" {
		cfg.Directory = createTestDir(t)
	}

	absDir, err := filepath.Abs(cfg.Directory)
	require.NoError(t, err)
	cfg.Directory = absDir

	for i, vf := range cfg.VarFiles {
		if vf != "" && !filepath.IsAbs(vf) {
			cfg.VarFiles[i] = filepath.Join(cfg.Directory, filepath.Base(vf))
		}
	}

	p, err := tfhcl.NewProvider(cfg, mockLogger)
	require.NoError(t, err)
	require.NotNil(t, p)

	return &testHCLProvider{
		provider: p,
		dir:      cfg.Directory,
		cleanup:  func() { os.RemoveAll(cfg.Directory) },
	}
}

func TestTFHCLProvider_NewProvider(t *testing.T) {
	mockLogger := testutil.NewMockLogger()
	t.Run("Valid Config", func(t *testing.T) {
		dir := t.TempDir()
		defer cleanupTestDir(t, dir)
		cfg := tfhcl.Config{Directory: dir}
		p, err := tfhcl.NewProvider(cfg, mockLogger)
		assert.NoError(t, err)
		assert.NotNil(t, p)
		assert.Equal(t, dir, p.Config.Directory)
		assert.Equal(t, "default", p.Config.Workspace)
	})
	t.Run("Missing Directory", func(t *testing.T) {
		cfg := tfhcl.Config{}
		_, err := tfhcl.NewProvider(cfg, mockLogger)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "requires a directory")
	})
}

func TestTFHCLProvider_Type(t *testing.T) {
	tp := setupHCLTestProvider(t, tfhcl.Config{})
	defer tp.cleanup()
	assert.Equal(t, tfhcl.ProviderTypeTFHCL, tp.provider.Type())
}

func TestTFHCLProvider_ListResources_Success(t *testing.T) {
	cfg := tfhcl.Config{}
	tp := setupHCLTestProvider(t, cfg)
	defer tp.cleanup()

	createTestHCLFile(t, tp.dir, "ec2.tf", `
		resource "aws_instance" "web" {
		  instance_type = "t2.nano"
		  ami           = "ami-web"
		}
		resource "aws_instance" "app" {
		  instance_type = "t2.micro"
		  ami           = "ami-app"
		}
	`)
	createTestHCLFile(t, tp.dir, "s3.tf", `
		resource "aws_s3_bucket" "data" {
		  bucket = "my-data-bucket"
		}
	`)

	ctx := context.Background()
	resources, err := tp.provider.ListResources(ctx, domain.KindComputeInstance)
	require.NoError(t, err)
	require.Len(t, resources, 2)

	foundWeb := false
	foundApp := false
	for _, res := range resources {
		meta := res.Metadata()
		attrs := res.Attributes()
		require.Equal(t, domain.KindComputeInstance, meta.Kind)
		if meta.SourceIdentifier == "aws_instance.web" {
			assert.Equal(t, "t2.nano", attrs[domain.ComputeInstanceTypeKey])
			assert.Equal(t, "ami-web", attrs[domain.ComputeImageIDKey])
			foundWeb = true
		} else if meta.SourceIdentifier == "aws_instance.app" {
			assert.Equal(t, "t2.micro", attrs[domain.ComputeInstanceTypeKey])
			assert.Equal(t, "ami-app", attrs[domain.ComputeImageIDKey])
			foundApp = true
		}
	}
	assert.True(t, foundWeb, "web instance not found")
	assert.True(t, foundApp, "app instance not found")

	s3Resources, err := tp.provider.ListResources(ctx, domain.KindStorageBucket)
	require.NoError(t, err)
	require.Len(t, s3Resources, 1)
	assert.Equal(t, "aws_s3_bucket.data", s3Resources[0].Metadata().SourceIdentifier)
	assert.Equal(t, "my-data-bucket", s3Resources[0].Attributes()["bucket"])
}

func TestTFHCLProvider_ListResources_WithVarsAndLocals(t *testing.T) {
	tempDir := createTestDir(t)
	defer cleanupTestDir(t, tempDir)
	varFileName := "dev.tfvars"
	varFilePath := createTestHCLFile(t, tempDir, varFileName, `instance_size = "t3.large"`)

	cfg := tfhcl.Config{Directory: tempDir, VarFiles: []string{varFilePath}, Workspace: "dev"}
	tp := setupHCLTestProvider(t, cfg)

	createTestHCLFile(t, tp.dir, "vars.tf", `variable "instance_size" { default = "t2.small" }`)
	createTestHCLFile(t, tp.dir, "main.tf", `
		locals {
		  common_ami = "ami-local"
		}
		resource "aws_instance" "main" {
		  instance_type = var.instance_size
		  ami           = local.common_ami
		}
	`)

	ctx := context.Background()
	resources, err := tp.provider.ListResources(ctx, domain.KindComputeInstance)
	require.NoError(t, err)
	require.Len(t, resources, 1)

	meta := resources[0].Metadata()
	attrs := resources[0].Attributes()
	assert.Equal(t, "aws_instance.main", meta.SourceIdentifier)
	assert.Equal(t, "t3.large", attrs[domain.ComputeInstanceTypeKey])
	assert.Equal(t, "ami-local", attrs[domain.ComputeImageIDKey])
}

func TestTFHCLProvider_ListResources_InitErrors(t *testing.T) {
	mockLogger := testutil.NewMockLogger()
	ctx := context.Background()

	t.Run("Parse Error", func(t *testing.T) {
		dir := createTestDir(t)
		defer cleanupTestDir(t, dir)
		createTestHCLFile(t, dir, "bad.tf", `resource "a" "b" { = }`)
		p, _ := tfhcl.NewProvider(tfhcl.Config{Directory: dir}, mockLogger)
		_, err := p.ListResources(ctx, domain.KindComputeInstance)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "HCL provider initialization failed")
		assert.Contains(t, err.Error(), "HCL parsing failed")
		var diagErr *evaluator.HCLDiagnosticsError
		require.True(t, errors.As(err, &diagErr))
		assert.True(t, evaluator.DiagsHasFatalErrors(diagErr.Diags))
		assert.Contains(t, diagErr.Diags.Error(), "bad.tf")
	})

	t.Run("Context Build Error (Bad Local Ref)", func(t *testing.T) {
		dir := createTestDir(t)
		defer cleanupTestDir(t, dir)
		createTestHCLFile(t, dir, "main.tf", `locals { bad = var.nope }`)
		p, _ := tfhcl.NewProvider(tfhcl.Config{Directory: dir}, mockLogger)
		_, err := p.ListResources(ctx, domain.KindComputeInstance)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "HCL provider initialization failed")
		assert.Contains(t, err.Error(), "fatal errors during HCL initialization")
		var diagErr *evaluator.HCLDiagnosticsError
		require.True(t, errors.As(err, &diagErr))
		assert.True(t, evaluator.DiagsHasFatalErrors(diagErr.Diags))
		assert.Contains(t, diagErr.Diags.Error(), "var.nope")
	})
}

func TestTFHCLProvider_ListResources_EvaluationError(t *testing.T) {
	cfg := tfhcl.Config{}
	tp := setupHCLTestProvider(t, cfg)
	defer tp.cleanup()

	createTestHCLFile(t, tp.dir, "main.tf", `
		resource "aws_instance" "good" {
		  instance_type = "t2.micro"
		}
		resource "aws_instance" "bad" {
		  instance_type = var.nonexistent
		}
	`)

	ctx := context.Background()
	resources, err := tp.provider.ListResources(ctx, domain.KindComputeInstance)
	require.NoError(t, err)
	require.Len(t, resources, 1)
	assert.Equal(t, "aws_instance.good", resources[0].Metadata().SourceIdentifier)
}

func TestTFHCLProvider_GetResource(t *testing.T) {
	tempDir := createTestDir(t)
	defer cleanupTestDir(t, tempDir)
	varFileName := "vars.tfvars"
	varFilePath := createTestHCLFile(t, tempDir, varFileName, `inst_type = "m5.large"`)
	cfg := tfhcl.Config{Directory: tempDir, VarFiles: []string{varFilePath}}
	tp := setupHCLTestProvider(t, cfg)

	createTestHCLFile(t, tp.dir, "main.tf", `
		resource "aws_instance" "web" {
		  instance_type = var.inst_type
		  ami           = "ami-web"
		}
		resource "aws_s3_bucket" "data" {
		  bucket = "my-bucket"
		}
	`)

	ctx := context.Background()

	t.Run("Found", func(t *testing.T) {
		res, err := tp.provider.GetResource(ctx, domain.KindComputeInstance, "aws_instance.web")
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Equal(t, "aws_instance.web", res.Metadata().SourceIdentifier)
		attrs := res.Attributes()
		assert.Equal(t, "m5.large", attrs[domain.ComputeInstanceTypeKey])
		assert.Equal(t, "ami-web", attrs[domain.ComputeImageIDKey])
	})

	t.Run("Not Found (Wrong ID)", func(t *testing.T) {
		_, err := tp.provider.GetResource(ctx, domain.KindComputeInstance, "aws_instance.db")
		require.Error(t, err)
		var nfErr *errors.AppError
		require.True(t, errors.As(err, &nfErr))
		assert.Equal(t, errors.CodeResourceNotFound, nfErr.Code)
		assert.Contains(t, err.Error(), "aws_instance.db")
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("Not Found (Wrong Kind)", func(t *testing.T) {
		_, err := tp.provider.GetResource(ctx, domain.KindStorageBucket, "aws_instance.web")
		require.Error(t, err)
		var nfErr *errors.AppError
		require.True(t, stderrors.As(err, &nfErr))
		assert.Equal(t, errors.CodeResourceNotFound, nfErr.Code)
		assert.Contains(t, err.Error(), "aws_instance.web")
		assert.Contains(t, err.Error(), "expected 'StorageBucket'")
	})

	t.Run("Evaluation Error During Get", func(t *testing.T) {
		createTestHCLFile(t, tp.dir, "bad_get.tf", `
            resource "aws_instance" "error_instance" {
                instance_type = var.no_such_var
            }
        `)

		// Recreate provider to force re-initialization with the new file
		mockLogger := testutil.NewMockLogger()
		p, _ := tfhcl.NewProvider(cfg, mockLogger)

		_, err := p.GetResource(ctx, domain.KindComputeInstance, "aws_instance.error_instance")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Errors evaluating target HCL block")
		var evalErr *evaluator.ResourceEvaluationError
		require.True(t, errors.As(err, &evalErr), "Error should wrap ResourceEvaluationError")
		assert.True(t, evaluator.DiagsHasFatalErrors(evalErr.Diags))
		assert.Contains(t, evalErr.Diags.Error(), "var.no_such_var")
	})

	t.Run("Duplicate Resource Definition Error", func(t *testing.T) {
		createTestHCLFile(t, tp.dir, "dup.tf", `
			resource "aws_instance" "web" { # Duplicate address from main.tf
				ami = "ami-dup"
			}
		`)
		// Recreate provider
		mockLogger := testutil.NewMockLogger()
		p, _ := tfhcl.NewProvider(cfg, mockLogger)

		_, err := p.GetResource(ctx, domain.KindComputeInstance, "aws_instance.web")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Fatal error finding specific HCL block")
		var diagErr *evaluator.HCLDiagnosticsError
		require.True(t, errors.As(err, &diagErr))
		assert.True(t, evaluator.DiagsHasFatalErrors(diagErr.Diags))
		assert.Contains(t, diagErr.Diags.Error(), "Duplicate resource definition")

	})
}
