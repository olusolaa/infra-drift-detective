package ports

import (
	"context"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
)

type Reporter interface {
	Report(ctx context.Context, results []domain.ComparisonResult) error
}
