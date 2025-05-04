// --- START OF FILE infra-drift-detector/internal/adapters/state/tfhcl/provider.go ---

package tfhcl

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/mapping"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl/evaluator"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	apperrors "github.com/olusolaa/infra-drift-detector/internal/errors"
)

const ProviderTypeTFHCL = "tfhcl"

type Provider struct {
	config      Config
	logger      ports.Logger
	initOnce    sync.Once
	initErr     error
	module      *evaluator.Module
	parsedFiles map[string]*hcl.File
}

type Config struct {
	Directory string   `yaml:"directory" validate:"required,dir"`
	VarFiles  []string `yaml:"var_files" validate:"omitempty,dive,file"`
	Workspace string   `yaml:"workspace" validate:"required"`
}

func NewProvider(cfg Config, logger ports.Logger) (*Provider, error) {
	if cfg.Directory == "" {
		return nil, apperrors.New(apperrors.CodeConfigValidation, "HCL provider requires a non-empty directory")
	}
	absDir, err := filepath.Abs(cfg.Directory)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.CodeConfigValidation, "failed to get absolute path for HCL directory")
	}
	cfg.Directory = absDir

	if cfg.Workspace == "" {
		cfg.Workspace = "default"
	}

	p := &Provider{
		config: cfg,
		logger: logger.WithFields(map[string]any{"provider": ProviderTypeTFHCL, "directory": cfg.Directory}),
	}
	return p, nil
}

func (p *Provider) Type() string {
	return ProviderTypeTFHCL
}

func (p *Provider) ensureInitialized(ctx context.Context) error {
	p.initOnce.Do(func() {
		p.logger.Infof(ctx, "Initializing HCL provider...")
		p.parsedFiles, p.module, p.initErr = evaluator.LoadModule(ctx, p.config.Directory, p.config.VarFiles, p.config.Workspace, p.logger)
		if p.initErr != nil {
			p.logger.Errorf(ctx, p.initErr, "HCL provider initialization failed")
		} else {
			p.logger.Infof(ctx, "HCL provider initialized successfully")
		}
	})
	return p.initErr
}

func (p *Provider) ListResources(ctx context.Context, kind domain.ResourceKind) ([]domain.StateResource, error) {
	if err := p.ensureInitialized(ctx); err != nil {
		return nil, apperrors.Wrap(err, apperrors.CodeStateReadError, "HCL provider initialization failed")
	}
	if p.module == nil || p.parsedFiles == nil {
		return nil, apperrors.New(apperrors.CodeInternal, "HCL provider not properly initialized (nil module or file map)")
	}

	p.logger.Debugf(ctx, "Finding HCL resource blocks for kind '%s'", kind)
	resourceBlocks, findDiags := evaluator.FindResourceBlocksOfType(p.parsedFiles, kind)
	if evaluator.DiagsHasFatalErrors(findDiags) {
		err := apperrors.Wrap(&evaluator.HCLDiagnosticsError{Diags: findDiags}, apperrors.CodeStateParseError, "Fatal error finding HCL blocks")
		p.logger.Errorf(ctx, err, "Cannot proceed with listing kind %s", kind)
		return nil, err
	}
	if len(findDiags) > 0 {
		p.logger.Warnf(ctx, "Non-fatal diagnostics finding blocks for %s:\n%s", kind, findDiags.Error())
	}

	domainResources := make([]domain.StateResource, 0, len(resourceBlocks))
	p.logger.Debugf(ctx, "Found %d potential HCL blocks for kind '%s', evaluating...", len(resourceBlocks), kind)

	evalCtx := p.module.EvalContext()

	for _, block := range resourceBlocks {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if len(block.Labels) != 2 {
			p.logger.Warnf(ctx, "Skipping resource block with unexpected labels: %v", block.Labels)
			continue
		}
		address := fmt.Sprintf("%s.%s", block.Labels[0], block.Labels[1])
		blockLogger := p.logger.WithFields(map[string]any{"hcl_address": address})

		evaluatedAttrs, evalDiags := evaluator.EvaluateBlock(ctx, block, evalCtx, blockLogger)
		if evaluator.DiagsHasFatalErrors(evalDiags) {
			blockLogger.Errorf(ctx, &evaluator.HCLDiagnosticsError{Diags: evalDiags}, "Errors evaluating HCL block, skipping resource")
			continue
		}
		if len(evalDiags) > 0 {
			blockLogger.Warnf(ctx, "Non-fatal diagnostics evaluating HCL block:\n%s", evalDiags.Error())
		}

		mappedRes, mapErr := MapEvaluatedHCLToDomain(kind, address, evaluatedAttrs)
		if mapErr != nil {
			blockLogger.Errorf(ctx, mapErr, "Failed to map evaluated HCL resource, skipping")
			continue
		}
		domainResources = append(domainResources, mappedRes)
	}

	p.logger.Debugf(ctx, "Successfully evaluated and mapped %d HCL resources for kind '%s'", len(domainResources), kind)
	return domainResources, nil
}

