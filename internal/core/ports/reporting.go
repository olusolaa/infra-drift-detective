package ports

import (
	"context"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
)

//go:generate mockery --name=Reporter --output=./mocks --outpkg=mocks --case underscore
type Reporter interface {
	Report(ctx context.Context, results []domain.ComparisonResult) error
}
