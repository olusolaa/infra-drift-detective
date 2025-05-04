package ec2

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	// Import mocks for EC2 interfaces
	ec2mocks "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/ec2/mocks"
	// Import mocks for shared interfaces
	sharedmocks "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/shared/mocks"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	portsmocks "github.com/olusolaa/infra-drift-detector/internal/core/ports/mocks"
	idderrors "github.com/olusolaa/infra-drift-detector/internal/errors"
)

type EC2HandlerTestSuite struct {
	suite.Suite
	mockEC2          *ec2mocks.EC2ClientInterface
	mockSTS          *sharedmocks.STSClientInterface
	mockLimiter      *sharedmocks.RateLimiter
	mockErrorHandler *sharedmocks.ErrorHandler
	mockLogger       *portsmocks.Logger
	mockPaginator    *ec2mocks.EC2InstancesPaginator // Mock the paginator as well
	awsConfig        aws.Config
	handler          *EC2Handler
	ctx              context.Context
	cancel           context.CancelFunc
}

func (s *EC2HandlerTestSuite) SetupTest() {
	// Instantiate all mocks
	s.mockEC2 = new(ec2mocks.EC2ClientInterface)
	s.mockSTS = new(sharedmocks.STSClientInterface)
	s.mockLimiter = new(sharedmocks.RateLimiter)
	s.mockErrorHandler = new(sharedmocks.ErrorHandler)
	s.mockLogger = new(portsmocks.Logger)
	s.mockPaginator = new(ec2mocks.EC2InstancesPaginator) // Initialize paginator mock

	s.awsConfig = aws.Config{Region: "us-east-1"}
	s.ctx, s.cancel = context.WithTimeout(context.Background(), 5*time.Second)

	// Default logger expectations - Define expectations for different argument counts
	// Match calls with ctx and format string ONLY (2 args)
	s.mockLogger.On("Debugf", mock.Anything, mock.AnythingOfType("string")).Maybe().Return()
	s.mockLogger.On("Infof", mock.Anything, mock.AnythingOfType("string")).Maybe().Return()
	s.mockLogger.On("Warnf", mock.Anything, mock.AnythingOfType("string")).Maybe().Return()
	s.mockLogger.On("Errorf", mock.Anything, mock.AnythingOfType("string")).Maybe().Return()
	// Match calls with ctx, format string, AND variadic args (3+ args)
	s.mockLogger.On("Debugf", mock.Anything, mock.AnythingOfType("string"), mock.Anything).Maybe().Return()
	s.mockLogger.On("Infof", mock.Anything, mock.AnythingOfType("string"), mock.Anything).Maybe().Return()
	s.mockLogger.On("Warnf", mock.Anything, mock.AnythingOfType("string"), mock.Anything).Maybe().Return()
	s.mockLogger.On("Errorf", mock.Anything, mock.AnythingOfType("string"), mock.Anything).Maybe().Return()
	// WithFields expectation
	s.mockLogger.On("WithFields", mock.Anything).Maybe().Return(s.mockLogger)

	// Default error handler expectation (pass-through)
	/*
		s.mockErrorHandler.On("Handle", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe().Return(func(service, operation string, err error, ctx context.Context) error {
			return err // Default pass-through
		})
	*/

	// Remove default limiter expectation to avoid conflicts with test-specific expectations

	// Create handler using functional options with mocks
	s.handler = NewHandler(s.awsConfig,
		WithSTSClient(s.mockSTS),
		WithEC2Client(s.mockEC2),
		WithRateLimiter(s.mockLimiter),
		WithErrorHandler(s.mockErrorHandler),
	)

	// Override the paginator factory in the handler to return our mock paginator
	s.handler.paginatorFactory = func(client EC2ClientInterface, input *ec2.DescribeInstancesInput) EC2InstancesPaginator {
		return s.mockPaginator
	}
}

func (s *EC2HandlerTestSuite) TearDownTest() {
	s.cancel()
}

func TestEC2HandlerTestSuite(t *testing.T) {
	suite.Run(t, new(EC2HandlerTestSuite))
}