func (p *Provider) GetResource(ctx context.Context, kind domain.ResourceKind, identifier string) (domain.StateResource, error) {
	if err := p.ensureInitialized(ctx); err != nil {
		return nil, apperrors.Wrap(err, apperrors.CodeStateReadError, "HCL provider initialization failed")
	}
	if p.module == nil || p.parsedFiles == nil {
		return nil, apperrors.New(apperrors.CodeInternal, "HCL provider not properly initialized")
	}

	resLogger := p.logger.WithFields(map[string]any{"hcl_address": identifier, "resource_kind": kind})
	resLogger.Debugf(ctx, "Finding specific HCL resource block")

	block, findDiags := evaluator.FindSpecificResourceBlock(p.parsedFiles, identifier)
	if evaluator.DiagsHasFatalErrors(findDiags) {
		err := apperrors.Wrap(&evaluator.HCLDiagnosticsError{Diags: findDiags}, apperrors.CodeStateParseError, "Fatal error finding specific HCL block")
		resLogger.Errorf(ctx, err, "Cannot proceed with GetResource")
		return nil, err
	}
	if len(findDiags) > 0 {
		resLogger.Warnf(ctx, "Non-fatal diagnostics finding specific resource block:\n%s", findDiags.Error())
	}
	if block == nil {
		return nil, apperrors.New(apperrors.CodeResourceNotFound, fmt.Sprintf("resource '%s' not found in HCL files", identifier))
	}

	blockKind, kindErr := mapping.MapTfTypeToDomainKind(block.Labels[0])
	if kindErr != nil {
		return nil, apperrors.Wrap(kindErr, apperrors.CodeResourceNotFound, fmt.Sprintf("resource '%s' found, but its type '%s' is unsupported", identifier, block.Labels[0]))
	}
	if blockKind != kind {
		return nil, apperrors.New(apperrors.CodeResourceNotFound, fmt.Sprintf("resource '%s' found, but it has kind '%s', expected '%s'", identifier, blockKind, kind))
	}

	resLogger.Debugf(ctx, "Evaluating found HCL resource block")
	evalCtx := p.module.EvalContext()
	evaluatedAttrs, evalDiags := evaluator.EvaluateBlock(ctx, block, evalCtx, resLogger)
	if evaluator.DiagsHasFatalErrors(evalDiags) {
		err := apperrors.Wrap(&evaluator.HCLDiagnosticsError{Address: identifier, Diags: evalDiags}, apperrors.CodeStateParseError, "Errors evaluating target HCL block")
		resLogger.Errorf(ctx, err, "Cannot return resource due to evaluation errors")
		return nil, err
	}
	if len(evalDiags) > 0 {
		resLogger.Warnf(ctx, "Non-fatal diagnostics evaluating HCL block:\n%s", evalDiags.Error())
	}

	resLogger.Debugf(ctx, "Mapping evaluated HCL resource")
	mappedRes, mapErr := MapEvaluatedHCLToDomain(kind, identifier, evaluatedAttrs)
	if mapErr != nil {
		return nil, apperrors.Wrap(mapErr, apperrors.CodeInternal, "failed to map evaluated HCL resource")
	}

	return mappedRes, nil
}
