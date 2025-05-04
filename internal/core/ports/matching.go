package ports

import (
	"context"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
)

type MatchedPair struct {
	Desired domain.StateResource
	Actual  domain.PlatformResource
}

type MatchingResult struct {
	Matched          []MatchedPair
	UnmatchedDesired []domain.StateResource    // Only in state
	UnmatchedActual  []domain.PlatformResource // Only on platform (unmanaged)
}

//go:generate mockery --name=Matcher --output=./mocks --outpkg=mocks --case underscore
type Matcher interface {
	Match(ctx context.Context, desired []domain.StateResource, actual []domain.PlatformResource) (MatchingResult, error)
}
