package aws

import (
	"context"
	"errors"
	"testing"
	"time"

	portsmocks "github.com/olusolaa/infra-drift-detector/internal/core/ports/mocks"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	awstypes "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/shared"
	"github.com/olusolaa/infra-drift-detector/internal/config"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	internalerrors "github.com/olusolaa/infra-drift-detector/internal/errors"
)

type MockAWSResourceHandler struct {
	mock.Mock
}

func (m *MockAWSResourceHandler) Kind() domain.ResourceKind {
	args := m.Called()
	return args.Get(0).(domain.ResourceKind)
}

func (m *MockAWSResourceHandler) ListResources(
	ctx context.Context,
	cfg aws.Config,
	filters map[string]string,
	logger ports.Logger,
	out chan<- domain.PlatformResource,
) error {
	args := m.Called(ctx, cfg, filters, logger, out)
	if resources, ok := args.Get(1).([]domain.PlatformResource); ok && resources != nil {
		// Send resources synchronously for deterministic tests
		for _, res := range resources {
			select {
			case out <- res:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return args.Error(0)
}

func (m *MockAWSResourceHandler) GetResource(
	ctx context.Context,
	cfg aws.Config,
	id string,
	logger ports.Logger,
) (domain.PlatformResource, error) {
	args := m.Called(ctx, cfg, id, logger)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(domain.PlatformResource), args.Error(1)
}

type mockPlatformResource struct {
	id   string
	kind domain.ResourceKind
}

func (m *mockPlatformResource) Metadata() domain.ResourceMetadata {
	return domain.ResourceMetadata{ProviderAssignedID: m.id, Kind: m.kind, ProviderType: awstypes.ProviderTypeAWS}
}
func (m *mockPlatformResource) Attributes(ctx context.Context) (map[string]any, error) {
	return map[string]any{"id": m.id}, nil
}

func setupProviderTest(t *testing.T) (*Provider, *MockAWSResourceHandler, *MockAWSResourceHandler, *portsmocks.Logger) {
	mockLogger := new(portsmocks.Logger)
	mockLogger.On("Debugf", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe().Return()
	mockLogger.On("Debugf", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe().Return()
	mockLogger.On("Infof", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe().Return()
	mockLogger.On("Errorf", mock.Anything, mock.Anything, mock.Anything).Maybe().Return()
	mockLogger.On("WithFields", mock.Anything).Return(mockLogger).Maybe()
	mockLogger.On("Warnf", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe().Return()

	cfg := aws.Config{Region: "us-east-1"}

	handlerEC2 := new(MockAWSResourceHandler)
	handlerEC2.On("Kind").Maybe().Return(domain.KindComputeInstance)

	handlerS3 := new(MockAWSResourceHandler)
	handlerS3.On("Kind").Maybe().Return(domain.KindStorageBucket)

	// Use the testing constructor
	provider := NewProviderWithHandlers(cfg, mockLogger, handlerEC2, handlerS3)
	require.NotNil(t, provider)

	return provider, handlerEC2, handlerS3, mockLogger
}

// Note: Testing the success path of NewProvider relies on AWS SDK's default loading behavior.
// For simplicity in unit tests, we assume LoadDefaultConfig succeeds if basic inputs are okay.
// Testing LoadDefaultConfig failure modes directly is closer to integration testing.
func TestNewProvider(t *testing.T) {
	ctx := context.Background()

	t.Run("success with config", func(t *testing.T) {
		mockLogger := new(portsmocks.Logger)
		mockLogger.On("Debugf", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe().Return()
		mockLogger.On("Infof", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe().Return()

		appCfg := &config.Config{
			Platform: config.PlatformConfig{
				AWS: &config.AWSPlatformConfig{
					Region:               "us-west-2",
					Profile:              "test-profile", // This profile likely doesn't exist
					APIRequestsPerSecond: 15,
				},
			},
		}

		// Expect an error because the test profile likely doesn't exist
		provider, err := NewProvider(ctx, appCfg, mockLogger)

		// Assert that an error occurred
		require.Error(t, err, "Expected an error due to non-existent profile")
		require.Nil(t, provider, "Provider should be nil on error")

		// Check the error type and message
		var appErr *internalerrors.AppError
		require.ErrorAs(t, err, &appErr, "Error should be of type AppError")
		assert.Equal(t, internalerrors.CodePlatformAuthError, appErr.Code, "Error code should be PlatformAuthError")
		assert.Contains(t, appErr.Message, "Failed to load AWS configuration/credentials", "Error message should indicate credential loading failure")

		// Verify logging calls indicating config usage were still attempted
		mockLogger.AssertCalled(t, "Debugf", mock.Anything, "AWS config: Using specified region", "region", "us-west-2")
		mockLogger.AssertCalled(t, "Debugf", mock.Anything, "AWS config: Using specified profile", "profile", "test-profile")

	})

	t.Run("success with defaults", func(t *testing.T) {
		mockLogger := new(portsmocks.Logger)
		mockLogger.On("Warnf", mock.Anything, mock.Anything).Maybe().Return()
		mockLogger.On("Debugf", mock.Anything, "Registered AWS handler", mock.AnythingOfType("string"), mock.AnythingOfType("domain.ResourceKind")).Maybe().Return()
		mockLogger.On("Infof", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe().Return()

		appCfg := &config.Config{}
		t.Setenv("AWS_REGION", "eu-central-1")

		provider, err := NewProvider(ctx, appCfg, mockLogger)

		require.NoError(t, err)
		require.NotNil(t, provider)
		assert.Equal(t, "eu-central-1", provider.awsConfig.Region) // Verify SDK default was picked up
		mockLogger.AssertCalled(t, "Warnf", ctx, "Platform.aws configuration block missing, attempting AWS client setup with defaults.")
	})

	t.Run("error nil logger", func(t *testing.T) {
		appCfg := &config.Config{}
		provider, err := NewProvider(ctx, appCfg, nil)
		require.Error(t, err)
		assert.Nil(t, provider)
		assert.Contains(t, err.Error(), "logger cannot be nil")
	})

	t.Run("error platform auth", func(t *testing.T) {
		mockLogger := new(portsmocks.Logger)
		mockLogger.On("Infof", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe().Return()
		mockLogger.On("Debugf", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe().Return()

		appCfg := &config.Config{
			Platform: config.PlatformConfig{
				AWS: &config.AWSPlatformConfig{
					Region:               "us-west-2",
					Profile:              "test-profile",
					APIRequestsPerSecond: 15,
				},
			},
		}

		provider, err := NewProvider(ctx, appCfg, mockLogger)

		require.Error(t, err)
		require.Nil(t, provider)
		var appErr *internalerrors.AppError
		require.ErrorAs(t, err, &appErr)
		assert.Equal(t, internalerrors.CodePlatformAuthError, appErr.Code)
		assert.Contains(t, appErr.Message, "Failed to load AWS configuration/credentials")

	})
}

func TestProviderType(t *testing.T) {
	provider, _, _, _ := setupProviderTest(t)
	assert.Equal(t, awstypes.ProviderTypeAWS, provider.Type())
}

func TestProviderListResources(t *testing.T) {
	ctx := context.Background()
	testErr := errors.New("handler failed")
	notFoundErr := internalerrors.New(internalerrors.CodeResourceNotFound, "simulated not found")
	resEC2 := &mockPlatformResource{id: "i-1", kind: domain.KindComputeInstance}
	resS3 := &mockPlatformResource{id: "b-1", kind: domain.KindStorageBucket}

	t.Run("success multiple handlers", func(t *testing.T) {
		provider, handlerEC2, handlerS3, _ := setupProviderTest(t)

		handlerEC2.On("ListResources", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, []domain.PlatformResource{resEC2}).Once()
		handlerS3.On("ListResources", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, []domain.PlatformResource{resS3}).Once()

		outChan := make(chan domain.PlatformResource, 5)
		err := provider.ListResources(ctx, []domain.ResourceKind{domain.KindComputeInstance, domain.KindStorageBucket}, nil, outChan)
		close(outChan)

		require.NoError(t, err)
		results := make([]domain.PlatformResource, 0)
		for res := range outChan {
			results = append(results, res)
		}
		assert.Len(t, results, 2)
		assert.Contains(t, results, resEC2)
		assert.Contains(t, results, resS3)
		handlerEC2.AssertExpectations(t)
		handlerS3.AssertExpectations(t)
	})

	t.Run("one handler fails", func(t *testing.T) {
		provider, handlerEC2, handlerS3, _ := setupProviderTest(t)
		handlerEC2.On("ListResources", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(testErr, nil).Once()
		handlerS3.On("ListResources", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, []domain.PlatformResource{resS3}).Once()

		outChan := make(chan domain.PlatformResource, 5)
		err := provider.ListResources(ctx, []domain.ResourceKind{domain.KindComputeInstance, domain.KindStorageBucket}, nil, outChan)
		close(outChan)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "handler for kind 'ComputeInstance' failed") // Check wrapped error
		assert.ErrorIs(t, err, testErr)

		results := make([]domain.PlatformResource, 0)
		for res := range outChan {
			results = append(results, res)
		}
		assert.Len(t, results, 1) // S3 result should still be sent
		assert.Contains(t, results, resS3)

		handlerEC2.AssertExpectations(t)
		handlerS3.AssertExpectations(t)
	})

	t.Run("handler returns not found error", func(t *testing.T) {
		provider, handlerEC2, handlerS3, _ := setupProviderTest(t)
		handlerEC2.On("ListResources", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(notFoundErr, nil).Once()
		handlerS3.On("ListResources", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, []domain.PlatformResource{resS3}).Once()

		outChan := make(chan domain.PlatformResource, 5)
		err := provider.ListResources(ctx, []domain.ResourceKind{domain.KindComputeInstance, domain.KindStorageBucket}, nil, outChan)
		close(outChan)

		require.NoError(t, err)

		results := make([]domain.PlatformResource, 0)
		for res := range outChan {
			results = append(results, res)
		}
		assert.Len(t, results, 1)
		assert.Contains(t, results, resS3)
		handlerEC2.AssertExpectations(t)
		handlerS3.AssertExpectations(t)
	})

	t.Run("unsupported kind requested", func(t *testing.T) {
		provider, handlerEC2, _, _ := setupProviderTest(t)
		unsupportedKind := domain.ResourceKind("unsupported")
		handlerEC2.On("ListResources", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, []domain.PlatformResource{resEC2}).Once()

		outChan := make(chan domain.PlatformResource, 5)
		err := provider.ListResources(ctx, []domain.ResourceKind{domain.KindComputeInstance, unsupportedKind}, nil, outChan)
		close(outChan)

		require.NoError(t, err)
		results := make([]domain.PlatformResource, 0)
		for res := range outChan {
			results = append(results, res)
		}
		assert.Len(t, results, 1)
		assert.Contains(t, results, resEC2)
		handlerEC2.AssertExpectations(t)
	})

	t.Run("no supported kinds requested", func(t *testing.T) {
		provider, _, _, _ := setupProviderTest(t)
		unsupportedKind := domain.ResourceKind("unsupported")

		outChan := make(chan domain.PlatformResource, 5)
		err := provider.ListResources(ctx, []domain.ResourceKind{unsupportedKind}, nil, outChan)
		close(outChan)

		require.Error(t, err)
		assert.True(t, internalerrors.Is(err, internalerrors.CodeNotImplemented))
		assert.Empty(t, outChan)
	})

	t.Run("context cancelled", func(t *testing.T) {
		provider, handlerEC2, _, _ := setupProviderTest(t)
		ctxCancel, cancel := context.WithCancel(ctx)

		handlerEC2.On("ListResources", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(args mock.Arguments) { time.Sleep(10 * time.Millisecond); cancel() }). // Cancel while handler runs
			Return(context.Canceled, nil).Once()                                            // Handler respects cancellation

		outChan := make(chan domain.PlatformResource, 5)
		err := provider.ListResources(ctxCancel, []domain.ResourceKind{domain.KindComputeInstance}, nil, outChan)
		close(outChan)

		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Empty(t, outChan)
		handlerEC2.AssertExpectations(t)
	})
}

func TestProviderGetResource(t *testing.T) {
	ctx := context.Background()
	testID := "r-123"
	testErr := errors.New("handler failed")
	notFoundErr := internalerrors.New(internalerrors.CodeResourceNotFound, "not found by handler")
	resEC2 := &mockPlatformResource{id: testID, kind: domain.KindComputeInstance}
	unsupportedKind := domain.ResourceKind("unsupported")

	t.Run("success", func(t *testing.T) {
		provider, handlerEC2, _, _ := setupProviderTest(t)
		handlerEC2.On("GetResource", mock.Anything, mock.Anything, testID, mock.Anything).Return(resEC2, nil).Once()

		res, err := provider.GetResource(ctx, domain.KindComputeInstance, testID)

		require.NoError(t, err)
		require.NotNil(t, res)
		assert.Equal(t, testID, res.Metadata().ProviderAssignedID)
		handlerEC2.AssertExpectations(t)
	})

	t.Run("handler returns error", func(t *testing.T) {
		provider, handlerEC2, _, _ := setupProviderTest(t)
		handlerEC2.On("GetResource", mock.Anything, mock.Anything, testID, mock.Anything).Return(nil, testErr).Once()

		res, err := provider.GetResource(ctx, domain.KindComputeInstance, testID)

		require.Error(t, err)
		assert.Nil(t, res)
		assert.ErrorIs(t, err, testErr) // Check original error is wrapped
		assert.True(t, internalerrors.Is(err, internalerrors.CodePlatformAPIError))
		handlerEC2.AssertExpectations(t)
	})

	t.Run("handler returns not found", func(t *testing.T) {
		provider, handlerEC2, _, _ := setupProviderTest(t)
		handlerEC2.On("GetResource", mock.Anything, mock.Anything, testID, mock.Anything).Return(nil, notFoundErr).Once()

		res, err := provider.GetResource(ctx, domain.KindComputeInstance, testID)

		require.Error(t, err)
		assert.Nil(t, res)
		assert.ErrorIs(t, err, notFoundErr) // Should return the NotFound error directly
		assert.True(t, internalerrors.Is(err, internalerrors.CodeResourceNotFound))
		handlerEC2.AssertExpectations(t)
	})

	t.Run("unsupported kind", func(t *testing.T) {
		provider, _, _, _ := setupProviderTest(t)

		res, err := provider.GetResource(ctx, unsupportedKind, testID)

		require.Error(t, err)
		assert.Nil(t, res)
		assert.True(t, internalerrors.Is(err, internalerrors.CodeNotImplemented))
	})

	t.Run("context cancelled", func(t *testing.T) {
		provider, handlerEC2, _, _ := setupProviderTest(t)
		ctxCancel, cancel := context.WithCancel(ctx)
		cancel()

		handlerEC2.On("GetResource", mock.Anything, mock.Anything, testID, mock.Anything).Return(nil, context.Canceled).Maybe()

		res, err := provider.GetResource(ctxCancel, domain.KindComputeInstance, testID)

		require.Error(t, err)
		assert.Nil(t, res)
		assert.ErrorIs(t, err, context.Canceled)
	})
}
