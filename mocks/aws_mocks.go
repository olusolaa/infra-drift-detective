package mocks

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/mock"

	ports "github.com/olusolaa/infra-drift-detector/internal/core/ports"
)

// MockSTSClient is a mock implementation of the STS client
type MockSTSClient struct {
	mock.Mock
}

func (m *MockSTSClient) GetCallerIdentity(ctx context.Context, params *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error) {
	args := m.Called(ctx, params, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*sts.GetCallerIdentityOutput), args.Error(1)
}

// MockEC2Client is a mock implementation of the EC2 client
type MockEC2Client struct {
	mock.Mock
}

func (m *MockEC2Client) DescribeInstances(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	args := m.Called(ctx, params, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ec2.DescribeInstancesOutput), args.Error(1)
}

// MockEC2InstanceAttributeClient is a mock implementation of the EC2 client for instance attributes
type MockEC2InstanceAttributeClient struct {
	mock.Mock
}

func (m *MockEC2InstanceAttributeClient) DescribeInstanceAttribute(ctx context.Context, params *ec2.DescribeInstanceAttributeInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstanceAttributeOutput, error) {
	args := m.Called(ctx, params, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ec2.DescribeInstanceAttributeOutput), args.Error(1)
}

// MockEC2VolumeClient is a mock implementation of the EC2 client for volumes
type MockEC2VolumeClient struct {
	mock.Mock
}

func (m *MockEC2VolumeClient) DescribeVolumes(ctx context.Context, params *ec2.DescribeVolumesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeVolumesOutput, error) {
	args := m.Called(ctx, params, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ec2.DescribeVolumesOutput), args.Error(1)
}

// MockEC2InstancesPaginator is a mock implementation of the EC2 instances paginator
type MockEC2InstancesPaginator struct {
	mock.Mock
	MaxPages int
	curPage  int
}

func (m *MockEC2InstancesPaginator) HasMorePages() bool {
	if args := m.Called(); args.Get(0) != nil {
		return args.Bool(0)
	}

	// Fallback behavior if no explicit expectation
	m.curPage++
	return m.curPage <= m.MaxPages
}

func (m *MockEC2InstancesPaginator) NextPage(ctx context.Context, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	args := m.Called(ctx, optFns)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*ec2.DescribeInstancesOutput), args.Error(1)
}

// MockLogger is a mock implementation of the Logger interface
type MockLogger struct {
	mock.Mock
}

func (m *MockLogger) Debug(ctx context.Context, msg string) {
	m.Called(ctx, msg)
}

func (m *MockLogger) Info(ctx context.Context, msg string) {
	m.Called(ctx, msg)
}

func (m *MockLogger) Warn(ctx context.Context, msg string) {
	m.Called(ctx, msg)
}

func (m *MockLogger) Error(ctx context.Context, err error, msg string) {
	m.Called(ctx, err, msg)
}

func (m *MockLogger) Debugf(ctx context.Context, format string, args ...interface{}) {
	varArgs := []interface{}{ctx, format}
	for _, arg := range args {
		varArgs = append(varArgs, arg)
	}
	m.Called(varArgs...)
}

func (m *MockLogger) Infof(ctx context.Context, format string, args ...interface{}) {
	varArgs := []interface{}{ctx, format}
	for _, arg := range args {
		varArgs = append(varArgs, arg)
	}
	m.Called(varArgs...)
}

func (m *MockLogger) Warnf(ctx context.Context, format string, args ...interface{}) {
	varArgs := []interface{}{ctx, format}
	for _, arg := range args {
		varArgs = append(varArgs, arg)
	}
	m.Called(varArgs...)
}

func (m *MockLogger) Errorf(ctx context.Context, err error, format string, args ...interface{}) {
	varArgs := []interface{}{ctx, err, format}
	for _, arg := range args {
		varArgs = append(varArgs, arg)
	}
	m.Called(varArgs...)
}

func (m *MockLogger) WithFields(fields map[string]interface{}) ports.Logger {
	args := m.Called(fields)
	return args.Get(0).(ports.Logger)
}

// Helper function to reset all mocks
func ResetAllMocks(mocks ...interface{}) {
	for _, m := range mocks {
		if mockObj, ok := m.(interface{ AssertExpectations(t mock.TestingT) bool }); ok {
			switch mockTyped := mockObj.(type) {
			case *MockSTSClient:
				mockTyped.ExpectedCalls = nil
				mockTyped.Calls = nil
			case *MockEC2Client:
				mockTyped.ExpectedCalls = nil
				mockTyped.Calls = nil
			case *MockEC2InstancesPaginator:
				mockTyped.ExpectedCalls = nil
				mockTyped.Calls = nil
				mockTyped.curPage = 0
			case *MockLogger:
				mockTyped.ExpectedCalls = nil
				mockTyped.Calls = nil
			case *MockEC2InstanceAttributeClient:
				mockTyped.ExpectedCalls = nil
				mockTyped.Calls = nil
			case *MockEC2VolumeClient:
				mockTyped.ExpectedCalls = nil
				mockTyped.Calls = nil
			}
		}
	}
}
