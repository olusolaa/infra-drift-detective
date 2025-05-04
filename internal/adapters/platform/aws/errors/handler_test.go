package errors

import (
	"context"
	"fmt"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
	"github.com/stretchr/testify/assert"
)

// Mock implementation of smithy.APIError for testing
type mockAPIError struct {
	errorCode string
	errorMsg  string
}

func (m *mockAPIError) Error() string {
	return m.errorMsg
}

func (m *mockAPIError) ErrorCode() string {
	return m.errorCode
}

func (m *mockAPIError) ErrorMessage() string {
	return m.errorMsg
}

func (m *mockAPIError) ErrorFault() smithy.ErrorFault {
	return smithy.FaultUnknown
}

// MockErrorWithCode implements the interface{ ErrorCode() string } for testing
type MockErrorWithCode struct {
	Code    string
	Message string
}

func (m *MockErrorWithCode) Error() string {
	return m.Message
}

func (m *MockErrorWithCode) ErrorCode() string {
	return m.Code
}

func TestHandleAWSError(t *testing.T) {
	tests := []struct {
		name         string
		resourceType string
		resourceID   string
		err          error
		ctx          context.Context
		expectedCode errors.Code
	}{
		{
			name:         "nil error",
			resourceType: "test-resource",
			resourceID:   "test-id",
			err:          nil,
			ctx:          context.Background(),
			expectedCode: errors.CodeInternal,
		},
		{
			name:         "context canceled",
			resourceType: "test-resource",
			resourceID:   "test-id",
			err:          fmt.Errorf("some error"),
			ctx:          canceledContext(),
			expectedCode: errors.CodePlatformAPIError,
		},
		{
			name:         "direct context canceled",
			resourceType: "test-resource",
			resourceID:   "test-id",
			err:          context.Canceled,
			ctx:          context.Background(),
			expectedCode: errors.CodePlatformAPIError,
		},
		{
			name:         "direct context deadline exceeded",
			resourceType: "test-resource",
			resourceID:   "test-id",
			err:          context.DeadlineExceeded,
			ctx:          context.Background(),
			expectedCode: errors.CodePlatformAPIError,
		},
		{
			name:         "auth failure",
			resourceType: "test-resource",
			resourceID:   "test-id",
			err:          fmt.Errorf("AuthFailure: some auth error"),
			ctx:          context.Background(),
			expectedCode: errors.CodePlatformAuthError,
		},
		{
			name:         "unauthorized operation",
			resourceType: "test-resource",
			resourceID:   "test-id",
			err:          fmt.Errorf("UnauthorizedOperation: not allowed"),
			ctx:          context.Background(),
			expectedCode: errors.CodePlatformAuthError,
		},
		{
			name:         "access denied",
			resourceType: "test-resource",
			resourceID:   "test-id",
			err:          fmt.Errorf("AccessDenied: access denied"),
			ctx:          context.Background(),
			expectedCode: errors.CodePlatformAuthError,
		},
		{
			name:         "resource not found by string",
			resourceType: "test-resource",
			resourceID:   "test-id",
			err:          fmt.Errorf("NotFound: resource not found"),
			ctx:          context.Background(),
			expectedCode: errors.CodeResourceNotFound,
		},
		{
			name:         "resource not found by API error",
			resourceType: "test-resource",
			resourceID:   "test-id",
			err:          &mockAPIError{errorCode: "ResourceNotFoundException", errorMsg: "not found"},
			ctx:          context.Background(),
			expectedCode: errors.CodeResourceNotFound,
		},
		{
			name:         "generic error",
			resourceType: "test-resource",
			resourceID:   "test-id",
			err:          fmt.Errorf("some other error"),
			ctx:          context.Background(),
			expectedCode: errors.CodePlatformAPIError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := HandleAWSError(tt.resourceType, tt.resourceID, tt.err, tt.ctx)
			
			// Check if result is the expected error type
			appErr, ok := result.(*errors.AppError)
			assert.True(t, ok, "Expected an *errors.AppError")
			assert.Equal(t, tt.expectedCode, appErr.Code, "Error code doesn't match expected")
		})
	}
}

func TestIsNotFoundError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		errMsg   string
		expected bool
	}{
		{
			name:     "NotFound in message",
			err:      fmt.Errorf("NotFound: resource not found"),
			errMsg:   "NotFound: resource not found",
			expected: true,
		},
		{
			name:     "not found lowercase in message",
			err:      fmt.Errorf("resource not found"),
			errMsg:   "resource not found",
			expected: true,
		},
		{
			name:     "not exist in message",
			err:      fmt.Errorf("resource does not exist"),
			errMsg:   "resource does not exist",
			expected: true,
		},
		{
			name:     "NoSuchKey in message",
			err:      fmt.Errorf("NoSuchKey: the object doesn't exist"),
			errMsg:   "NoSuchKey: the object doesn't exist",
			expected: true,
		},
		{
			name:     "NoSuchBucket in message",
			err:      fmt.Errorf("NoSuchBucket: the bucket doesn't exist"),
			errMsg:   "NoSuchBucket: the bucket doesn't exist",
			expected: true,
		},
		{
			name:     "API error with not found code",
			err:      &mockAPIError{errorCode: "NoSuchBucket", errorMsg: "Some error message"},
			errMsg:   "Some error message",
			expected: true,
		},
		{
			name:     "Mock error with not found code",
			err:      &MockErrorWithCode{Code: "ResourceNotFoundException", Message: "Not found"},
			errMsg:   "Not found",
			expected: true,
		},
		{
			name:     "generic error",
			err:      fmt.Errorf("some other error"),
			errMsg:   "some other error",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isNotFoundError(tt.err, tt.errMsg)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsNotFoundErrorCode(t *testing.T) {
	testCases := []struct {
		code     string
		expected bool
	}{
		{"InvalidInstanceID.NotFound", true},
		{"InvalidInstanceID.Malformed", true},
		{"NoSuchBucket", true},
		{"NoSuchKey", true},
		{"ResourceNotFoundException", true},
		{"EntityNotFoundException", true},
		{"NotFoundException", true},
		{"SomeRandomError", false},
		{"", false},
	}

	for _, tc := range testCases {
		t.Run(tc.code, func(t *testing.T) {
			result := isNotFoundErrorCode(tc.code)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestDefaultErrorHandler_Handle(t *testing.T) {
	handler := &DefaultErrorHandler{}
	err := handler.Handle("TestService", "TestOperation", fmt.Errorf("test error"), context.Background())
	
	// Check that the handler returned an error
	assert.Error(t, err)
	
	// Check that the error is the expected type
	appErr, ok := err.(*errors.AppError)
	assert.True(t, ok, "Expected an *errors.AppError")
	
	// Should be a platform API error as it's not a specific case
	assert.Equal(t, errors.CodePlatformAPIError, appErr.Code)
}

// Helper function to create a canceled context
func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
