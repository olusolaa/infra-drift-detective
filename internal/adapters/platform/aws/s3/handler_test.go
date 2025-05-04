package s3

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	idderrors "github.com/olusolaa/infra-drift-detector/internal/errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/mock"

	"github.com/stretchr/testify/suite"

	s3mocks "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/s3/mocks"
	sharedmocks "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/shared/mocks"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	domainmocks "github.com/olusolaa/infra-drift-detector/internal/core/domain/mocks"
	portsmocks "github.com/olusolaa/infra-drift-detector/internal/core/ports/mocks"
)

type S3HandlerTestSuite struct {
	suite.Suite
	mockS3           *s3mocks.S3ClientInterface
	mockSTS          *sharedmocks.STSClientInterface
	mockBuilder      *s3mocks.S3ResourceBuilder
	mockLimiter      *sharedmocks.RateLimiter
	mockErrorHandler *sharedmocks.ErrorHandler
	mockLogger       *portsmocks.Logger
	awsConfig        aws.Config
	handler          *S3Handler
	ctx              context.Context
	cancel           context.CancelFunc
}

func (s *S3HandlerTestSuite) SetupTest() {
	// Instantiate all mocks with correct types
	s.mockS3 = new(s3mocks.S3ClientInterface)
	s.mockSTS = new(sharedmocks.STSClientInterface)
	s.mockBuilder = new(s3mocks.S3ResourceBuilder)
	s.mockLimiter = new(sharedmocks.RateLimiter)
	s.mockErrorHandler = new(sharedmocks.ErrorHandler)
	s.mockLogger = new(portsmocks.Logger)

	s.awsConfig = aws.Config{Region: "us-east-1"}
	s.ctx, s.cancel = context.WithTimeout(context.Background(), 5*time.Second)

	// Default logger expectations
	s.mockLogger.On("Debugf", mock.Anything, mock.Anything, mock.AnythingOfType("[]interface {}")).Maybe().Return()
	s.mockLogger.On("Infof", mock.Anything, mock.Anything, mock.AnythingOfType("[]interface {}")).Maybe().Return()
	//s.mockLogger.On("Warnf", mock.Anything, mock.Anything, mock.AnythingOfType("[]interface {}")).Maybe().Return()
	s.mockLogger.On("Errorf", mock.Anything, mock.Anything, mock.AnythingOfType("[]interface {}")).Maybe().Return()

	s.mockErrorHandler.On("Handle", mock.AnythingOfType("string"), mock.AnythingOfType("string"), mock.AnythingOfType("error"), mock.Anything).Maybe().Return(func(service, operation string, err error, ctx context.Context) error {
		return err // Default pass-through
	})

	// Create handler using functional options with mocks
	s.handler = NewHandler(s.awsConfig,
		WithSTSClient(s.mockSTS),
		WithS3Client(s.mockS3),
		WithS3Builder(s.mockBuilder),
		WithRateLimiter(s.mockLimiter),
		WithErrorHandler(s.mockErrorHandler),
	)
}

func (s *S3HandlerTestSuite) TearDownTest() {
	s.cancel()
}

func TestS3HandlerTestSuite(t *testing.T) {
	suite.Run(t, new(S3HandlerTestSuite))
}

func (s *S3HandlerTestSuite) TestKind() {
	s.Equal(domain.KindStorageBucket, s.handler.Kind())
}

func (s *S3HandlerTestSuite) TestGetAccountID_SuccessFirstTime() {
	expectedAccountID := "111122223333"

	// Expect limiter wait before STS call
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()

	s.mockSTS.On("GetCallerIdentity", mock.Anything, &sts.GetCallerIdentityInput{}).
		Return(&sts.GetCallerIdentityOutput{Account: aws.String(expectedAccountID)}, nil).Once()

	accID, err := s.handler.getAccountID(s.ctx, s.mockLogger)

	s.NoError(err)
	s.Equal(expectedAccountID, accID)
	s.Equal(expectedAccountID, s.handler.accountID) // Check internal cache update
	s.mockLimiter.AssertExpectations(s.T())
	s.mockSTS.AssertExpectations(s.T())
}

