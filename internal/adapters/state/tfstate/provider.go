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
	FilePath string `yaml:"path" mapstructure:"path" validate:"required"`
}

func NewProvider(cfg Config, logger ports.Logger) (*Provider, error) {
	filePath := cfg.FilePath
	if filePath == "" {
		filePath = "terraform.tfstate"
		logger.Debugf(nil, "No state file path specified, using default: %s", filePath)
	}

	plog := logger.WithFields(map[string]any{
		"provider":   ProviderTypeTFState,
		"state_file": filePath,
	})

	return &Provider{
		parser: newStateParser(filePath, plog),
		logger: plog,
	}, nil
}

func (p *Provider) Type() string { return ProviderTypeTFState }

func (p *Provider) ListResources(
	ctx context.Context,
	kind domain.ResourceKind,
) ([]domain.StateResource, error) {

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	state, err := p.parser.parseAndCache(ctx)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeStateProviderError,
			"parsing terraform state file for ListResources")
	}

	results := make([]domain.StateResource, 0)

	resources, err := findResourcesInState(state, kind, p.logger)
	if err != nil {
		return nil, err
	}

	for _, tfRes := range resources {
		for _, inst := range tfRes.Instances {
			mappedResource, err := mapRawInstanceToDomain(tfRes, &inst, p.logger, state)
			if err != nil {
				return nil, errors.Wrap(err, errors.CodeStateProviderError,
					fmt.Sprintf("parsing resource %s [%s.%s]",
						tfRes.Type, tfRes.Mode, tfRes.Name))
			}
			results = append(results, mappedResource)
		}
	}

	return results, nil
}

func (p *Provider) GetResource(
	ctx context.Context,
	kind domain.ResourceKind,
	identifier string,
) (domain.StateResource, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	state, err := p.parser.parseAndCache(ctx)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeStateProviderError,
			"parsing terraform state file for GetResource")
	}

	res, err := findSpecificResource(state, kind, identifier, p.logger)
	if err != nil {
		return nil, err
	}

	if len(res.Instances) == 0 {
		return nil, errors.New(errors.CodeResourceNotFound,
			fmt.Sprintf("resource '%s.%s' has no instances", res.Type, res.Name))
	}

	return mapRawInstanceToDomain(res, &res.Instances[0], p.logger, state)
}
