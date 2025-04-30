package aws_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	awsadapter "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws"
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

type MockLogger struct {
	mock.Mock
}

func (m *MockLogger) Debugf(ctx context.Context, format string, args ...any) {
	m.Called(ctx, format, args)
}

func (m *MockLogger) Infof(ctx context.Context, format string, args ...any) {
	m.Called(ctx, format, args)
}

func (m *MockLogger) Warnf(ctx context.Context, format string, args ...any) {
	m.Called(ctx, format, args)
}

func (m *MockLogger) Errorf(ctx context.Context, err error, format string, args ...any) {
	m.Called(ctx, err, format, args)
}

func (m *MockLogger) WithFields(fields map[string]any) ports.Logger {
	args := m.Called(fields)
	return args.Get(0).(ports.Logger)
}

func TestNewProvider(t *testing.T) {
	t.Run("successful initialization", func(t *testing.T) {
		mockLogger := new(MockLogger)
		mockLogger.On("WithFields", mock.Anything).Return(mockLogger)
		mockLogger.On("Debugf", mock.Anything, mock.Anything, mock.Anything).Return()
		mockLogger.On("Infof", mock.Anything, mock.Anything, mock.Anything).Return()

		// Create a minimal config for testing
		appCfg := &config.Config{
			Platform: config.PlatformConfig{
				AWS: &config.AWSPlatformConfig{
					Region:  "us-west-2",
					Profile: "default",
				},
			},
		}

		provider, err := awsadapter.NewProvider(context.Background(), appCfg, mockLogger)

		require.NoError(t, err)
		require.NotNil(t, provider)
	})

	t.Run("nil logger causes error", func(t *testing.T) {
		appCfg := &config.Config{
			Platform: config.PlatformConfig{
				AWS: &config.AWSPlatformConfig{
					Region:  "us-west-2",
					Profile: "default",
				},
			},
		}
		provider, err := awsadapter.NewProvider(context.Background(), appCfg, nil)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "logger cannot be nil")
		assert.Nil(t, provider)
	})
}

