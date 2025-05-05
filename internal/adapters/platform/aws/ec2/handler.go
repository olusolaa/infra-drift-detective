package ec2

import (
	"context"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	aws_errors "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/errors"

	aws_limiter "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/limiter"
	// Import shared aws interfaces from the 'shared' package
	shared "github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/shared"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
	"golang.org/x/sync/errgroup"
)

type EC2Handler struct {
	stsClient    shared.STSClientInterface // Use shared interface
	accountID    string
	accMu        sync.RWMutex
	awsConfig    aws.Config
	ec2Client    EC2ClientInterface  // Use specific interface
	limiter      shared.RateLimiter  // Use shared interface
	errorHandler shared.ErrorHandler // Use shared interface
	// Paginator factory - keep for now, or abstract if needed later
	paginatorFactory func(EC2ClientInterface, *ec2.DescribeInstancesInput) EC2InstancesPaginator
}

// HandlerOption defines a function signature for configuring the EC2Handler.
type HandlerOption func(*EC2Handler)

// WithSTSClient provides an option to set a custom STS client.
func WithSTSClient(client shared.STSClientInterface) HandlerOption {
	return func(h *EC2Handler) {
		if client != nil {
			h.stsClient = client
		}
	}
}

// WithEC2Client provides an option to set a custom EC2 client.
func WithEC2Client(client EC2ClientInterface) HandlerOption {
	return func(h *EC2Handler) {
		if client != nil {
			h.ec2Client = client
		}
	}
}

// WithRateLimiter provides an option to set a custom rate limiter.
func WithRateLimiter(limiter shared.RateLimiter) HandlerOption {
	return func(h *EC2Handler) {
		if limiter != nil {
			h.limiter = limiter
		}
	}
}

// WithErrorHandler provides an option to set a custom error handler.
func WithErrorHandler(handler shared.ErrorHandler) HandlerOption {
	return func(h *EC2Handler) {
		if handler != nil {
			h.errorHandler = handler
		}
	}
}

// NewHandler creates a new EC2Handler with the given AWS config and optional configurations.
func NewHandler(cfg aws.Config, opts ...HandlerOption) *EC2Handler {
	h := &EC2Handler{
		awsConfig: cfg, // Assign mandatory config
	}

	// Set defaults
	h.stsClient = sts.NewFromConfig(cfg)               // Default STS client
	h.ec2Client = ec2.NewFromConfig(cfg)               // Default EC2 client
	h.limiter = &aws_limiter.DefaultRateLimiter{}      // Default limiter
	h.errorHandler = &aws_errors.DefaultErrorHandler{} // Default error handler
	h.paginatorFactory = defaultPaginatorFactory       // Default paginator factory

	// Apply options, potentially overriding defaults
	for _, opt := range opts {
		opt(h)
	}

	return h
}

// defaultPaginatorFactory is the default implementation for creating DescribeInstancesPaginator.
func defaultPaginatorFactory(client EC2ClientInterface, input *ec2.DescribeInstancesInput) EC2InstancesPaginator {
	ec2Client, ok := client.(*ec2.Client)
	if !ok {
		// This panic is acceptable as it indicates an internal configuration error
		// if the default EC2Client isn't an *ec2.Client.
		panic(fmt.Sprintf("internal error: default EC2 client is not *ec2.Client, got %T", client))
	}
	return ec2.NewDescribeInstancesPaginator(ec2Client, input)
}

func (h *EC2Handler) Kind() domain.ResourceKind {
	return domain.KindComputeInstance
}

func (h *EC2Handler) getAccountID(ctx context.Context, logger ports.Logger) (string, error) {
	h.accMu.RLock()
	if h.accountID != "" {
		accID := h.accountID
		h.accMu.RUnlock()
		return accID, nil
	}
	h.accMu.RUnlock()

	h.accMu.Lock()
	defer h.accMu.Unlock()

	if h.accountID != "" {
		return h.accountID, nil
	}

	logger.Debugf(ctx, "Fetching AWS Account ID")
	if err := h.limiter.Wait(ctx, logger); err != nil {
		return "", h.errorHandler.Handle("Limiter", "Wait", err, ctx)
	}
	input := &sts.GetCallerIdentityInput{}
	output, err := h.stsClient.GetCallerIdentity(ctx, input)
	if err != nil {
		// Use injected error handler
		return "", h.errorHandler.Handle("STS", "GetCallerIdentity", err, ctx)
	}
	if output.Account == nil {
		// Use standard error for internal logic issue
		return "", errors.New(errors.CodePlatformAPIError, "EC2: AWS caller identity response did not contain Account ID")
	}
	h.accountID = aws.ToString(output.Account)
	logger.Debugf(ctx, "Fetched AWS Account ID successfully")
	return h.accountID, nil
}

