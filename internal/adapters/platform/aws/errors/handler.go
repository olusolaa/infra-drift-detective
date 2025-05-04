package errors

import (
	"context"
	stderrs "errors"
	"fmt"
	"strings"

	"github.com/aws/smithy-go"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

// HandleAWSError provides a unified way to handle AWS errors and map them to application error codes
// resourceType: the AWS resource type (e.g. "EC2 instance", "S3 bucket")
// resourceID: the identifier for the resource
// err: the original AWS error
// ctx: the context, to check for cancellation
func HandleAWSError(resourceType string, resourceID string, err error, ctx context.Context) error {
	// Safety check
	if err == nil {
		return errors.New(errors.CodeInternal, fmt.Sprintf("unexpected nil error in AWS error handler for %s", resourceType))
	}

	// Handle context cancellation first for fast returns
	if ctx.Err() != nil {
		return errors.Wrap(ctx.Err(), errors.CodePlatformAPIError,
			fmt.Sprintf("context canceled during AWS %s API call", resourceType))
	}

	// Direct equality checks for common error types
	if err == context.Canceled || err == context.DeadlineExceeded {
		return errors.Wrap(err, errors.CodePlatformAPIError,
			fmt.Sprintf("context canceled during AWS %s API call", resourceType))
	}

	// Get error message for string matching
	errMsg := err.Error()

	// Authentication/authorization errors
	if strings.Contains(errMsg, "AuthFailure") ||
		strings.Contains(errMsg, "UnauthorizedOperation") ||
		strings.Contains(errMsg, "AccessDenied") {
		return errors.Wrap(err, errors.CodePlatformAuthError,
			fmt.Sprintf("AWS authentication error accessing %s %s", resourceType, resourceID))
	}

	// Resource not found errors - use both error types and message content
	if isNotFoundError(err, errMsg) {
		return errors.Wrap(err, errors.CodeResourceNotFound,
			fmt.Sprintf("%s '%s' not found", resourceType, resourceID))
	}

	// Fall back to generic platform API error
	return errors.Wrap(err, errors.CodePlatformAPIError,
		fmt.Sprintf("failed to access %s '%s'", resourceType, resourceID))
}

// isNotFoundError checks if the error indicates a resource was not found
func isNotFoundError(err error, errMsg string) bool {
	// Check common not found strings in error messages
	if strings.Contains(errMsg, "NotFound") ||
		strings.Contains(errMsg, "not found") ||
		strings.Contains(errMsg, "not exist") ||
		strings.Contains(errMsg, "NoSuchKey") ||
		strings.Contains(errMsg, "NoSuchBucket") {
		return true
	}

	// Try to extract error code using AWS API error interface
	var notFoundCode bool

	// First try using type assertion for mock tests
	if mockErr, ok := err.(interface{ ErrorCode() string }); ok && mockErr != nil {
		code := mockErr.ErrorCode()
		return isNotFoundErrorCode(code)
	}

	// Then try using the smithy.APIError interface
	if !notFoundCode {
		var apiErr smithy.APIError
		if stderrs.As(err, &apiErr) && apiErr != nil {
			code := apiErr.ErrorCode()
			return isNotFoundErrorCode(code)
		}
	}

	return false
}

// isNotFoundErrorCode checks if an error code indicates a resource was not found
func isNotFoundErrorCode(code string) bool {
	notFoundCodes := []string{
		// EC2
		"InvalidInstanceID.NotFound",
		"InvalidInstanceID.Malformed",

		// S3
		"NoSuchBucket",
		"NoSuchKey",

		// Generic
		"ResourceNotFoundException",
		"EntityNotFoundException",
		"NotFoundException",
	}

	for _, nfCode := range notFoundCodes {
		if code == nfCode {
			return true
		}
	}

	return false
}

// DefaultErrorHandler implements the shared aws.ErrorHandler interface.
type DefaultErrorHandler struct{}

// Handle calls the package-level HandleAWSError function.
func (d *DefaultErrorHandler) Handle(service, operation string, err error, ctx context.Context) error {
	// Note: The default HandleAWSError uses a combined resourceType and resourceID string.
	// We might need to refine this if more granular info (service, operation) is needed.
	// For now, we'll pass a generic string or combine service/operation.
	resourceType := service                                   // Or fmt.Sprintf("%s:%s", service, operation)
	resourceID := operation                                   // Or "API call"
	return HandleAWSError(resourceType, resourceID, err, ctx) // Calls the existing HandleAWSError function
}
