package tfhcl_test

import (
	"context"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl"
	"github.com/olusolaa/infra-drift-detector/test/testutil"
	"os"
	"path/filepath"
	"testing"

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
	dir, err := os.MkdirTemp("", "tfhcl-provider-test-")
	require.NoError(t, err, "Failed to create temp dir for testing")
	return dir
}

func cleanupTestDir(t *testing.T, dir string) {
	t.Helper()
	os.RemoveAll(dir)
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

	absVarFiles := make([]string, len(cfg.VarFiles))
	for i, vf := range cfg.VarFiles {
		if vf != "" && !filepath.IsAbs(vf) {
			absVarFiles[i] = filepath.Join(cfg.Directory, filepath.Base(vf))
		} else if vf != "" {
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
		require.NotNil(t, p)
		_, err := p.ListResources(ctx, domain.KindComputeInstance)
		require.Error(t, err)

		// Check that we get an AppError with the right code
		var appErr *errors.AppError
		require.True(t, errors.As(err, &appErr), "Expected error to be of type *errors.AppError, got: %T, value: %v", err, err)
		assert.Equal(t, errors.CodeStateReadError, appErr.Code)

		// Verify error message contains expected details
		errString := err.Error()
		assert.Contains(t, errString, "HCL provider initialization failed")
		assert.Contains(t, errString, "bad.tf")
		assert.Contains(t, errString, "Argument or block definition required")
	})

	t.Run("Context Build Error (Bad Local Ref)", func(t *testing.T) {
		dir := createTestDir(t)
		defer cleanupTestDir(t, dir)
		createTestHCLFile(t, dir, "main.tf", `locals { bad = var.nope }`)
		p, _ := tfhcl.NewProvider(tfhcl.Config{Directory: dir}, mockLogger)
		require.NotNil(t, p)
		_, err := p.ListResources(ctx, domain.KindComputeInstance)
		require.Error(t, err)

		// Check that we get an AppError with the right code
		var appErr *errors.AppError
		require.True(t, errors.As(err, &appErr), "Expected error to be of type *errors.AppError, got: %T, value: %v", err, err)
		assert.Equal(t, errors.CodeStateReadError, appErr.Code)

		// Verify error message contains expected details
		errString := err.Error()
		assert.Contains(t, errString, "HCL provider initialization failed")
		assert.Contains(t, errString, "var.nope")
		assert.Contains(t, errString, "does not have an attribute")
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
		var appErr *errors.AppError
		if !errors.As(err, &appErr) || appErr == nil {
			t.Fatalf("Expected error to be of type *errors.AppError, got: %T, value: %v", err, err)
		}
		assert.Equal(t, errors.CodeResourceNotFound, appErr.Code)
		assert.Contains(t, err.Error(), "aws_instance.db")
	})

	t.Run("Not Found (Wrong Kind)", func(t *testing.T) {
		_, err := tp.provider.GetResource(ctx, domain.KindStorageBucket, "aws_instance.web")
		require.Error(t, err)
		var appErr *errors.AppError
		if !errors.As(err, &appErr) || appErr == nil {
			t.Fatalf("Expected error to be of type *errors.AppError, got: %T, value: %v", err, err)
		}
		assert.Equal(t, errors.CodeResourceNotFound, appErr.Code)
		assert.Contains(t, err.Error(), "aws_instance.web")
		assert.Contains(t, err.Error(), "expected 'StorageBucket'")
	})

	t.Run("Evaluation Error During Get", func(t *testing.T) {
		createTestHCLFile(t, tp.dir, "bad_get.tf", `
            resource "aws_instance" "error_instance" {
                instance_type = var.no_such_var
            }
        `)
		mockLogger := testutil.NewMockLogger()
		p, _ := tfhcl.NewProvider(cfg, mockLogger)

		_, err := p.GetResource(ctx, domain.KindComputeInstance, "aws_instance.error_instance")
		require.Error(t, err)
		var appErr *errors.AppError
		if !errors.As(err, &appErr) || appErr == nil {
			t.Fatalf("Expected error to be of type *errors.AppError, got: %T, value: %v", err, err)
		}
		assert.Equal(t, errors.CodeStateParseError, appErr.Code)
		assert.Contains(t, err.Error(), "var.no_such_var")
	})

	t.Run("Duplicate Resource Definition Error", func(t *testing.T) {
		createTestHCLFile(t, tp.dir, "dup.tf", `
			resource "aws_instance" "web" {
				ami = "ami-dup"
			}
		`)
		mockLogger := testutil.NewMockLogger()
		p, _ := tfhcl.NewProvider(cfg, mockLogger)

		_, err := p.GetResource(ctx, domain.KindComputeInstance, "aws_instance.web")
		require.Error(t, err)
		var appErr *errors.AppError
		if !errors.As(err, &appErr) || appErr == nil {
			t.Fatalf("Expected error to be of type *errors.AppError, got: %T, value: %v", err, err)
		}
		assert.Equal(t, errors.CodeStateParseError, appErr.Code) // Wrap code indicates init/parse phase failure
		assert.Contains(t, err.Error(), "Duplicate resource definition")
	})
}
