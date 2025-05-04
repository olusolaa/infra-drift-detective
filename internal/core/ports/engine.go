package ports

import "context"

//go:generate mockery --name DriftAnalysisEngine --output ./mocks --outpkg mocks --case underscore
type DriftAnalysisEngine interface {
	Run(ctx context.Context) error
}