func (s *S3HandlerTestSuite) TestGetAccountID_SuccessCached() {
	expectedAccountID := "444455556666"
	s.handler.accountID = expectedAccountID // Pre-populate cache

	accID, err := s.handler.getAccountID(s.ctx, s.mockLogger)

	s.NoError(err)
	s.Equal(expectedAccountID, accID)
	// Assert that limiter and STS were NOT called due to cache hit
	s.mockLimiter.AssertNotCalled(s.T(), "Wait", mock.Anything, mock.Anything)
	s.mockSTS.AssertNotCalled(s.T(), "GetCallerIdentity", mock.Anything, mock.Anything)
}

func (s *S3HandlerTestSuite) TestGetAccountID_LimiterError() {
	limiterErr := errors.New("rate limit exceeded")
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(limiterErr).Once()

	accID, err := s.handler.getAccountID(s.ctx, s.mockLogger)

	s.ErrorIs(err, limiterErr)
	s.Empty(accID)
	s.Empty(s.handler.accountID)
	s.mockLimiter.AssertExpectations(s.T())
	s.mockSTS.AssertNotCalled(s.T(), "GetCallerIdentity", mock.Anything, mock.Anything)
	s.mockErrorHandler.AssertNotCalled(s.T(), "Handle", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func (s *S3HandlerTestSuite) TestGetAccountID_STSError() {
	fetchErr := errors.New("STS failed")
	wrappedErr := idderrors.New(idderrors.CodePlatformAuthError, "wrapped: STS failed")

	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockSTS.On("GetCallerIdentity", mock.Anything, &sts.GetCallerIdentityInput{}).
		Return(nil, fetchErr).Once()
	// Expect error handler to be called
	s.mockErrorHandler.On("Handle", "STS", "GetCallerIdentity", fetchErr, mock.Anything).
		Return(wrappedErr).Once()

	accID, err := s.handler.getAccountID(s.ctx, s.mockLogger)

	s.ErrorIs(err, wrappedErr) // Check for the error returned by the handler
	s.Empty(accID)
	s.Empty(s.handler.accountID)
	s.mockLimiter.AssertExpectations(s.T())
	s.mockSTS.AssertExpectations(s.T())
	s.mockErrorHandler.AssertExpectations(s.T())
}

func (s *S3HandlerTestSuite) TestGetAccountID_ErrorNoAccountInResponse() {
	// This tests an internal logic error, not an API error
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockSTS.On("GetCallerIdentity", mock.Anything, &sts.GetCallerIdentityInput{}).
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

func (s *S3HandlerTestSuite) TestListResources_Success() {
	accountID := "555666777888"
	bucket1Name := "list-bucket-1"
	bucket2Name := "list-bucket-2"

	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockSTS.On("GetCallerIdentity", mock.Anything, mock.Anything).
		Return(&sts.GetCallerIdentityOutput{Account: aws.String(accountID)}, nil).Once()

	listOutput := &s3.ListBucketsOutput{
		Buckets: []types.Bucket{
			{Name: aws.String(bucket1Name), CreationDate: aws.Time(time.Now())},
			{Name: aws.String(bucket2Name), CreationDate: aws.Time(time.Now())},
		},
	}
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockS3.On("ListBuckets", mock.Anything, &s3.ListBucketsInput{}).Return(listOutput, nil).Once()

	mockRes1 := new(domainmocks.PlatformResource)
	mockRes1.On("Metadata").Return(domain.ResourceMetadata{ProviderAssignedID: bucket1Name, AccountID: accountID, Kind: domain.KindStorageBucket, Region: "us-west-2"})
	s.mockBuilder.On("Build", mock.Anything, bucket1Name, accountID, mock.AnythingOfType("aws.Config"), s.mockLogger).
		Return(mockRes1, nil).Once()

	mockRes2 := new(domainmocks.PlatformResource)
	mockRes2.On("Metadata").Return(domain.ResourceMetadata{ProviderAssignedID: bucket2Name, AccountID: accountID, Kind: domain.KindStorageBucket, Region: "eu-west-1"})
	s.mockBuilder.On("Build", mock.Anything, bucket2Name, accountID, mock.AnythingOfType("aws.Config"), s.mockLogger).
		Return(mockRes2, nil).Once()

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
		s.Equal(domain.KindStorageBucket, meta.Kind)
		s.Equal(accountID, meta.AccountID)
		// No need to call Attributes here

		if meta.ProviderAssignedID == bucket1Name {
			s.Equal("us-west-2", meta.Region)
			found1 = true
		} else if meta.ProviderAssignedID == bucket2Name {
			s.Equal("eu-west-1", meta.Region)
			found2 = true
		}
	}
	s.True(found1, "Bucket 1 not found")
	s.True(found2, "Bucket 2 not found")

	s.mockLimiter.AssertExpectations(s.T()) // Called twice: getAccountID, ListBuckets
	s.mockSTS.AssertExpectations(s.T())
	s.mockS3.AssertExpectations(s.T())
	s.mockBuilder.AssertExpectations(s.T())
	s.mockErrorHandler.AssertNotCalled(s.T(), "Handle", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func (s *S3HandlerTestSuite) TestListResources_Empty() {
	accountID := "555666777888"
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockSTS.On("GetCallerIdentity", mock.Anything, mock.Anything).
		Return(&sts.GetCallerIdentityOutput{Account: aws.String(accountID)}, nil).Once()

	listOutput := &s3.ListBucketsOutput{Buckets: []types.Bucket{}}
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockS3.On("ListBuckets", mock.Anything, &s3.ListBucketsInput{}).Return(listOutput, nil).Once()

	outChan := make(chan domain.PlatformResource, 1)
	err := s.handler.ListResources(s.ctx, s.awsConfig, nil, s.mockLogger, outChan)

	s.NoError(err)
	_, ok := <-outChan
	s.False(ok)

	s.mockLimiter.AssertExpectations(s.T())
	s.mockSTS.AssertExpectations(s.T())
	s.mockS3.AssertExpectations(s.T())
	s.mockBuilder.AssertNotCalled(s.T(), "Build", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	s.mockErrorHandler.AssertNotCalled(s.T(), "Handle", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func (s *S3HandlerTestSuite) TestListResources_AccountIDError() {
	accountErr := errors.New("failed getting account")
	wrappedAccountErr := fmt.Errorf("failed to get AWS account ID needed for S3 listing: %w", accountErr)

	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockSTS.On("GetCallerIdentity", mock.Anything, mock.Anything).Return(nil, accountErr).Once()
	s.mockErrorHandler.On("Handle", "STS", "GetCallerIdentity", accountErr, mock.Anything).Return(accountErr).Once() // Error handler returns original error

	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockS3.On("ListBuckets", mock.Anything, &s3.ListBucketsInput{}).Return(&s3.ListBucketsOutput{}, nil).Once()

	s.mockLogger.On("Warnf", mock.Anything, "Failed to get account ID for listing S3 buckets, skipping list: %v", accountErr).Once()

	outChan := make(chan domain.PlatformResource)

	err := s.handler.ListResources(s.ctx, s.awsConfig, nil, s.mockLogger, outChan)
	s.Require().Error(err)
	s.EqualError(err, wrappedAccountErr.Error()) // Check the specific error message and wrapping

	select {
	case _, ok := <-outChan:
		s.False(ok, "Channel should be closed")
	default:
		s.Fail("Channel should be closed")
	}

	s.mockLimiter.AssertExpectations(s.T())
	s.mockSTS.AssertExpectations(s.T())
	s.mockS3.AssertExpectations(s.T())
	s.mockErrorHandler.AssertExpectations(s.T())
	s.mockBuilder.AssertNotCalled(s.T(), "Build", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	s.mockLogger.AssertCalled(s.T(), "Warnf", mock.Anything, "Failed to get account ID for listing S3 buckets, skipping list: %v", accountErr)
}

func (s *S3HandlerTestSuite) TestListResources_ListBucketsError() {
	listErr := errors.New("failed to list buckets")
	wrappedListErr := idderrors.New(idderrors.CodePlatformAPIError, "wrapped list")

	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockS3.On("ListBuckets", mock.Anything, &s3.ListBucketsInput{}).Return(nil, listErr).Once()
	s.mockErrorHandler.On("Handle", "S3", "ListBuckets", listErr, mock.Anything).Return(wrappedListErr).Once()

	outChan := make(chan domain.PlatformResource)
	err := s.handler.ListResources(s.ctx, s.awsConfig, nil, s.mockLogger, outChan)

	s.ErrorIs(err, wrappedListErr)

	select {
	case _, ok := <-outChan:
		s.False(ok, "Channel should be closed")
	default:
		s.Fail("Channel should be closed")
	}

	s.mockLimiter.AssertExpectations(s.T())
	s.mockS3.AssertExpectations(s.T())
	s.mockErrorHandler.AssertExpectations(s.T())
	s.mockSTS.AssertNotCalled(s.T(), "GetCallerIdentity", mock.Anything, mock.Anything)
	s.mockBuilder.AssertNotCalled(s.T(), "Build", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func (s *S3HandlerTestSuite) TestListResources_BuildError() {
	accountID := "555666777888"
	bucket1Name := "good-bucket"
	bucket2Name := "bad-bucket"
	buildErr := s.newMockAPIError(http.StatusNotFound, "The specified bucket does not exist")

	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockSTS.On("GetCallerIdentity", mock.Anything, mock.Anything).
		Return(&sts.GetCallerIdentityOutput{Account: aws.String(accountID)}, nil).Once()

	listOutput := &s3.ListBucketsOutput{
		Buckets: []types.Bucket{
			{Name: aws.String(bucket1Name)},
			{Name: aws.String(bucket2Name)},
		},
	}
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockS3.On("ListBuckets", mock.Anything, &s3.ListBucketsInput{}).Return(listOutput, nil).Once()

	mockRes1 := new(domainmocks.PlatformResource)
	mockRes1.On("Metadata").Return(domain.ResourceMetadata{ProviderAssignedID: bucket1Name, AccountID: accountID, Kind: domain.KindStorageBucket, Region: "us-west-2"})
	s.mockBuilder.On("Build", mock.Anything, bucket1Name, accountID, mock.AnythingOfType("aws.Config"), s.mockLogger).
		Return(mockRes1, nil).Once()

	s.mockBuilder.On("Build", mock.Anything, bucket2Name, accountID, mock.AnythingOfType("aws.Config"), s.mockLogger).
		Return(nil, buildErr).Once()

	s.mockLogger.On("Warnf", mock.Anything, "Error building S3 resource for bucket %s: %v", bucket2Name, buildErr).Once()

	outChan := make(chan domain.PlatformResource, 2)
	err := s.handler.ListResources(s.ctx, s.awsConfig, nil, s.mockLogger, outChan)

	s.NoError(err)

	results := []domain.PlatformResource{}
	for res := range outChan {
		results = append(results, res)
	}

	s.Len(results, 1)
	s.Equal(bucket1Name, results[0].Metadata().ProviderAssignedID)

	s.mockLimiter.AssertExpectations(s.T())
	s.mockSTS.AssertExpectations(s.T())
	s.mockS3.AssertExpectations(s.T())
	s.mockBuilder.AssertExpectations(s.T())
	s.mockErrorHandler.AssertNotCalled(s.T(), "Handle", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	s.mockLogger.AssertCalled(s.T(), "Warnf", mock.Anything, "Error building S3 resource for bucket %s: %v", bucket2Name, buildErr)
}

func (s *S3HandlerTestSuite) TestListResources_ContextCanceled() {
	accountID := "555666777888"
	bucket1Name := "bucket1"
	bucket2Name := "bucket2"

	ctx, cancel := context.WithCancel(context.Background())

	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockSTS.On("GetCallerIdentity", mock.Anything, mock.Anything).
		Return(&sts.GetCallerIdentityOutput{Account: aws.String(accountID)}, nil).Once()

	listOutput := &s3.ListBucketsOutput{
		Buckets: []types.Bucket{
			{Name: aws.String(bucket1Name)},
			{Name: aws.String(bucket2Name)},
		},
	}
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockS3.On("ListBuckets", mock.Anything, &s3.ListBucketsInput{}).Return(listOutput, nil).Once()

	s.mockBuilder.On("Build", mock.Anything, bucket1Name, mock.Anything, mock.AnythingOfType("aws.Config"), s.mockLogger).
		Run(func(args mock.Arguments) {
			cancel()
		}).Return(nil, context.Canceled).Once()

	s.mockLogger.On("Warnf", mock.Anything, "Error building S3 resource for bucket %s: %v", bucket1Name, context.Canceled).Once()

	s.mockBuilder.On("Build", mock.Anything, bucket2Name, mock.Anything, mock.AnythingOfType("aws.Config"), s.mockLogger).
		Maybe().Return(nil, nil)

	outChan := make(chan domain.PlatformResource)

	err := s.handler.ListResources(ctx, s.awsConfig, nil, s.mockLogger, outChan)
	s.ErrorIs(err, context.Canceled)
	_, isOpen := <-outChan
	s.False(isOpen, "Channel should be closed after ListResources returns")
}

func (s *S3HandlerTestSuite) TestGetResource_Success() {
	accountID := "123456789012"
	bucketName := "get-bucket-success"
	region := "us-east-1"

	// Mock HeadBucket call (limiter + S3)
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockS3.On("HeadBucket", mock.Anything, mock.MatchedBy(func(i *s3.HeadBucketInput) bool { return *i.Bucket == bucketName })).
		Return(&s3.HeadBucketOutput{}, nil).Once()

	// Mock getAccountID call (limiter + STS)
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockSTS.On("GetCallerIdentity", mock.Anything, mock.Anything).
		Return(&sts.GetCallerIdentityOutput{Account: aws.String(accountID)}, nil).Once()

	// Mock successful build using the helper struct
	mockRes := new(domainmocks.PlatformResource)
	mockRes.On("Metadata").Return(domain.ResourceMetadata{ProviderAssignedID: bucketName, AccountID: accountID, Kind: domain.KindStorageBucket, Region: region})
	s.mockBuilder.On("Build", mock.Anything, bucketName, accountID, mock.AnythingOfType("aws.Config"), s.mockLogger).
		Return(mockRes, nil).Once()

	resource, err := s.handler.GetResource(s.ctx, s.awsConfig, bucketName, s.mockLogger)

	s.NoError(err)
	s.NotNil(resource)
	s.Equal(bucketName, resource.Metadata().ProviderAssignedID)
	s.Equal(accountID, resource.Metadata().AccountID)
	s.Equal(region, resource.Metadata().Region)

	s.mockLimiter.AssertExpectations(s.T()) // Wait called twice
	s.mockS3.AssertExpectations(s.T())
	s.mockSTS.AssertExpectations(s.T())
	s.mockBuilder.AssertExpectations(s.T())
	s.mockErrorHandler.AssertNotCalled(s.T(), "Handle", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func (s *S3HandlerTestSuite) TestGetResource_AccountIDError() {
	bucketName := "account-error-bucket"
	accountErr := errors.New("failed getting account")
	wrappedErr := fmt.Errorf("failed to get AWS account ID: %w", accountErr)

	// First the handler calls limiter.Wait for the HeadBucket call
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()

	// Then the handler makes a HeadBucket call
	s.mockS3.On("HeadBucket", mock.Anything, &s3.HeadBucketInput{Bucket: aws.String(bucketName)}).
		Return(&s3.HeadBucketOutput{}, nil).Once()

	// Then the handler calls limiter.Wait for the GetCallerIdentity call
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()

	// Then the handler calls GetCallerIdentity which fails
	s.mockSTS.On("GetCallerIdentity", mock.Anything, mock.Anything).Return(nil, accountErr).Once()
	s.mockErrorHandler.On("Handle", "STS", "GetCallerIdentity", accountErr, mock.Anything).Return(accountErr).Once()

	// The specific Warnf call - specify the EXACT format of args that the handler is using
	s.mockLogger.On("Warnf", mock.Anything, "Failed to get account ID needed for S3 GetResource: %v", accountErr).Once()

	resource, err := s.handler.GetResource(s.ctx, s.awsConfig, bucketName, s.mockLogger)

	s.Nil(resource)
	s.EqualError(err, wrappedErr.Error())

	// Verify all expectations were met
	s.mockLimiter.AssertExpectations(s.T())
	s.mockS3.AssertExpectations(s.T())
	s.mockSTS.AssertExpectations(s.T())
	s.mockErrorHandler.AssertExpectations(s.T())
	s.mockLogger.AssertExpectations(s.T())

	// The builder should never be called since we fail before reaching that point
	s.mockBuilder.AssertNotCalled(s.T(), "Build", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func (s *S3HandlerTestSuite) TestGetResource_NotFound_HeadBucket() {
	bucketName := "get-bucket-not-found-head"
	// Pass HTTP status code (int) and message (string)
	notFoundErr := s.newMockAPIError(http.StatusNotFound, "Not Found")
	wrappedErr := idderrors.New(idderrors.CodeResourceNotFound, "wrapped not found")

	// Mock HeadBucket (limiter + S3 -> error)
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockS3.On("HeadBucket", mock.Anything, mock.Anything).Return(nil, notFoundErr).Once()
	// Expect error handler call
	s.mockErrorHandler.On("Handle", "S3", "HeadBucket", notFoundErr, mock.Anything).Return(wrappedErr).Once()

	resource, err := s.handler.GetResource(s.ctx, s.awsConfig, bucketName, s.mockLogger)

	s.ErrorIs(err, wrappedErr)
	s.Nil(resource)

	s.mockLimiter.AssertExpectations(s.T())
	s.mockS3.AssertExpectations(s.T())
	s.mockErrorHandler.AssertExpectations(s.T())
	s.mockSTS.AssertNotCalled(s.T(), "GetCallerIdentity", mock.Anything, mock.Anything)
	s.mockBuilder.AssertNotCalled(s.T(), "Build", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func (s *S3HandlerTestSuite) TestGetResource_HeadBucketForbidden_BuildSucceeds() {
	accountID := "123456789012"
	bucketName := "get-bucket-forbidden"
	region := "us-west-1"
	forbiddenErr := &awshttp.ResponseError{
		ResponseError: &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{ // Outer smithyhttp.Response
				Response: &http.Response{StatusCode: 403}, // Inner standard http.Response
			},
			Err: errors.New("forbidden"),
		},
	}

	// Mock HeadBucket (limiter + S3 -> 403)
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockS3.On("HeadBucket", mock.Anything, mock.Anything).Return(nil, forbiddenErr).Once()

	// Expect logger warning for 403 - use integer param as that's what the handler uses
	s.mockLogger.On("Warnf", mock.Anything, "HeadBucket returned %d status, attempting to continue with build", 403).Once()

	// Mock successful getAccountID (limiter + STS)
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockSTS.On("GetCallerIdentity", mock.Anything, mock.Anything).
		Return(&sts.GetCallerIdentityOutput{Account: aws.String(accountID)}, nil).Once()

	// Mock successful build using the helper struct
	mockRes := new(domainmocks.PlatformResource)
	mockRes.On("Metadata").Return(domain.ResourceMetadata{ProviderAssignedID: bucketName, AccountID: accountID, Kind: domain.KindStorageBucket, Region: region})
	s.mockBuilder.On("Build", mock.Anything, bucketName, accountID, mock.AnythingOfType("aws.Config"), s.mockLogger).
		Return(mockRes, nil).Once()

	resource, err := s.handler.GetResource(s.ctx, s.awsConfig, bucketName, s.mockLogger)

	s.NoError(err)
	s.NotNil(resource)
	s.Equal(bucketName, resource.Metadata().ProviderAssignedID)

	s.mockLimiter.AssertExpectations(s.T()) // Wait called twice
	s.mockS3.AssertExpectations(s.T())
	s.mockSTS.AssertExpectations(s.T())
	s.mockBuilder.AssertExpectations(s.T())
	s.mockErrorHandler.AssertNotCalled(s.T(), "Handle", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	s.mockLogger.AssertCalled(s.T(), "Warnf", mock.Anything, "HeadBucket returned %d status, attempting to continue with build", 403)
}

func (s *S3HandlerTestSuite) TestGetResource_HeadBucketRedirect_BuildSucceeds() {
	accountID := "123456789012"
	bucketName := "get-bucket-redirect"
	region := "eu-central-1"
	// Correct the nested Response literal
	redirectErr := &awshttp.ResponseError{
		ResponseError: &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{ // Outer smithyhttp.Response
				Response: &http.Response{StatusCode: 301}, // Inner standard http.Response
			},
			Err: errors.New("redirect"),
		},
	}

	// Mock HeadBucket (limiter + S3 -> 301)
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockS3.On("HeadBucket", mock.Anything, mock.Anything).Return(nil, redirectErr).Once()

	// Expect logger warning for 301
	s.mockLogger.On("Warnf", mock.Anything, "HeadBucket returned %d status, attempting to continue with build", 301).Once()

	// Mock successful getAccountID (limiter + STS)
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockSTS.On("GetCallerIdentity", mock.Anything, mock.Anything).
		Return(&sts.GetCallerIdentityOutput{Account: aws.String(accountID)}, nil).Once()

	// Mock successful build using the helper struct
	mockRes := new(domainmocks.PlatformResource)
	mockRes.On("Metadata").Return(domain.ResourceMetadata{ProviderAssignedID: bucketName, AccountID: accountID, Kind: domain.KindStorageBucket, Region: region})
	s.mockBuilder.On("Build", mock.Anything, bucketName, accountID, mock.AnythingOfType("aws.Config"), s.mockLogger).
		Return(mockRes, nil).Once()

	resource, err := s.handler.GetResource(s.ctx, s.awsConfig, bucketName, s.mockLogger)

	s.NoError(err)
	s.NotNil(resource)
	s.Equal(bucketName, resource.Metadata().ProviderAssignedID)

	s.mockLimiter.AssertExpectations(s.T()) // Wait called twice
	s.mockS3.AssertExpectations(s.T())
	s.mockSTS.AssertExpectations(s.T())
	s.mockBuilder.AssertExpectations(s.T())
	s.mockErrorHandler.AssertNotCalled(s.T(), "Handle", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	s.mockLogger.AssertCalled(s.T(), "Warnf", mock.Anything, "HeadBucket returned %d status, attempting to continue with build", 301)
}

func (s *S3HandlerTestSuite) TestGetResource_HeadBucketErrorOther() {
	bucketName := "get-bucket-head-fail"
	headErr := errors.New("random head error")
	wrappedErr := idderrors.New(idderrors.CodePlatformAPIError, "wrapped head fail")

	// Mock HeadBucket (limiter + S3 -> error)
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockS3.On("HeadBucket", mock.Anything, mock.Anything).Return(nil, headErr).Once()
	// Expect error handler call
	s.mockErrorHandler.On("Handle", "S3", "HeadBucket", headErr, mock.Anything).Return(wrappedErr).Once()

	resource, err := s.handler.GetResource(s.ctx, s.awsConfig, bucketName, s.mockLogger)

	s.ErrorIs(err, wrappedErr)
	s.Nil(resource)

	s.mockLimiter.AssertExpectations(s.T())
	s.mockS3.AssertExpectations(s.T())
	s.mockErrorHandler.AssertExpectations(s.T())
	s.mockSTS.AssertNotCalled(s.T(), "GetCallerIdentity", mock.Anything, mock.Anything)
	s.mockBuilder.AssertNotCalled(s.T(), "Build", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func (s *S3HandlerTestSuite) TestGetResource_BuildFails() {
	accountID := "123456789012"
	bucketName := "get-bucket-build-fail"
	buildErr := errors.New("builder failed spectacularly")

	// Mock HeadBucket call (limiter + S3)
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockS3.On("HeadBucket", mock.Anything, mock.Anything).Return(&s3.HeadBucketOutput{}, nil).Once()

	// Mock getAccountID call (limiter + STS)
	s.mockLimiter.On("Wait", mock.Anything, s.mockLogger).Return(nil).Once()
	s.mockSTS.On("GetCallerIdentity", mock.Anything, mock.Anything).
		Return(&sts.GetCallerIdentityOutput{Account: aws.String(accountID)}, nil).Once()

	// Mock failed build
	s.mockBuilder.On("Build", mock.Anything, bucketName, accountID, mock.AnythingOfType("aws.Config"), s.mockLogger).
		Return(nil, buildErr).Once()

	resource, err := s.handler.GetResource(s.ctx, s.awsConfig, bucketName, s.mockLogger)

	s.ErrorIs(err, buildErr) // Expect the exact error from the builder
	s.Nil(resource)

	s.mockLimiter.AssertExpectations(s.T()) // Wait called twice
	s.mockS3.AssertExpectations(s.T())
	s.mockSTS.AssertExpectations(s.T())
	s.mockBuilder.AssertExpectations(s.T())
	s.mockErrorHandler.AssertNotCalled(s.T(), "Handle", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func (s *S3HandlerTestSuite) newMockAPIError(code int, message string) error {
	return &awshttp.ResponseError{
		ResponseError: &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{
				Response: &http.Response{StatusCode: code},
			},
			Err: errors.New(message),
		},
	}
}
