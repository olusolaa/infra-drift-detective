package ports

import "context"

type DriftAnalysisEngine interface {
	Run(ctx context.Context) error
}
