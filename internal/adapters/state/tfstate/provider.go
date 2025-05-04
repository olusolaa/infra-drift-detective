package tfstate

import (
	"context"
	"fmt"

	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

const ProviderTypeTFState = "tfstate"

type Provider struct {
	parser *stateParser
	logger ports.Logger
}

type Config struct {
	FilePath string `yaml:"path" validate:"required,file"`
}

func NewProvider(cfg Config, logger ports.Logger) (*Provider, error) {
	if cfg.FilePath == "" {
		return nil, errors.New(errors.CodeConfigValidation,
			"terraform‑state provider requires a non‑empty file path")
	}

	plog := logger.WithFields(map[string]any{
		"provider":   ProviderTypeTFState,
		"state_file": cfg.FilePath,
	})

	return &Provider{
		parser: newStateParser(cfg.FilePath, plog),
		logger: plog,
	}, nil
}

func (p *Provider) Type() string { return ProviderTypeTFState }

// ──────────────────────────────────────────────────────────────────────────────
// ListResources – returns *every instance* of the requested kind.
// ──────────────────────────────────────────────────────────────────────────────
func (p *Provider) ListResources(
	ctx context.Context,
	kind domain.ResourceKind,
) ([]domain.StateResource, error) {

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	st, err := p.parser.parseAndCache(ctx)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeStateReadError,
			"failed to parse state file for listing")
	}

	rawRes, err := findResourcesInState(st, kind, p.logger)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal,
			"failed to filter resources in state")
	}

	out := make([]domain.StateResource, 0, len(rawRes))
	for _, r := range rawRes {
		// every *instance* becomes one domain.StateResource
		for idx := range r.Instances {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}

			inst := &r.Instances[idx]
			mapped, mapErr := mapRawInstanceToDomain(r, inst, p.logger)
			if mapErr != nil {
				addr := fmt.Sprintf("%s.%s", r.Type, r.Name)
				p.logger.Errorf(ctx, mapErr,
					"skipping resource %s instance %d", addr, idx)
				continue
			}
			out = append(out, mapped)
		}
	}
	return out, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// GetResource – first instance matching "type.name" (e.g. aws_s3_bucket.logs).
// ──────────────────────────────────────────────────────────────────────────────
func (p *Provider) GetResource(
	ctx context.Context,
	kind domain.ResourceKind,
	identifier string,
) (domain.StateResource, error) {

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	st, err := p.parser.parseAndCache(ctx)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeStateReadError,
			fmt.Sprintf("failed to parse state file for %s", identifier))
	}

	res, err := findSpecificResource(st, kind, identifier, p.logger)
	if err != nil {
		return nil, err // already typed (CodeResourceNotFound etc.)
	}

	if len(res.Instances) == 0 {
		return nil, errors.New(errors.CodeResourceNotFound,
			fmt.Sprintf("resource %s has no instances", identifier))
	}

	mapped, mapErr := mapRawInstanceToDomain(res, &res.Instances[0], p.logger)
	if mapErr != nil {
		return nil, errors.Wrap(mapErr, errors.CodeMappingError,
			fmt.Sprintf("failed to map resource %s", identifier))
	}
	return mapped, nil
}