func (s *EC2HandlerTestSuite) TestKind() {
	s.Equal(domain.KindComputeInstance, s.handler.Kind())
}

// --- getAccountID Tests ---

func (s *EC2HandlerTestSuite) TestGetAccountID_SuccessFirstTime() {
	expectedAccountID := "111122223333"

	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockSTS.On("GetCallerIdentity", mock.Anything, mock.AnythingOfType("*sts.GetCallerIdentityInput")).
		Return(&sts.GetCallerIdentityOutput{Account: aws.String(expectedAccountID)}, nil).Once()

	accID, err := s.handler.getAccountID(s.ctx, s.mockLogger)

	s.NoError(err)
	s.Equal(expectedAccountID, accID)
	s.Equal(expectedAccountID, s.handler.accountID) // Check internal cache update
	s.mockLimiter.AssertExpectations(s.T())
	s.mockSTS.AssertExpectations(s.T())
	s.mockErrorHandler.AssertNotCalled(s.T(), "Handle", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func (s *EC2HandlerTestSuite) TestGetAccountID_SuccessCached() {
	expectedAccountID := "444455556666"
	s.handler.accountID = expectedAccountID // Pre-populate cache

	accID, err := s.handler.getAccountID(s.ctx, s.mockLogger)

	s.NoError(err)
	s.Equal(expectedAccountID, accID)
	// Assert that limiter and STS were NOT called due to cache hit
	s.mockLimiter.AssertNotCalled(s.T(), "Wait", mock.Anything, mock.Anything)
	s.mockSTS.AssertNotCalled(s.T(), "GetCallerIdentity", mock.Anything, mock.Anything)
	s.mockErrorHandler.AssertNotCalled(s.T(), "Handle", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func (s *EC2HandlerTestSuite) TestGetAccountID_LimiterError() {
	limiterErr := fmt.Errorf("rate limit exceeded")
	wrappedErr := fmt.Errorf("wrapped limiter error") // Mock error returned by handler

	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(limiterErr).Once()
	// Expect the error handler to be called with the limiter error
	s.mockErrorHandler.On("Handle", "Limiter", "Wait", limiterErr, mock.Anything).
		Return(wrappedErr).Once() // Handler returns wrappedErr

	accID, err := s.handler.getAccountID(s.ctx, s.mockLogger)

	// Assert the error is the one returned by the handler
	s.ErrorIs(err, wrappedErr)
	s.Empty(accID)
	s.Empty(s.handler.accountID)
	s.mockLimiter.AssertExpectations(s.T())
	s.mockErrorHandler.AssertExpectations(s.T()) // Assert handler was called
	s.mockSTS.AssertNotCalled(s.T(), "GetCallerIdentity", mock.Anything, mock.Anything)
}

func (s *EC2HandlerTestSuite) TestGetAccountID_STSError() {
	fetchErr := fmt.Errorf("STS failed")
	wrappedErr := fmt.Errorf("[PLATFORM_AUTH_ERROR] wrapped: STS failed")

	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockSTS.On("GetCallerIdentity", mock.Anything, mock.AnythingOfType("*sts.GetCallerIdentityInput")).
		Return(nil, fetchErr).Once()
	// Expect error handler to be called
	s.mockErrorHandler.On("Handle", "STS", "GetCallerIdentity", fetchErr, mock.Anything).
		Return(wrappedErr).Once()

	accID, err := s.handler.getAccountID(s.ctx, s.mockLogger)

	s.ErrorIs(err, wrappedErr)
	s.Empty(accID)
	s.Empty(s.handler.accountID)
	s.mockLimiter.AssertExpectations(s.T())
	s.mockSTS.AssertExpectations(s.T())
	s.mockErrorHandler.AssertExpectations(s.T())
}

func (s *EC2HandlerTestSuite) TestGetAccountID_ErrorNoAccountInResponse() {
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockSTS.On("GetCallerIdentity", mock.Anything, mock.AnythingOfType("*sts.GetCallerIdentityInput")).
		Return(&sts.GetCallerIdentityOutput{Account: nil}, nil).Once()

	accID, err := s.handler.getAccountID(s.ctx, s.mockLogger)

	s.Error(err)
	s.Equal(idderrors.CodePlatformAPIError, idderrors.GetCode(err))
	s.Contains(err.Error(), "AWS caller identity response did not contain Account ID")
	s.Empty(accID)
	s.Empty(s.handler.accountID)
	s.mockLimiter.AssertExpectations(s.T())
	s.mockSTS.AssertExpectations(s.T())
	s.mockErrorHandler.AssertNotCalled(s.T(), "Handle", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// --- ListResources Tests ---

func (s *EC2HandlerTestSuite) TestListResources_Success() {
	accountID := "555666777888"
	instanceID1 := "i-123"
	instanceID2 := "i-456"

	// Mock getAccountID success
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once() // For getAccountID
	s.mockSTS.On("GetCallerIdentity", mock.Anything, mock.AnythingOfType("*sts.GetCallerIdentityInput")).
		Return(&sts.GetCallerIdentityOutput{Account: aws.String(accountID)}, nil).Once()

	// Mock paginator setup
	instance1 := ec2types.Instance{InstanceId: aws.String(instanceID1)}
	instance2 := ec2types.Instance{InstanceId: aws.String(instanceID2)}
	page1Output := &ec2.DescribeInstancesOutput{
		Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{instance1}}},
	}
	page2Output := &ec2.DescribeInstancesOutput{
		Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{instance2}}},
	}

	s.mockPaginator.On("HasMorePages").Return(true).Once()                   // First call
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once() // For page 1
	s.mockPaginator.On("NextPage", mock.Anything).Return(page1Output, nil).Once()

	s.mockPaginator.On("HasMorePages").Return(true).Once()                   // Second call
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once() // For page 2
	s.mockPaginator.On("NextPage", mock.Anything).Return(page2Output, nil).Once()

	s.mockPaginator.On("HasMorePages").Return(false).Once() // Final call

	// We need to mock the attribute/volume calls made inside newEc2InstanceResource
	s.mockEC2.On("DescribeInstanceAttribute", mock.Anything, mock.AnythingOfType("*ec2.DescribeInstanceAttributeInput")).Maybe().Return(&ec2.DescribeInstanceAttributeOutput{}, nil)
	s.mockEC2.On("DescribeVolumes", mock.Anything, mock.AnythingOfType("*ec2.DescribeVolumesInput")).Maybe().Return(&ec2.DescribeVolumesOutput{}, nil)

	outChan := make(chan domain.PlatformResource, 5)
	err := s.handler.ListResources(s.ctx, s.awsConfig, nil, s.mockLogger, outChan)
	s.NoError(err)

	results := []domain.PlatformResource{}
	for res := range outChan {
		results = append(results, res)
	}

	s.Len(results, 2)
	found1, found2 := false, false
	for _, res := range results {
		meta := res.Metadata()
		s.Equal(domain.KindComputeInstance, meta.Kind)
		s.Equal(accountID, meta.AccountID)

		if meta.ProviderAssignedID == instanceID1 {
			found1 = true
		} else if meta.ProviderAssignedID == instanceID2 {
			found2 = true
		}
	}
	s.True(found1, "Instance 1 not found")
	s.True(found2, "Instance 2 not found")

	s.mockLimiter.AssertExpectations(s.T()) // Wait called 3 times (getAcc, page1, page2)
	s.mockSTS.AssertExpectations(s.T())
	s.mockPaginator.AssertExpectations(s.T())
	s.mockErrorHandler.AssertNotCalled(s.T(), "Handle", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func (s *EC2HandlerTestSuite) TestListResources_PaginationError() {
	accountID := "555666777888"
	pageErr := errors.New("pagination failed")
	wrappedErr := idderrors.New(idderrors.CodePlatformAPIError, "wrapped page error")

	// Mock getAccountID success
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockSTS.On("GetCallerIdentity", mock.Anything, mock.AnythingOfType("*sts.GetCallerIdentityInput")).
		Return(&sts.GetCallerIdentityOutput{Account: aws.String(accountID)}, nil).Once()

	// Mock paginator failure
	s.mockPaginator.On("HasMorePages").Return(true).Once()
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockPaginator.On("NextPage", mock.Anything).Return(nil, pageErr).Once()
	s.mockErrorHandler.On("Handle", "EC2", "DescribeInstances:Page1", pageErr, mock.Anything).Return(wrappedErr).Once()

	outChan := make(chan domain.PlatformResource)
	err := s.handler.ListResources(s.ctx, s.awsConfig, nil, s.mockLogger, outChan)

	s.ErrorIs(err, wrappedErr)
	_, ok := <-outChan
	s.False(ok, "Channel should be closed on error")

	s.mockLimiter.AssertExpectations(s.T())
	s.mockSTS.AssertExpectations(s.T())
	s.mockPaginator.AssertExpectations(s.T())
	s.mockErrorHandler.AssertExpectations(s.T())
}

func (s *EC2HandlerTestSuite) TestListResources_ContextCanceledDuringPagination() {
	accountID := "555666777888"

	// Mock getAccountID success
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockSTS.On("GetCallerIdentity", mock.Anything, mock.AnythingOfType("*sts.GetCallerIdentityInput")).
		Return(&sts.GetCallerIdentityOutput{Account: aws.String(accountID)}, nil).Once()

	// Mock paginator setup
	instance1 := ec2types.Instance{InstanceId: aws.String("i-cancel-1")}
	page1Output := &ec2.DescribeInstancesOutput{
		Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{instance1}}},
	}

	// Page 1 succeeds
	s.mockPaginator.On("HasMorePages").Return(true).Once()
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockPaginator.On("NextPage", mock.Anything).Return(page1Output, nil).Once()

	// Cancel context before next HasMorePages check
	s.mockPaginator.On("HasMorePages").Return(true).Once().Run(func(args mock.Arguments) {
		s.cancel() // Cancel the test suite's context
	})
	// Don't expect Wait or NextPage for page 2

	// Expect Maybe attribute/volume calls for the instance from page 1
	s.mockEC2.On("DescribeInstanceAttribute", mock.Anything, mock.AnythingOfType("*ec2.DescribeInstanceAttributeInput")).Maybe().Return(&ec2.DescribeInstanceAttributeOutput{}, nil)
	s.mockEC2.On("DescribeVolumes", mock.Anything, mock.AnythingOfType("*ec2.DescribeVolumesInput")).Maybe().Return(&ec2.DescribeVolumesOutput{}, nil)

	outChan := make(chan domain.PlatformResource, 1)
	err := s.handler.ListResources(s.ctx, s.awsConfig, nil, s.mockLogger, outChan)

	s.ErrorIs(err, context.Canceled) // Expect context canceled or deadline exceeded depending on timing

	s.mockLimiter.AssertExpectations(s.T())
	s.mockSTS.AssertExpectations(s.T())
	s.mockPaginator.AssertExpectations(s.T())
	s.mockErrorHandler.AssertNotCalled(s.T(), "Handle", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// --- GetResource Tests ---

func (s *EC2HandlerTestSuite) TestGetResource_Success() {
	accountID := "123456789012"
	instanceID := "i-get-success"

	// Mock DescribeInstances call
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once() // For DescribeInstances
	instance := ec2types.Instance{InstanceId: aws.String(instanceID)}
	describeOutput := &ec2.DescribeInstancesOutput{
		Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{instance}}},
	}
	s.mockEC2.On("DescribeInstances", mock.Anything, mock.MatchedBy(func(i *ec2.DescribeInstancesInput) bool {
		return len(i.InstanceIds) == 1 && i.InstanceIds[0] == instanceID
	})).Return(describeOutput, nil).Once()

	// Mock getAccountID call
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once() // For getAccountID
	s.mockSTS.On("GetCallerIdentity", mock.Anything, mock.AnythingOfType("*sts.GetCallerIdentityInput")).
		Return(&sts.GetCallerIdentityOutput{Account: aws.String(accountID)}, nil).Once()

	// Mock attribute/volume calls needed by newEc2InstanceResource
	s.mockEC2.On("DescribeInstanceAttribute", mock.Anything, mock.AnythingOfType("*ec2.DescribeInstanceAttributeInput")).Maybe().Return(&ec2.DescribeInstanceAttributeOutput{}, nil)
	s.mockEC2.On("DescribeVolumes", mock.Anything, mock.AnythingOfType("*ec2.DescribeVolumesInput")).Maybe().Return(&ec2.DescribeVolumesOutput{}, nil)

	resource, err := s.handler.GetResource(s.ctx, s.awsConfig, instanceID, s.mockLogger)

	s.NoError(err)
	s.NotNil(resource)
	s.Equal(instanceID, resource.Metadata().ProviderAssignedID)
	s.Equal(accountID, resource.Metadata().AccountID)
	s.Equal(domain.KindComputeInstance, resource.Metadata().Kind)

	s.mockLimiter.AssertExpectations(s.T()) // Wait called twice
	s.mockEC2.AssertExpectations(s.T())     // DescribeInstances called
	s.mockSTS.AssertExpectations(s.T())
	s.mockErrorHandler.AssertNotCalled(s.T(), "Handle", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func (s *EC2HandlerTestSuite) TestGetResource_DescribeError() {
	instanceID := "i-get-fail"
	describeErr := errors.New("describe failed")
	wrappedErr := idderrors.New(idderrors.CodePlatformAPIError, "wrapped describe")

	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockEC2.On("DescribeInstances", mock.Anything, mock.Anything).Return(nil, describeErr).Once()
	s.mockErrorHandler.On("Handle", "EC2", "DescribeInstances", describeErr, mock.Anything).Return(wrappedErr).Once()

	resource, err := s.handler.GetResource(s.ctx, s.awsConfig, instanceID, s.mockLogger)

	s.ErrorIs(err, wrappedErr)
	s.Nil(resource)

	s.mockLimiter.AssertExpectations(s.T())
	s.mockEC2.AssertExpectations(s.T())
	s.mockErrorHandler.AssertExpectations(s.T())
	s.mockSTS.AssertNotCalled(s.T(), "GetCallerIdentity", mock.Anything, mock.Anything)
}

func (s *EC2HandlerTestSuite) TestGetResource_NotFound() {
	instanceID := "i-get-not-found"

	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	// Return empty response
	describeOutput := &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{}}
	s.mockEC2.On("DescribeInstances", mock.Anything, mock.Anything).Return(describeOutput, nil).Once()

	resource, err := s.handler.GetResource(s.ctx, s.awsConfig, instanceID, s.mockLogger)

	s.Error(err)
	s.Equal(idderrors.CodeResourceNotFound, idderrors.GetCode(err))
	s.Nil(resource)

	s.mockLimiter.AssertExpectations(s.T())
	s.mockEC2.AssertExpectations(s.T())
	s.mockErrorHandler.AssertNotCalled(s.T(), "Handle", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	s.mockSTS.AssertNotCalled(s.T(), "GetCallerIdentity", mock.Anything, mock.Anything)
}

// Helper to create mock AWS ResponseError for NotFound tests
func (s *EC2HandlerTestSuite) newMockNotFoundError() error {
	// Simulate an EC2 NotFound error (e.g., InvalidInstanceID.NotFound)
	// Note: Directly creating smithy.APIError might be tricky. Using ResponseError is often sufficient.
	return &awshttp.ResponseError{
		ResponseError: &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{
				Response: &http.Response{StatusCode: http.StatusNotFound},
			},
			// Attach a GenericAPIError matching what the error handler might look for
			Err: &smithy.GenericAPIError{Code: "InvalidInstanceID.NotFound", Message: "Instance not found"},
		},
	}
}

// This test was previously named TestGetResource_NotFound_HeadBucket, updated name
// and use the newMockNotFoundError helper.
func (s *EC2HandlerTestSuite) TestGetResource_DescribeError_NotFound() {
	instanceID := "i-get-not-found-describe" // Renamed for clarity
	notFoundErr := s.newMockNotFoundError()  // Use helper to create a NotFound error
	wrappedErr := idderrors.New(idderrors.CodeResourceNotFound, "wrapped not found")

	// Mock DescribeInstances (limiter + EC2 -> error)
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockEC2.On("DescribeInstances", mock.Anything, mock.Anything).Return(nil, notFoundErr).Once()
	// Expect error handler call
	s.mockErrorHandler.On("Handle", "EC2", "DescribeInstances", notFoundErr, mock.Anything).Return(wrappedErr).Once()

	resource, err := s.handler.GetResource(s.ctx, s.awsConfig, instanceID, s.mockLogger)

	s.ErrorIs(err, wrappedErr)
	s.Nil(resource)

	s.mockLimiter.AssertExpectations(s.T())
	s.mockEC2.AssertExpectations(s.T())
	s.mockErrorHandler.AssertExpectations(s.T())
	s.mockSTS.AssertNotCalled(s.T(), "GetCallerIdentity", mock.Anything, mock.Anything)
}

func (s *EC2HandlerTestSuite) TestGetResource_AccountIDError() {
	instanceID := "i-get-acc-fail"
	accountErr := errors.New("sts failed")
	wrappedAccErr := idderrors.New(idderrors.CodePlatformAPIError, "wrapped sts")

	// Mock DescribeInstances success
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once() // For DescribeInstances
	instance := ec2types.Instance{InstanceId: aws.String(instanceID)}
	describeOutput := &ec2.DescribeInstancesOutput{
		Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{instance}}},
	}
	s.mockEC2.On("DescribeInstances", mock.Anything, mock.Anything).Return(describeOutput, nil).Once()

	// Mock getAccountID failure
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once() // For getAccountID
	s.mockSTS.On("GetCallerIdentity", mock.Anything, mock.AnythingOfType("*sts.GetCallerIdentityInput")).
		Return(nil, accountErr).Once()
	s.mockErrorHandler.On("Handle", "STS", "GetCallerIdentity", accountErr, mock.Anything).Return(wrappedAccErr).Once()
	s.mockLogger.On("Warnf", mock.Anything, "Proceeding without AWS Account ID for EC2 GetResource: %v", wrappedAccErr).Once()

	// Mock attribute/volume calls needed by newEc2InstanceResource (will still be called)
	s.mockEC2.On("DescribeInstanceAttribute", mock.Anything, mock.AnythingOfType("*ec2.DescribeInstanceAttributeInput")).Maybe().Return(&ec2.DescribeInstanceAttributeOutput{}, nil)
	s.mockEC2.On("DescribeVolumes", mock.Anything, mock.AnythingOfType("*ec2.DescribeVolumesInput")).Maybe().Return(&ec2.DescribeVolumesOutput{}, nil)

	resource, err := s.handler.GetResource(s.ctx, s.awsConfig, instanceID, s.mockLogger)

	// GetResource allows proceeding without accountID, so no error is returned here
	// The error happens during getAccountID but is logged as a warning.
	s.NoError(err)
	s.NotNil(resource)
	s.Equal("", resource.Metadata().AccountID) // Account ID should be empty

	s.mockLimiter.AssertExpectations(s.T()) // Wait called twice
	s.mockEC2.AssertExpectations(s.T())
	s.mockSTS.AssertExpectations(s.T())
	s.mockErrorHandler.AssertExpectations(s.T()) // Error handled for STS call
	s.mockLogger.AssertCalled(s.T(), "Warnf", mock.Anything, "Proceeding without AWS Account ID for EC2 GetResource: %v", wrappedAccErr)
}

// Add a placeholder test for newEc2InstanceResource if needed, or ensure mapper_test covers it.
func (s *EC2HandlerTestSuite) TestNewEc2InstanceResourcePlaceholder() {
	s.T().Skip("Mapping logic tested separately in mapper_test.go")
}