func (h *EC2Handler) ListResources(
	ctx context.Context,
	cfg aws.Config,
	filters map[string]string,
	logger ports.Logger,
	out chan<- domain.PlatformResource,
) error {
	client := h.ec2Client // Use the injected client
	ec2Filters := BuildEC2Filters(filters)
	input := &ec2.DescribeInstancesInput{Filters: ec2Filters}
	paginator := h.paginatorFactory(client, input) // Use the paginator factory
	accountID, accErr := h.getAccountID(ctx, logger)
	if accErr != nil {
		logger.Warnf(ctx, "Proceeding without AWS Account ID for EC2 ListResources: %v", accErr)
		// Don't return here, allow listing to proceed without account ID if possible
	}

	pageNum := 0
	concurrencyLimit := 10
	sem := make(chan struct{}, concurrencyLimit)
	g, childCtx := errgroup.WithContext(ctx)

	logger.Debugf(ctx, "Starting EC2 instance listing with pagination")

	for paginator.HasMorePages() {
		select {
		case <-childCtx.Done():
			logger.Warnf(childCtx, "Context cancelled during pagination loop")
			return childCtx.Err()
		default:
		}
		currentPageNum := pageNum + 1
		logger.Debugf(childCtx, "Fetching EC2 instances page %d", currentPageNum)

		if err := h.limiter.Wait(childCtx, logger); err != nil {
			return err
		}
		output, err := paginator.NextPage(childCtx)
		if err != nil {
			// Use injected error handler
			return h.errorHandler.Handle("EC2", fmt.Sprintf("DescribeInstances:Page%d", currentPageNum), err, childCtx)
		}
		pageNum = currentPageNum

		for _, reservation := range output.Reservations {
			for _, instance := range reservation.Instances {
				instance := instance
				sem <- struct{}{}
				g.Go(func() error {
					defer func() { <-sem }()
					select {
					case <-childCtx.Done():
						return childCtx.Err()
					default:
						// Note: newEc2InstanceResource now takes the EC2ClientInterface directly
						resource, mapErr := newEc2InstanceResource(
							instance,
							cfg.Region, // Region from config passed to ListResources
							accountID,
							logger,
							client, // Pass the client interface only ONCE
						)
						if mapErr != nil {
							logger.Errorf(childCtx, mapErr, "Failed to create resource wrapper for instance %s, skipping", aws.ToString(instance.InstanceId))
							return nil // Continue processing other instances
						}

						select {
						case out <- resource:
						case <-childCtx.Done():
							logger.Warnf(childCtx, "Context cancelled while sending instance %s", aws.ToString(instance.InstanceId))
							return childCtx.Err()
						}
						return nil
					}
				})
			}
		}
	}

	// Now wait for all goroutines to finish
	err := g.Wait()

	// NOTE: No longer closing channel here, the provider handles that

	if err != nil {
		if err != context.Canceled && err != context.DeadlineExceeded {
			// Use standard error wrapping for internal coordination errors
			return errors.Wrap(err, errors.CodeInternal, "error occurred during concurrent instance processing")
		}
		return err // Return context errors directly
	}

	logger.Debugf(ctx, "Finished EC2 pagination and processing.")
	return nil
}

func (h *EC2Handler) GetResource(ctx context.Context, cfg aws.Config, id string, logger ports.Logger) (domain.PlatformResource, error) {
	client := h.ec2Client // Use injected client
	input := &ec2.DescribeInstancesInput{InstanceIds: []string{id}}

	logger.Debugf(ctx, "Describing single instance %s", id)
	if err := h.limiter.Wait(ctx, logger); err != nil {
		return nil, err
	}

	output, err := client.DescribeInstances(ctx, input)
	if err != nil {
		// Use injected error handler
		return nil, h.errorHandler.Handle("EC2", "DescribeInstances", err, ctx)
	}

	if len(output.Reservations) == 0 || len(output.Reservations[0].Instances) == 0 {
		// Use standard error for not found
		return nil, errors.New(errors.CodeResourceNotFound, fmt.Sprintf("EC2 instance with ID '%s' not found (empty response)", id))
	}
	instance := output.Reservations[0].Instances[0]

	accountID, accErr := h.getAccountID(ctx, logger)
	if accErr != nil {
		logger.Warnf(ctx, "Proceeding without AWS Account ID for EC2 GetResource: %v", accErr)
		// Don't fail here, allow resource building without account ID if possible
	}

	// Note: newEc2InstanceResource now takes the EC2ClientInterface directly
	resource, mapErr := newEc2InstanceResource(
		instance,
		cfg.Region, // Region from config passed to GetResource
		accountID,
		logger,
		client, // Pass the client interface only ONCE
	)
	if mapErr != nil {
		// Use standard error wrapping for internal mapping errors
		return nil, errors.Wrap(mapErr, errors.CodeInternal, fmt.Sprintf("failed to create resource wrapper for instance %s", id))
	}

	logger.Debugf(ctx, "Successfully described instance %s", id)
	return resource, nil
}

type DescribeInstanceAttributeInput = ec2.DescribeInstanceAttributeInput
type DescribeVolumesInput = ec2.DescribeVolumesInput
