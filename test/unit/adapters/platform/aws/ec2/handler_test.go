package ec2_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	ec2adapter "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/ec2"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/mocks"
)

// TestEC2HandlerKind tests the Kind method of the EC2Handler
func TestEC2HandlerKind(t *testing.T) {
	handler := ec2adapter.NewHandler(aws.Config{})
	assert.Equal(t, domain.KindComputeInstance, handler.Kind())
}

// TestEC2HandlerGetAccountID tests the GetAccountID method of the EC2Handler
func TestEC2HandlerGetAccountID(t *testing.T) {
	accountID := "123456789012"

	tests := []struct {
		name            string
		setupMocks      func(mockSTSClient *mocks.MockSTSClient)
		expectedAccount string
		expectedError   bool
	}{
		{
			name: "successful retrieval",
			setupMocks: func(mockSTSClient *mocks.MockSTSClient) {
				mockSTSClient.On("GetCallerIdentity", mock.Anything, mock.Anything, mock.Anything).Return(
					&sts.GetCallerIdentityOutput{
						Account: &accountID,
					}, nil)
			},
			expectedAccount: accountID,
			expectedError:   false,
		},
		{
			name: "API error",
			setupMocks: func(mockSTSClient *mocks.MockSTSClient) {
				mockSTSClient.On("GetCallerIdentity", mock.Anything, mock.Anything, mock.Anything).Return(
					nil, errors.New("API error"))
			},
			expectedAccount: "",
			expectedError:   true,
		},
		{
			name: "nil account ID",
			setupMocks: func(mockSTSClient *mocks.MockSTSClient) {
				mockSTSClient.On("GetCallerIdentity", mock.Anything, mock.Anything, mock.Anything).Return(
					&sts.GetCallerIdentityOutput{
						Account: nil,
					}, nil)
			},
			expectedAccount: "",
			expectedError:   true,
		},
		{
			name: "cached account ID",
			setupMocks: func(mockSTSClient *mocks.MockSTSClient) {
				// No mock calls needed as we'll set the account ID directly
			},
			expectedAccount: accountID,
			expectedError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock STS client
			mockSTSClient := new(mocks.MockSTSClient)

			// Create handler with mock
			handler := &ec2adapter.EC2Handler{
				STSClient: mockSTSClient,
			}

			// For the cached test, set the account ID
			if tt.name == "cached account ID" {
				handler.SetAccountID(accountID)
			}

			// Setup mocks
			tt.setupMocks(mockSTSClient)

			// Call GetAccountID
			ctx := context.Background()
			result, err := handler.GetAccountID(ctx)

			// Check results
			if tt.expectedError {
				assert.Error(t, err)
				assert.Empty(t, result)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedAccount, result)
			}

			// Verify mocks
			mockSTSClient.AssertExpectations(t)
		})
	}
}

