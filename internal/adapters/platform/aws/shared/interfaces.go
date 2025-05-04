package shared

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
)

//go:generate mockery --name RateLimiter --output ./mocks --outpkg mocks --case underscore
//go:generate mockery --name ErrorHandler --output ./mocks --outpkg mocks --case underscore
//go:generate mockery --name STSClientInterface --output ./mocks --outpkg mocks --case underscore
//go:generate mockery --name Logger --output ./mocks --outpkg mocks --case underscore
//go:generate mockery --name PlatformResource --output ./mocks --outpkg mocks --case underscore

// RateLimiter defines an interface for rate-limiting AWS API calls.
type RateLimiter interface {
	// Wait blocks until the rate limit allows proceeding, or returns an error.
	// It requires a Logger for potential warnings/errors during the wait.
	Wait(ctx context.Context, logger ports.Logger) error
}

// ErrorHandler defines an interface for handling errors from AWS API calls.
type ErrorHandler interface {
	// Handle processes an error, potentially wrapping or transforming it.
	// Service and operation provide context about where the error occurred.
	Handle(service, operation string, err error, ctx context.Context) error
}

// STSClientInterface defines the method needed from the AWS SDK STS client.
type STSClientInterface interface {
	GetCallerIdentity(ctx context.Context, params *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}
