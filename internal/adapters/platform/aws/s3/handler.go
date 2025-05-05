// s3/handler.go

package s3

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	aws_errors "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/errors"
	aws_limiter "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/limiter"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/shared"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	idderrors "github.com/olusolaa/infra-drift-detector/internal/errors"
)

type S3Handler struct {
	stsClient    shared.STSClientInterface
	accountID    string
	accMu        sync.RWMutex
	awsConfig    aws.Config
	s3Client     S3ClientInterface
	builder      S3ResourceBuilder
	limiter      shared.RateLimiter
	errorHandler shared.ErrorHandler
}

// HandlerOption defines a function signature for configuring the S3Handler.
type HandlerOption func(*S3Handler)

// WithSTSClient provides an option to set a custom STS client.
func WithSTSClient(client shared.STSClientInterface) HandlerOption {
	return func(h *S3Handler) {
		if client != nil {
			h.stsClient = client
		}
	}
}

// WithS3Client provides an option to set a custom S3 client.
func WithS3Client(client S3ClientInterface) HandlerOption {
	return func(h *S3Handler) {
		if client != nil {
			h.s3Client = client
		}
	}
}

// WithS3Builder provides an option to set a custom S3 resource builder.
func WithS3Builder(builder S3ResourceBuilder) HandlerOption {
	return func(h *S3Handler) {
		if builder != nil {
			h.builder = builder
		}
	}
}

// WithRateLimiter provides an option to set a custom rate limiter.
func WithRateLimiter(limiter shared.RateLimiter) HandlerOption {
	return func(h *S3Handler) {
		if limiter != nil {
			h.limiter = limiter
		}
	}
}

// WithErrorHandler provides an option to set a custom error handler.
func WithErrorHandler(handler shared.ErrorHandler) HandlerOption {
	return func(h *S3Handler) {
		if handler != nil {
			h.errorHandler = handler
		}
	}
}

// NewHandler creates a new S3Handler with the given AWS config and optional configurations.
func NewHandler(cfg aws.Config, opts ...HandlerOption) *S3Handler {
	s3Factory := func(c aws.Config) S3ClientInterface {
		return s3.NewFromConfig(c)
	}

	h := &S3Handler{
		awsConfig: cfg,
	}

	h.stsClient = sts.NewFromConfig(cfg)
	h.s3Client = s3.NewFromConfig(cfg)
	h.builder = NewDefaultS3ResourceBuilder(s3Factory)
	h.limiter = &aws_limiter.DefaultRateLimiter{}
	h.errorHandler = &aws_errors.DefaultErrorHandler{}

	for _, opt := range opts {
		opt(h)
	}

	return h
}

func (h *S3Handler) Kind() domain.ResourceKind { return domain.KindStorageBucket }

func (h *S3Handler) getAccountID(ctx context.Context, logger ports.Logger) (string, error) {
	h.accMu.RLock()
	acc := h.accountID
	h.accMu.RUnlock()
	if acc != "" {
		return acc, nil
	}

	h.accMu.Lock()
	defer h.accMu.Unlock()
	if h.accountID != "" {
		return h.accountID, nil
	}

	if err := h.limiter.Wait(ctx, logger); err != nil {
		return "", err
	}
	out, err := h.stsClient.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		// Use injected error handler
		return "", h.errorHandler.Handle("STS", "GetCallerIdentity", err, ctx)
	}
	if out.Account == nil {
		return "", idderrors.New(idderrors.CodePlatformAPIError, "S3: AWS caller identity response did not contain Account ID")
	}
	h.accountID = aws.ToString(out.Account)
	return h.accountID, nil
}

func (h *S3Handler) ListResources(
	ctx context.Context,
	cfg aws.Config,
	filters map[string]string,
	logger ports.Logger,
	out chan<- domain.PlatformResource,
) error {
	client := h.s3Client

	accountID, accountErr := h.getAccountID(ctx, logger)
	if accountErr != nil {
		logger.Warnf(ctx, "Failed to get account ID for listing S3 buckets: %v", accountErr)
	}

	if err := h.limiter.Wait(ctx, logger); err != nil {
		return err
	}

	listOutput, err := client.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return h.errorHandler.Handle("S3", "ListBuckets", err, ctx)
	}

	for _, bucket := range listOutput.Buckets {
		select {
		case <-ctx.Done():
			logger.Warnf(ctx, "Context cancelled during S3 bucket processing")
			return ctx.Err()
		default:
			// Continue processing
		}

		bucketName := aws.ToString(bucket.Name)
		if bucketName == "" {
			continue
		}

		res, buildErr := h.builder.Build(ctx, bucketName, accountID, cfg, logger)
		if buildErr != nil {
			logger.Warnf(ctx, "Error building S3 resource for bucket %s: %v", bucketName, buildErr)
			continue
		}

		select {
		case out <- res:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

func (h *S3Handler) GetResource(ctx context.Context, cfg aws.Config, id string, logger ports.Logger) (domain.PlatformResource, error) {
	bucketName := id
	client := h.s3Client

	if err := h.limiter.Wait(ctx, logger); err != nil {
		return nil, err
	}

	_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucketName)})
	if err != nil {
		var respErr *awshttp.ResponseError
		if errors.As(err, &respErr) && (respErr.HTTPStatusCode() == 403 || respErr.HTTPStatusCode() == 301) {
			logger.Warnf(ctx, "HeadBucket returned %d status, attempting to continue with build", respErr.HTTPStatusCode())
		} else {
			return nil, h.errorHandler.Handle("S3", "HeadBucket", err, ctx)
		}
	}

	accountID, accountErr := h.getAccountID(ctx, logger)
	if accountErr != nil {
		logger.Warnf(ctx, "Failed to get account ID needed for S3 GetResource: %v", accountErr)
		return nil, fmt.Errorf("failed to get AWS account ID: %w", accountErr)
	}

	resource, buildErr := h.builder.Build(ctx, bucketName, accountID, cfg, logger)
	if buildErr != nil {
		return nil, buildErr
	}

	return resource, nil
}