// setupMockLogger sets up a mock logger with common expectations
func setupMockLogger() *mocks.MockLogger {
	mockLogger := new(mocks.MockLogger)
	mockLogger.On("Debug", mock.Anything, mock.Anything).Return()
	mockLogger.On("Info", mock.Anything, mock.Anything).Return()
	mockLogger.On("Warn", mock.Anything, mock.Anything).Return()
	mockLogger.On("Error", mock.Anything, mock.Anything, mock.Anything).Return()
	mockLogger.On("WithFields", mock.Anything).Return(mockLogger)

	// Allow any calls to the formatter methods with any arguments
	mockLogger.On("Debugf", mock.Anything, mock.Anything, mock.AnythingOfType("int")).Maybe().Return()
	mockLogger.On("Infof", mock.Anything, mock.Anything, mock.Anything).Maybe().Return()
	mockLogger.On("Warnf", mock.Anything, mock.Anything, mock.Anything).Maybe().Return()
	mockLogger.On("Errorf", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Maybe().Return()

	return mockLogger
}

// TestEC2HandlerListResources tests the ListResources method of the EC2Handler
func TestEC2HandlerListResources(t *testing.T) {
	region := "us-west-2"
	cfg := aws.Config{
		Region: region,
	}

	// Define test instances
	instance1 := types.Instance{
		InstanceId:   aws.String("i-12345"),
		InstanceType: types.InstanceTypeT2Micro,
		Tags: []types.Tag{
			{Key: aws.String("Name"), Value: aws.String("Instance1")},
			{Key: aws.String("Environment"), Value: aws.String("Test")},
		},
	}

	instance2 := types.Instance{
		InstanceId:   aws.String("i-67890"),
		InstanceType: types.InstanceTypeT2Micro,
		Tags: []types.Tag{
			{Key: aws.String("Name"), Value: aws.String("Instance2")},
			{Key: aws.String("Environment"), Value: aws.String("Test")},
		},
	}

	// Define test cases
	tests := []struct {
		name                string
		filters             map[string]string
		setupMocks          func(mockSTSClient *mocks.MockSTSClient, mockPaginator *mocks.MockEC2InstancesPaginator)
		expectedResourceIDs []string
		expectedError       bool
	}{
		{
			name:    "successful listing with multiple pages",
			filters: map[string]string{"Environment": "Test"},
			setupMocks: func(mockSTSClient *mocks.MockSTSClient, mockPaginator *mocks.MockEC2InstancesPaginator) {
				// Setup STS client mock
				mockSTSClient.On("GetCallerIdentity", mock.Anything, mock.Anything, mock.Anything).Return(
					&sts.GetCallerIdentityOutput{
						Account: aws.String("123456789012"),
					}, nil)

				// Setup paginator mock for multiple pages
				mockPaginator.On("HasMorePages").Return(true).Once()
				mockPaginator.On("HasMorePages").Return(true).Once()
				mockPaginator.On("HasMorePages").Return(false).Once()

				// Page 1
				mockPaginator.On("NextPage", mock.Anything, mock.Anything).Return(
					&ec2.DescribeInstancesOutput{
						Reservations: []types.Reservation{
							{
								Instances: []types.Instance{instance1},
							},
						},
					}, nil).Once()

				// Page 2
				mockPaginator.On("NextPage", mock.Anything, mock.Anything).Return(
					&ec2.DescribeInstancesOutput{
						Reservations: []types.Reservation{
							{
								Instances: []types.Instance{instance2},
							},
						},
					}, nil).Once()
			},
			expectedResourceIDs: []string{"i-12345", "i-67890"},
			expectedError:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mocks
			mockSTSClient := new(mocks.MockSTSClient)
			mockEC2Client := new(mocks.MockEC2Client)
			mockPaginator := new(mocks.MockEC2InstancesPaginator)
			mockLogger := setupMockLogger()

			// Create handler with mocks
			handler := &ec2adapter.EC2Handler{
				STSClient: mockSTSClient,
				PaginatorFactory: func(client ec2adapter.EC2ClientInterface, input *ec2.DescribeInstancesInput) ec2adapter.EC2InstancesPaginator {
					return mockPaginator
				},
				EC2ClientFactory: func(cfg aws.Config) ec2adapter.EC2ClientInterface {
					return mockEC2Client
				},
			}

			// Set up resource channel
			resourceChan := make(chan domain.PlatformResource, 10)

			// Setup mocks for this test case
			tt.setupMocks(mockSTSClient, mockPaginator)

			// Call ListResources
			err := handler.ListResources(context.Background(), cfg, tt.filters, mockLogger, resourceChan)

			// Check results
			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)

				// Get resources from channel
				close(resourceChan)
				var receivedIDs []string
				for resource := range resourceChan {
					receivedIDs = append(receivedIDs, resource.Metadata().ProviderAssignedID)
				}

				assert.ElementsMatch(t, tt.expectedResourceIDs, receivedIDs)
			}
		})
	}
}

