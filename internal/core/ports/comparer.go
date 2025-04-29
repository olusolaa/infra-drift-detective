package ports

import (
	"context"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
)

type ResourceComparer interface {
	Kind() domain.ResourceKind
	Compare(ctx context.Context, desired domain.StateResource, actual domain.PlatformResource, attributesToCheck []string) ([]domain.AttributeDiff, error)
}
