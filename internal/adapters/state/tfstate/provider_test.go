package tfstate_test

import (
	"context"
	"path/filepath"
	"testing"

	portsmocks "github.com/olusolaa/infra-drift-detector/internal/core/ports/mocks"
	"github.com/stretchr/testify/mock"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfstate"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

func TestNewProvider(t *testing.T) {
	mockLogger := portsmocks.NewLogger(t)
	mockLogger.On("WithFields", mock.Anything).Maybe().Return(mockLogger)
	mockLogger.On("Debugf", mock.Anything, mock.AnythingOfType("string")).Maybe().Return()

	t.Run("Valid Config", func(t *testing.T) {
		cfg := tfstate.Config{FilePath: filepath.Join("testdata", "sample_ec2.tfstate")}
		p, err := tfstate.NewProvider(cfg, mockLogger)
		require.NoError(t, err)
		require.NotNil(t, p)
		assert.Equal(t, tfstate.ProviderTypeTFState, p.Type())
	})

	t.Run("Empty File Path", func(t *testing.T) {
		cfg := tfstate.Config{FilePath: ""}
		p, err := tfstate.NewProvider(cfg, mockLogger)
		require.Error(t, err)
		assert.Nil(t, p)
		var appErr *errors.AppError
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, errors.CodeConfigValidation, appErr.Code)
	})

	t.Run("Nil Logger", func(t *testing.T) {

		cfg := tfstate.Config{FilePath: filepath.Join("testdata", "sample_ec2.tfstate")}
		assert.Panics(t, func() {
			_, _ = tfstate.NewProvider(cfg, nil)
		}, "Expected panic with nil logger due to WithFields call")

	})
}

func TestProvider_ListResources(t *testing.T) {
	mockLogger := portsmocks.NewLogger(t)
	mockLogger.On("WithFields", mock.Anything).Maybe().Return(mockLogger)
	mockLogger.On("Debugf", mock.Anything, mock.AnythingOfType("string"), mock.Anything).Maybe().Return()
	mockLogger.On("Warnf", mock.Anything, mock.Anything).Maybe().Return()
	mockLogger.On("Debugf", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe().Return()
	mockLogger.On("Errorf", mock.Anything, mock.Anything, mock.Anything).Maybe().Return()
	ctx := context.Background()

	t.Run("Success EC2", func(t *testing.T) {
		cfg := tfstate.Config{FilePath: filepath.Join("testdata", "sample_ec2_raw.tfstate")}
		p, _ := tfstate.NewProvider(cfg, mockLogger)
		resources, err := p.ListResources(ctx, domain.KindComputeInstance)
		require.NoError(t, err)
		require.Len(t, resources, 2)
		assert.Equal(t, "aws_instance.web", resources[0].Metadata().SourceIdentifier)
		assert.Equal(t, "module.ec2_instances.aws_instance.web", resources[1].Metadata().SourceIdentifier)
	})

	t.Run("Success S3", func(t *testing.T) {
		cfg := tfstate.Config{FilePath: filepath.Join("testdata", "sample_s3.tfstate")}
		p, _ := tfstate.NewProvider(cfg, mockLogger)
		resources, err := p.ListResources(ctx, domain.KindStorageBucket)
		require.NoError(t, err)
		require.Len(t, resources, 1)
		assert.Equal(t, "aws_s3_bucket.my_bucket", resources[0].Metadata().SourceIdentifier)
		assert.Equal(t, "my-test-bucket-123", resources[0].Attributes()[domain.KeyID])
	})

	t.Run("Kind Not Found", func(t *testing.T) {
		cfg := tfstate.Config{FilePath: filepath.Join("testdata", "sample_ec2_raw.tfstate")}
		p, _ := tfstate.NewProvider(cfg, mockLogger)
		resources, err := p.ListResources(ctx, domain.KindStorageBucket)
		require.NoError(t, err)
		assert.Empty(t, resources)
	})

	t.Run("File Not Found", func(t *testing.T) {
		cfg := tfstate.Config{FilePath: filepath.Join("testdata", "nonexistent.tfstate")}
		p, _ := tfstate.NewProvider(cfg, mockLogger)
		_, err := p.ListResources(ctx, domain.KindComputeInstance)
		require.Error(t, err)
		var appErr *errors.AppError
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, errors.CodeStateReadError, appErr.Code)
	})

	t.Run("Invalid JSON", func(t *testing.T) {
		cfg := tfstate.Config{FilePath: filepath.Join("testdata", "invalid_json.tfstate")}
		p, _ := tfstate.NewProvider(cfg, mockLogger)
		_, err := p.ListResources(ctx, domain.KindComputeInstance)
		require.Error(t, err)
		var appErr *errors.AppError
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, errors.CodeStateParseError, appErr.Code)
	})

	t.Run("Context Cancelled During List", func(t *testing.T) {
		cfg := tfstate.Config{FilePath: filepath.Join("testdata", "sample_ec2.tfstate")}
		p, _ := tfstate.NewProvider(cfg, mockLogger)
		cancelCtx, cancel := context.WithCancel(ctx)
		cancel() // Cancel immediately
		_, err := p.ListResources(cancelCtx, domain.KindComputeInstance)
		require.ErrorIs(t, err, context.Canceled)
	})

}

