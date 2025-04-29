package aws

import (
	"context"
	"fmt"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/types"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"

	"github.com/olusolaa/infra-drift-detector/internal/adapters/platform/aws/ec2"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
	"golang.org/x/sync/errgroup" // Import errgroup for managing handler routines
)

type Provider struct {
	awsConfig aws.Config
	handlers  map[domain.ResourceKind]AWSResourceHandler
	logger    ports.Logger // Inject logger into provider
}

// NewProvider needs logger
func NewProvider(ctx context.Context, logger ports.Logger) (*Provider, error) {
	if logger == nil {
		// Fallback or error? Let's error - logger is essential.
		return nil, errors.New(errors.CodeConfigValidation, "logger cannot be nil for AWS Provider")
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeConfigValidation, "failed to load default AWS config")
	}

	p := &Provider{
		awsConfig: cfg,
		handlers:  make(map[domain.ResourceKind]AWSResourceHandler),
		logger:    logger, // Store logger
	}

	ec2Handler := ec2.NewHandler(cfg)
	p.registerHandler(ec2Handler)
	// Register other handlers...

	if len(p.handlers) == 0 {
		return nil, errors.New(errors.CodeInternal, "no AWS resource handlers registered")
	}
	return p, nil
}

// registerHandler remains the same
func (p *Provider) registerHandler(handler AWSResourceHandler) {
	// ... implementation ... // (Same as before)
	if handler != nil {
		kind := handler.Kind()
		p.handlers[kind] = handler
	}
}

// Type remains the same
func (p *Provider) Type() string {
	return types.ProviderTypeAWS
}

// ListResources now orchestrates calling handlers concurrently and streams results.
// IMPORTANT: This method now *blocks* until all handlers for the requested kinds finish
// and it pushes all results onto the 'out' channel passed from the engine.
func (p *Provider) ListResources(
	ctx context.Context,
	requestedKinds []domain.ResourceKind, // Accept list of kinds engine wants
	filters map[string]string, // Filters might need to be kind-specific in future
	out chan<- domain.PlatformResource, // Engine provides the output channel
) error {
	g, childCtx := errgroup.WithContext(ctx)

	foundHandler := false
	for _, kind := range requestedKinds {
		handler, found := p.handlers[kind]
		if !found {
			p.logger.Warnf(childCtx, "Resource kind '%s' not supported by AWS provider, skipping", kind)
			continue // Skip unsupported kinds
		}
		foundHandler = true

		// Create local copy of kind for the goroutine closure
		currentKind := kind
		currentHandler := handler

		g.Go(func() error {
			// Pass provider's logger down to the handler
			handlerLogger := p.logger.WithFields(map[string]any{"resource_kind": currentKind})
			handlerLogger.Debugf(childCtx, "Starting ListResources for handler")
			// The handler now sends directly to the 'out' channel
			err := currentHandler.ListResources(childCtx, p.awsConfig, filters, handlerLogger, out)
			if err != nil {
				// Log the error from the handler
				handlerLogger.Errorf(childCtx, err, "Handler failed")
				// Propagate the error to the errgroup
				return err // Return the actual error, not wrapped again here
			}
			handlerLogger.Debugf(childCtx, "Finished ListResources for handler")
			return nil
		})
	}

	if !foundHandler && len(requestedKinds) > 0 {
		return errors.New(errors.CodeNotImplemented, "no supported resource kinds found for AWS provider among requested kinds")
	}

	// Wait for all handler Goroutines to complete.
	if err := g.Wait(); err != nil {
		// Error already logged by the handler/goroutine, just return it
		// Avoid wrapping context errors again
		if err == context.Canceled || err == context.DeadlineExceeded {
			p.logger.Warnf(ctx, "AWS ListResources cancelled or timed out.")
		} else {
			p.logger.Errorf(ctx, err, "Error occurred during AWS ListResources execution")
		}
		return err // Return the first error encountered by any handler
	}

	p.logger.Debugf(ctx, "All AWS resource handlers finished successfully.")
	return nil // Overall success if no handler returned an error
}

// GetResource delegates to the appropriate handler, passing the logger.
func (p *Provider) GetResource(ctx context.Context, kind domain.ResourceKind, id string) (domain.PlatformResource, error) {
	handler, found := p.handlers[kind]
	if !found {
		return nil, errors.New(errors.CodeNotImplemented, fmt.Sprintf("resource kind '%s' not supported by AWS provider", kind))
	}

	// Pass logger down to the handler's GetResource method
	handlerLogger := p.logger.WithFields(map[string]any{"resource_kind": kind, "resource_id": id})
	return handler.GetResource(ctx, p.awsConfig, id, handlerLogger)
}
