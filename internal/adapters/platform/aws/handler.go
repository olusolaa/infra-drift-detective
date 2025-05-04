package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
)

type AWSResourceHandler interface {
	Kind() domain.ResourceKind
	ListResources(
		ctx context.Context,
		cfg aws.Config,
		filters map[string]string,
		logger ports.Logger,
		out chan<- domain.PlatformResource,
	) error
	GetResource(
		ctx context.Context,
		cfg aws.Config,
		id string,
		logger ports.Logger,
	) (domain.PlatformResource, error)
}