func TestProvider_GetResource(t *testing.T) {
	mockLogger := portsmocks.NewLogger(t)
	mockLogger.On("WithFields", mock.Anything).Maybe().Return(mockLogger)
	mockLogger.On("Debugf", mock.Anything, mock.Anything).Maybe().Return()
	ctx := context.Background()
	cfg := tfstate.Config{FilePath: filepath.Join("testdata", "nested_modules_raw.tfstate")}
	p, _ := tfstate.NewProvider(cfg, mockLogger)

	t.Run("Found in Root", func(t *testing.T) {
		identifier := "aws_instance.root_ec2"
		res, err := p.GetResource(ctx, domain.KindComputeInstance, identifier)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Equal(t, identifier, res.Metadata().SourceIdentifier)
		assert.Equal(t, "i-root1234567890", res.Metadata().ProviderAssignedID)
		assert.Equal(t, "t2.micro", res.Attributes()[domain.ComputeInstanceTypeKey])
	})

	t.Run("Found in Child Module", func(t *testing.T) {
		identifier := "module.nested.aws_instance.child_ec2"
		res, err := p.GetResource(ctx, domain.KindComputeInstance, identifier)
		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Equal(t, identifier, res.Metadata().SourceIdentifier)
		assert.Equal(t, "i-child1234567890", res.Metadata().ProviderAssignedID)
	})

	t.Run("Not Found - Wrong Identifier", func(t *testing.T) {
		_, err := p.GetResource(ctx, domain.KindComputeInstance, "aws_instance.nonexistent")
		require.Error(t, err)
		var appErr *errors.AppError
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, errors.CodeResourceNotFound, appErr.Code)
	})

	t.Run("Not Found - Wrong Kind", func(t *testing.T) {
		_, err := p.GetResource(ctx, domain.KindStorageBucket, "aws_instance.root_ec2")
		require.Error(t, err)
		var appErr *errors.AppError
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, errors.CodeResourceNotFound, appErr.Code)
		assert.Contains(t, err.Error(), "found, but it has kind 'ComputeInstance', expected 'StorageBucket'")
	})

	t.Run("File Not Found", func(t *testing.T) {
		cfgBadFile := tfstate.Config{FilePath: filepath.Join("testdata", "nonexistent.tfstate")}
		pBadFile, _ := tfstate.NewProvider(cfgBadFile, mockLogger)
		_, err := pBadFile.GetResource(ctx, domain.KindComputeInstance, "any_id")
		require.Error(t, err)
		var appErr *errors.AppError
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, errors.CodeStateReadError, appErr.Code)
	})

	t.Run("Context Cancelled During Get", func(t *testing.T) {
		cancelCtx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := p.GetResource(cancelCtx, domain.KindComputeInstance, "aws_instance.root_ec2")
		require.ErrorIs(t, err, context.Canceled)
	})
}
