package tfhcl

import (
	"context"
	"fmt"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/mapping"
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl/evaluator"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

const ProviderTypeTFHCL = "tfhcl"

type Provider struct {
	Config      Config
	logger      ports.Logger
	parsedFiles map[string]*hcl.File
	evalContext *hcl.EvalContext
	initMutex   sync.RWMutex
	initErr     error
	initDiags   hcl.Diagnostics
}

type Config struct {
	Directory string   `yaml:"directory"`
	VarFiles  []string `yaml:"var_files"`
	Workspace string   `yaml:"workspace"`
}

func NewProvider(cfg Config, logger ports.Logger) (*Provider, error) {
	if cfg.Directory == "" {
		return nil, errors.New(errors.CodeConfigValidation, "HCL provider requires a directory")
	}
	if cfg.Workspace == "" {
		cfg.Workspace = "default"
	}

	p := &Provider{
		Config: cfg,
		logger: logger.WithFields(map[string]any{"provider": ProviderTypeTFHCL}),
	}
	return p, nil
}

func (p *Provider) Type() string {
	return ProviderTypeTFHCL
}

func (p *Provider) ensureInitialized(ctx context.Context) error {
	p.initMutex.RLock()
	if p.evalContext != nil || p.initErr != nil {
		p.initMutex.RUnlock()
		return p.initErr
	}
	p.initMutex.RUnlock()

	p.initMutex.Lock()
	defer p.initMutex.Unlock()

	if p.evalContext != nil || p.initErr != nil {
		return p.initErr
	}

	p.logger.Infof(ctx, "Initializing HCL provider: parsing directory '%s'", p.Config.Directory)
	filesMap, parseDiags, err := ParseHCLDirectory(ctx, p.Config.Directory, p.logger)
	p.initDiags = append(p.initDiags, parseDiags...)
	if err != nil {
		if evaluator.DiagsHasFatalErrors(parseDiags) {
			p.initErr = errors.Wrap(&evaluator.HCLDiagnosticsError{Operation: "parsing", FilePath: p.Config.Directory, Diags: parseDiags}, errors.CodeStateParseError, err.Error())
		} else {
			p.initErr = err
		}
		p.logger.Errorf(ctx, p.initErr, "Fatal HCL parsing error(s)")
		return p.initErr
	}
	p.parsedFiles = filesMap
	p.logger.Infof(ctx, "HCL parsing complete for %d files.", len(p.parsedFiles))

	p.logger.Infof(ctx, "Building HCL evaluation context for workspace '%s'", p.Config.Workspace)
	tempFilesSlice := make([]*hcl.File, 0, len(p.parsedFiles))
	for _, f := range p.parsedFiles {
		tempFilesSlice = append(tempFilesSlice, f)
	}
	mergedBody := hcl.MergeFiles(tempFilesSlice)

	evalCtx, evalDiags := evaluator.BuildEvalContext(
		ctx,
		mergedBody,
		p.Config.VarFiles,
		p.Config.Directory,
		p.Config.Workspace,
		p.logger,
	)
	p.initDiags = append(p.initDiags, evalDiags...)

	if evaluator.DiagsHasFatalErrors(p.initDiags) {
		p.initErr = errors.Wrap(&evaluator.HCLDiagnosticsError{Operation: "parsing or context build", FilePath: p.Config.Directory, Diags: p.initDiags}, errors.CodeStateParseError, "fatal errors during HCL initialization")
		p.logger.Errorf(ctx, p.initErr, "Fatal HCL initialization diagnostics")
		return p.initErr
	}
	if len(p.initDiags) > 0 {
		p.logger.Warnf(ctx, "Non-fatal diagnostics during HCL initialization:\n%s", p.initDiags.Error())
	}

	p.evalContext = evalCtx
	p.logger.Infof(ctx, "HCL evaluation context built successfully.")
	p.initErr = nil
	return nil
}

func (p *Provider) ListResources(ctx context.Context, kind domain.ResourceKind) ([]domain.StateResource, error) {
	if err := p.ensureInitialized(ctx); err != nil {
		return nil, errors.Wrap(err, errors.CodeStateReadError, "HCL provider initialization failed")
	}

	p.initMutex.RLock()
	parsedFiles := p.parsedFiles
	evalContext := p.evalContext
	p.initMutex.RUnlock()

	if parsedFiles == nil || evalContext == nil {
		return nil, errors.New(errors.CodeInternal, "HCL provider not properly initialized (nil file map or context)")
	}

	p.logger.Debugf(ctx, "Finding HCL resource blocks for kind '%s'", kind)
	blocks, _, findDiags := FindResourceBlocks(parsedFiles, kind)
	if evaluator.DiagsHasFatalErrors(findDiags) {
		err := errors.Wrap(&evaluator.HCLDiagnosticsError{Operation: "finding blocks", FilePath: p.Config.Directory, Diags: findDiags}, errors.CodeStateParseError, "Fatal error finding HCL blocks")
		p.logger.Errorf(ctx, err, "Cannot proceed with listing kind %s", kind)
		return nil, err
	}
	if len(findDiags) > 0 {
		p.logger.Warnf(ctx, "Non-fatal diagnostics finding blocks for %s:\n%s", kind, findDiags.Error())
	}

	domainResources := make([]domain.StateResource, 0, len(blocks))
	p.logger.Debugf(ctx, "Found %d potential HCL blocks for kind '%s', evaluating...", len(blocks), kind)

	for _, block := range blocks {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		address := fmt.Sprintf("%s.%s", block.Labels[0], block.Labels[1])
		blockLogger := p.logger.WithFields(map[string]any{"hcl_address": address})

		evaluatedAttrs, evalDiags := evaluator.EvaluateResourceBlock(ctx, block, evalContext, blockLogger)
		if evaluator.DiagsHasFatalErrors(evalDiags) {
			blockLogger.Errorf(ctx, &evaluator.HCLDiagnosticsError{Operation: "evaluating block", Diags: evalDiags}, "Errors evaluating HCL block, skipping resource")
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
		return nil, errors.Wrap(err, errors.CodeStateReadError, "HCL provider initialization failed")
	}

	p.initMutex.RLock()
	parsedFiles := p.parsedFiles
	evalContext := p.evalContext
	p.initMutex.RUnlock()

	if parsedFiles == nil || evalContext == nil {
		return nil, errors.New(errors.CodeInternal, "HCL provider not properly initialized")
	}

	resLogger := p.logger.WithFields(map[string]any{"hcl_address": identifier, "resource_kind": kind})
	resLogger.Debugf(ctx, "Finding specific HCL resource block")

	block, findDiags := FindSpecificResourceBlock(parsedFiles, identifier)
	if evaluator.DiagsHasFatalErrors(findDiags) {
		err := errors.Wrap(&evaluator.HCLDiagnosticsError{Operation: "finding specific block", FilePath: p.Config.Directory, Diags: findDiags}, errors.CodeStateParseError, "Fatal error finding specific HCL block")
		resLogger.Errorf(ctx, err, "Cannot proceed with GetResource")
		return nil, err
	}
	if len(findDiags) > 0 {
		resLogger.Warnf(ctx, "Non-fatal diagnostics finding specific resource block:\n%s", findDiags.Error())
	}
	if block == nil {
		return nil, errors.New(errors.CodeResourceNotFound, fmt.Sprintf("resource '%s' not found in HCL files", identifier))
	}

	blockKind, _ := mapping.MapTfTypeToDomainKind(block.Labels[0])
	if blockKind != kind {
		return nil, errors.New(errors.CodeResourceNotFound, fmt.Sprintf("resource '%s' found, but it has kind '%s', expected '%s'", identifier, blockKind, kind))
	}

	resLogger.Debugf(ctx, "Evaluating found HCL resource block")
	evaluatedAttrs, evalDiags := evaluator.EvaluateResourceBlock(ctx, block, evalContext, resLogger)
	if evaluator.DiagsHasFatalErrors(evalDiags) {
		err := errors.Wrap(&evaluator.ResourceEvaluationError{Address: identifier, Diags: evalDiags}, errors.CodeStateParseError, "Errors evaluating target HCL block")
		resLogger.Errorf(ctx, err, "Cannot return resource due to evaluation errors")
		return nil, err
	}
	if len(evalDiags) > 0 {
		resLogger.Warnf(ctx, "Non-fatal diagnostics evaluating HCL block:\n%s", evalDiags.Error())
	}

	resLogger.Debugf(ctx, "Mapping evaluated HCL resource")
	mappedRes, mapErr := MapEvaluatedHCLToDomain(kind, identifier, evaluatedAttrs)
	if mapErr != nil {
		return nil, errors.Wrap(mapErr, errors.CodeInternal, "failed to map evaluated HCL resource")
	}

	return mappedRes, nil
}