func TestListResources(t *testing.T) {
	t.Run("successful listing with multiple handlers", func(t *testing.T) {
		mockLogger := new(MockLogger)
		mockLogger.On("WithFields", mock.Anything).Return(mockLogger)
		mockLogger.On("Debugf", mock.Anything, mock.Anything, mock.Anything).Return()

		// Create mocked provider with test handlers
		provider, handlerEC2, handlerEBS := setupMockedProvider(t, mockLogger)

		// Mock EC2 handler to send resources to the channel
		ec2Resource := &mockResource{id: "i-1234", kind: domain.KindComputeInstance}
		handlerEC2.On("ListResources", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(args mock.Arguments) {
				out := args.Get(4).(chan<- domain.PlatformResource)
				out <- ec2Resource
			}).
			Return(nil)

		// Mock EBS handler to send resources to the channel
		ebsResource := &mockResource{id: "vol-5678", kind: domain.KindStorageBucket}
		handlerEBS.On("ListResources", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(args mock.Arguments) {
				out := args.Get(4).(chan<- domain.PlatformResource)
				out <- ebsResource
			}).
			Return(nil)

		// Create channel to receive resources
		resourceChan := make(chan domain.PlatformResource, 10)

		// Call ListResources with both handler kinds
		err := provider.ListResources(
			context.Background(),
			[]domain.ResourceKind{domain.KindComputeInstance, domain.KindStorageBucket},
			map[string]string{},
			resourceChan,
		)

		require.NoError(t, err)

		// Collect resources from the channel
		var resources []domain.PlatformResource
		timeout := time.After(100 * time.Millisecond)

		collecting := true
		for collecting {
			select {
			case resource, ok := <-resourceChan:
				if !ok {
					collecting = false
					break
				}
				resources = append(resources, resource)
			case <-timeout:
				collecting = false
			}
		}

		// Verify we got both resources
		assert.Equal(t, 2, len(resources))

		// Verify handlers were called
		handlerEC2.AssertExpectations(t)
		handlerEBS.AssertExpectations(t)
	})

	t.Run("cancellation propagates to handlers", func(t *testing.T) {
		mockLogger := new(MockLogger)
		mockLogger.On("WithFields", mock.Anything).Return(mockLogger)
		mockLogger.On("Debugf", mock.Anything, mock.Anything, mock.Anything).Return()
		mockLogger.On("Warnf", mock.Anything, mock.Anything, mock.Anything).Return()
		mockLogger.On("Errorf", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()

		// Create mocked provider with test handlers
		provider, handlerEC2, _ := setupMockedProvider(t, mockLogger)

		// Create a context that can be canceled
		ctx, cancel := context.WithCancel(context.Background())

		// Set up handler to block until canceled
		var wg sync.WaitGroup
		wg.Add(1)

		handlerEC2.On("ListResources", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(args mock.Arguments) {
				handlerCtx := args.Get(0).(context.Context)

				// Signal that we're inside the handler
				go func() {
					time.Sleep(10 * time.Millisecond)
					cancel()
				}()

				// Block until context is canceled
				<-handlerCtx.Done()
				wg.Done()
			}).
			Return(context.Canceled)

		// Create channel to receive resources
		resourceChan := make(chan domain.PlatformResource, 10)

		// Call ListResources with the cancellable context
		err := provider.ListResources(
			ctx,
			[]domain.ResourceKind{domain.KindComputeInstance},
			map[string]string{},
			resourceChan,
		)

		// Wait for the handler to finish
		wg.Wait()

		// Context cancellation should be propagated
		assert.Equal(t, context.Canceled, err)

		handlerEC2.AssertExpectations(t)
	})

	t.Run("handler error is propagated", func(t *testing.T) {
		mockLogger := new(MockLogger)
		mockLogger.On("WithFields", mock.Anything).Return(mockLogger)
		mockLogger.On("Debugf", mock.Anything, mock.Anything, mock.Anything).Return()
		mockLogger.On("Errorf", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()

		// Create mocked provider with test handlers
		provider, handlerEC2, _ := setupMockedProvider(t, mockLogger)

		// Set up handler to return an error
		testError := internalerrors.New(internalerrors.CodeInternal, "test error")
		handlerEC2.On("ListResources", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Return(testError)

		// Create channel to receive resources
		resourceChan := make(chan domain.PlatformResource, 10)

		// Call ListResources
		err := provider.ListResources(
			context.Background(),
			[]domain.ResourceKind{domain.KindComputeInstance},
			map[string]string{},
			resourceChan,
		)

		// The handler error should be propagated
		assert.Equal(t, testError, err)

		handlerEC2.AssertExpectations(t)
	})

	t.Run("no supported resource kinds", func(t *testing.T) {
		mockLogger := new(MockLogger)
		mockLogger.On("WithFields", mock.Anything).Return(mockLogger)
		mockLogger.On("Warnf", mock.Anything, mock.Anything, mock.Anything).Return()

		// Create mocked provider with test handlers
		provider, _, _ := setupMockedProvider(t, mockLogger)

		// Request a resource kind that doesn't exist
		resourceChan := make(chan domain.PlatformResource, 10)
		err := provider.ListResources(
			context.Background(),
			[]domain.ResourceKind{"unsupported-kind"},
			map[string]string{},
			resourceChan,
		)

		// Should get an error about no supported kinds
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no supported resource kinds")
	})

	t.Run("empty requested kinds", func(t *testing.T) {
		mockLogger := new(MockLogger)
		mockLogger.On("WithFields", mock.Anything).Return(mockLogger)
		mockLogger.On("Debugf", mock.Anything, mock.Anything, mock.Anything).Return()

		// Create mocked provider with test handlers
		provider, _, _ := setupMockedProvider(t, mockLogger)

		// Call with empty kinds slice
		resourceChan := make(chan domain.PlatformResource, 10)
		err := provider.ListResources(
			context.Background(),
			[]domain.ResourceKind{},
			map[string]string{},
			resourceChan,
		)

		// Should return success even though no handlers were called
		assert.NoError(t, err)
	})
}

func TestGetResource(t *testing.T) {
	t.Run("successful retrieval", func(t *testing.T) {
		mockLogger := new(MockLogger)
		mockLogger.On("WithFields", mock.Anything).Return(mockLogger)

		// Create mocked provider with test handlers
		provider, handlerEC2, _ := setupMockedProvider(t, mockLogger)

		// Set up the handler to return a resource
		expectedResource := &mockResource{id: "i-1234", kind: domain.KindComputeInstance}
		handlerEC2.On("GetResource", mock.Anything, mock.Anything, "i-1234", mock.Anything).
			Return(expectedResource, nil)

		// Get the resource
		resource, err := provider.GetResource(
			context.Background(),
			domain.KindComputeInstance,
			"i-1234",
		)

		// Verify the result
		require.NoError(t, err)
		assert.Equal(t, expectedResource, resource)

		handlerEC2.AssertExpectations(t)
	})

	t.Run("handler error is propagated", func(t *testing.T) {
		mockLogger := new(MockLogger)
		mockLogger.On("WithFields", mock.Anything).Return(mockLogger)
		mockLogger.On("Errorf", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return()

		// Create mocked provider with test handlers
		provider, handlerEC2, _ := setupMockedProvider(t, mockLogger)

		// Set up handler to return an error
		testError := internalerrors.New(internalerrors.CodeInternal, "resource not found")
		handlerEC2.On("GetResource", mock.Anything, mock.Anything, "i-nonexistent", mock.Anything).
			Return(nil, testError)

		// Get the resource
		resource, err := provider.GetResource(
			context.Background(),
			domain.KindComputeInstance,
			"i-nonexistent",
		)

		// Verify the error is propagated
		assert.Equal(t, testError, err)
		assert.Nil(t, resource)

		handlerEC2.AssertExpectations(t)
	})

	t.Run("unsupported resource kind", func(t *testing.T) {
		mockLogger := new(MockLogger)
		mockLogger.On("WithFields", mock.Anything).Return(mockLogger)

		// Create mocked provider with test handlers
		provider, _, _ := setupMockedProvider(t, mockLogger)

		// Try to get a resource of an unsupported kind
		resource, err := provider.GetResource(
			context.Background(),
			"unsupported-kind",
			"any-id",
		)

		// Should get an error about unsupported kind
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not supported by AWS provider")
		assert.Nil(t, resource)
	})
}

// Helper to set up a provider with mock handlers
func setupMockedProvider(t *testing.T, logger *MockLogger) (*awsadapter.Provider, *MockAWSResourceHandler, *MockAWSResourceHandler) {
	// Create mock handlers
	handlerEC2 := new(MockAWSResourceHandler)
	handlerEC2.On("Kind").Return(domain.KindComputeInstance)

	handlerEBS := new(MockAWSResourceHandler)
	handlerEBS.On("Kind").Return(domain.KindStorageBucket)

	// Create provider using the exported test constructor
	provider := awsadapter.NewProviderWithHandlers(aws.Config{}, logger, handlerEC2, handlerEBS)
	require.NotNil(t, provider)

	return provider, handlerEC2, handlerEBS
}

// Mock resource implementation for testing
type mockResource struct {
	id   string
	kind domain.ResourceKind
}

func (m *mockResource) ID() string {
	return m.id
}

func (m *mockResource) Kind() domain.ResourceKind {
	return m.kind
}

func (m *mockResource) Attributes() map[string]any {
	return map[string]any{"test": "attribute"}
}

func (m *mockResource) Metadata() domain.ResourceMetadata {
	return domain.ResourceMetadata{
		Kind:               m.kind,
		ProviderType:       "aws",
		ProviderAssignedID: m.id,
		Region:             "us-west-2",
		AccountID:          "123456789012",
	}
}