// TestEC2HandlerGetResource tests the GetResource method of EC2Handler
func TestEC2HandlerGetResource(t *testing.T) {
	region := "us-west-2"
	accountID := "123456789012"
	cfg := aws.Config{
		Region: region,
	}

	// Define test instances
	instance := types.Instance{
		InstanceId:   aws.String("i-12345"),
		InstanceType: types.InstanceTypeT2Micro,
		Tags: []types.Tag{
			{Key: aws.String("Name"), Value: aws.String("TestInstance")},
			{Key: aws.String("Environment"), Value: aws.String("Test")},
		},
	}

	// Define test cases
	tests := []struct {
		name        string
		instanceID  string
		setupMocks  func(mockEC2Client *mocks.MockEC2Client, mockSTSClient *mocks.MockSTSClient)
		expectedID  string
		expectError bool
	}{
		{
			name:       "successful retrieval",
			instanceID: "i-12345",
			setupMocks: func(mockEC2Client *mocks.MockEC2Client, mockSTSClient *mocks.MockSTSClient) {
				// Setup STS client mock
				mockSTSClient.On("GetCallerIdentity", mock.Anything, mock.Anything, mock.Anything).Return(
					&sts.GetCallerIdentityOutput{
						Account: aws.String(accountID),
					}, nil)

				// Setup EC2 client mock
				mockEC2Client.On("DescribeInstances", mock.Anything, mock.MatchedBy(func(input *ec2.DescribeInstancesInput) bool {
					return len(input.InstanceIds) == 1 && input.InstanceIds[0] == "i-12345"
				}), mock.Anything).Return(
					&ec2.DescribeInstancesOutput{
						Reservations: []types.Reservation{
							{
								Instances: []types.Instance{instance},
							},
						},
					}, nil)
			},
			expectedID:  "i-12345",
			expectError: false,
		},
		{
			name:       "instance not found",
			instanceID: "i-nonexistent",
			setupMocks: func(mockEC2Client *mocks.MockEC2Client, mockSTSClient *mocks.MockSTSClient) {
				// Setup STS client mock
				mockSTSClient.On("GetCallerIdentity", mock.Anything, mock.Anything, mock.Anything).Return(
					&sts.GetCallerIdentityOutput{
						Account: aws.String(accountID),
					}, nil)

				// Setup EC2 client mock to return empty response
				mockEC2Client.On("DescribeInstances", mock.Anything, mock.Anything, mock.Anything).Return(
					&ec2.DescribeInstancesOutput{
						Reservations: []types.Reservation{},
					}, nil)
			},
			expectedID:  "",
			expectError: true,
		},
		{
			name:       "API error",
			instanceID: "i-12345",
			setupMocks: func(mockEC2Client *mocks.MockEC2Client, mockSTSClient *mocks.MockSTSClient) {
				// Setup STS client mock
				mockSTSClient.On("GetCallerIdentity", mock.Anything, mock.Anything, mock.Anything).Return(
					&sts.GetCallerIdentityOutput{
						Account: aws.String(accountID),
					}, nil)

				// Setup EC2 client mock to return an error
				mockEC2Client.On("DescribeInstances", mock.Anything, mock.Anything, mock.Anything).Return(
					nil, errors.New("API error"))
			},
			expectedID:  "",
			expectError: true,
		},
		{
			name:       "authorization error",
			instanceID: "i-12345",
			setupMocks: func(mockEC2Client *mocks.MockEC2Client, mockSTSClient *mocks.MockSTSClient) {
				// Setup STS client mock
				mockSTSClient.On("GetCallerIdentity", mock.Anything, mock.Anything, mock.Anything).Return(
					&sts.GetCallerIdentityOutput{
						Account: aws.String(accountID),
					}, nil)

				// Setup EC2 client mock to return an auth error
				mockEC2Client.On("DescribeInstances", mock.Anything, mock.Anything, mock.Anything).Return(
					nil, fmt.Errorf("UnauthorizedOperation: You are not authorized to perform this operation"))
			},
			expectedID:  "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mocks
			mockSTSClient := new(mocks.MockSTSClient)
			mockEC2Client := new(mocks.MockEC2Client)
			mockLogger := setupMockLogger()

			// Create handler with mocks
			handler := &ec2adapter.EC2Handler{
				STSClient: mockSTSClient,
				EC2ClientFactory: func(cfg aws.Config) ec2adapter.EC2ClientInterface {
					return mockEC2Client
				},
			}

			// Setup mocks
			tt.setupMocks(mockEC2Client, mockSTSClient)

			// Call GetResource
			ctx := context.Background()
			resource, err := handler.GetResource(ctx, cfg, tt.instanceID, mockLogger)

			// Check results
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, resource)
				assert.Equal(t, tt.expectedID, resource.Metadata().ProviderAssignedID)
			}
		})
	}
}
