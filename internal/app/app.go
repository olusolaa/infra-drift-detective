package app

import (
	"context"

	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
)

// Application represents the main application that runs the drift detection engine
type Application struct {
	Engine ports.DriftAnalysisEngine
	Logger ports.Logger
}

// NewApplication creates a new application instance
func NewApplication(engine ports.DriftAnalysisEngine, logger ports.Logger) *Application {
	return &Application{
		Engine: engine,
		Logger: logger,
	}
}

// Run executes the drift analysis process
func (a *Application) Run(ctx context.Context) error {
	a.Logger.Infof(ctx, "Starting drift analysis...")

	err := a.Engine.Run(ctx)

	if err != nil {
		a.Logger.Errorf(ctx, err, "Drift analysis failed")
		return err
	}

	a.Logger.Infof(ctx, "Drift analysis completed successfully")
	return nil
}
