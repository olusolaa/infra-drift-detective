package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports" // Import ports for Logger
)

type AWSResourceHandler interface {
	Kind() domain.ResourceKind
	// ListResources now sends results to a channel instead of returning a slice.
	// It also accepts a logger for internal logging.
	ListResources(
		ctx context.Context,
		cfg aws.Config,
		filters map[string]string,
		logger ports.Logger,
		out chan<- domain.PlatformResource, // Channel to send results
	) error // Return error for fatal issues during listing/pagination
	GetResource(ctx context.Context, cfg aws.Config, id string, logger ports.Logger) (domain.PlatformResource, error)
}
