package ports

import (
	"context"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
)

//go:generate mockery --name=ResourceComparer --output=./mocks --outpkg=mocks --case underscore

type ResourceComparer interface {
	Kind() domain.ResourceKind
	Compare(ctx context.Context, desired domain.StateResource, actual domain.PlatformResource, attributesToCheck []string) ([]domain.AttributeDiff, error)
}
