package ports

import (
	"context"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
)

//go:generate mockery --name=PlatformProvider --output=./mocks --outpkg=mocks --case underscore
type PlatformProvider interface {
	Type() string
	ListResources(ctx context.Context, requestedKinds []domain.ResourceKind, filters map[string]string, out chan<- domain.PlatformResource) error
	GetResource(ctx context.Context, kind domain.ResourceKind, id string) (domain.PlatformResource, error)
}

//go:generate mockery --name=StateProvider --output=./mocks --outpkg=mocks --case underscore
type StateProvider interface {
	Type() string
	ListResources(ctx context.Context, kind domain.ResourceKind) ([]domain.StateResource, error)
	GetResource(ctx context.Context, kind domain.ResourceKind, identifier string) (domain.StateResource, error)
}
