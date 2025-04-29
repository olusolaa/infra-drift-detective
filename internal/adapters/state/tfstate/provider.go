package tfstate

import (
	"context"
	"fmt"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/mapping"
	"sync"

	terraformjson "github.com/hashicorp/terraform-json"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

const ProviderTypeTFState = "tfstate"

type Provider struct {
	filePath string
	parsedState *terraformjson.State
	stateMutex  sync.RWMutex
	parseErr    error
}

type Config struct {
	FilePath string `yaml:"path"`
}

func NewProvider(cfg Config) (*Provider, error) {
	if cfg.FilePath == "" {
		return nil, errors.New(errors.CodeConfigValidation, "Terraform state provider requires a non-empty file path")
	}
	p := &Provider{
		filePath: cfg.FilePath,
	}
	return p, nil
}

func (p *Provider) Type() string {
	return ProviderTypeTFState
}

func (p *Provider) ensureStateParsed(ctx context.Context) (*terraformjson.State, error) {
	p.stateMutex.RLock()
	if p.parsedState != nil {
		p.stateMutex.RUnlock()
		return p.parsedState, nil
	}
	if p.parseErr != nil {
		p.stateMutex.RUnlock()
		return nil, p.parseErr
	}
	p.stateMutex.RUnlock()

	p.stateMutex.Lock()
	defer p.stateMutex.Unlock()

	if p.parsedState != nil {
		return p.parsedState, nil
	}
	if p.parseErr != nil {
		return nil, p.parseErr
	}

	parsed, err := parseStateFile(p.filePath)
	if err != nil {
		p.parseErr = err
		return nil, err
	}
	p.parsedState = parsed
	return p.parsedState, nil
}

func (p *Provider) ListResources(ctx context.Context, kind domain.ResourceKind) ([]domain.StateResource, error) {
	state, err := p.ensureStateParsed(ctx)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeStateReadError, "failed to ensure state file is parsed")
	}
	if state == nil {
		return nil, errors.New(errors.CodeStateParseError, "parsed state is nil")
	}

	stateResources, err := findResourcesInState(state, kind)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "failed finding resources in parsed state")
	}

	domainResources := make([]domain.StateResource, 0, len(stateResources))
	for _, res := range stateResources {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		mappedRes, mapErr := mapStateResourceToDomain(res)
		if mapErr != nil {
			return nil, errors.Wrap(mapErr, errors.CodeInternal, fmt.Sprintf("failed to map state resource %s", res.Address))
		}
		domainResources = append(domainResources, mappedRes)
	}

	return domainResources, nil
}

func (p *Provider) GetResource(ctx context.Context, kind domain.ResourceKind, identifier string) (domain.StateResource, error) {
	state, err := p.ensureStateParsed(ctx)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeStateReadError, "failed to ensure state file is parsed")
	}
	if state == nil {
		return nil, errors.New(errors.CodeStateParseError, "parsed state is nil")
	}

	var foundResource *terraformjson.StateResource

	var findInModule func(module *terraformjson.StateModule) bool
	findInModule = func(module *terraformjson.StateModule) bool {
		if module == nil {
			return false
		}
		for _, res := range module.Resources {
			if res != nil && res.Address == identifier {
				resKind, _ := mapping.MapTfTypeToDomainKind(res.Type)
				if resKind == kind {
					foundResource = res
					return true
				}
			}
		}
		for _, child := range module.ChildModules {
			if findInModule(child) {
				return true
			}
		}
		return false
	}

	if state.Values != nil && state.Values.RootModule != nil {
		if !findInModule(state.Values.RootModule) {
			return nil, errors.New(errors.CodeResourceNotFound, fmt.Sprintf("resource '%s' of kind '%s' not found in state", identifier, kind))
		}
	} else {
		return nil, errors.New(errors.CodeResourceNotFound, "state file appears empty or invalid (no root module values)")
	}

	if foundResource == nil {
		return nil, errors.New(errors.CodeResourceNotFound, fmt.Sprintf("resource '%s' of kind '%s' not found after search", identifier, kind))
	}

	mappedRes, mapErr := mapStateResourceToDomain(foundResource)
	if mapErr != nil {
		return nil, errors.Wrap(mapErr, errors.CodeInternal, fmt.Sprintf("failed to map state resource %s", foundResource.Address))
	}

	return mappedRes, nil
}
