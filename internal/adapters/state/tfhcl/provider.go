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

// Provider holds the state for the HCL adapter.
type Provider struct {
	config      Config
	logger      ports.Logger
	parsedFile  *hcl.File // Merged file
	evalContext *hcl.EvalContext
	initMutex   sync.RWMutex // Changed from hclMutex for clarity
	initErr     error
}

// Config struct defined previously
type Config struct {
	Directory string   `yaml:"directory"`
	VarFiles  []string `yaml:"var_files"`
	Workspace string   `yaml:"workspace"`
}

// NewProvider creates a new TFHCL provider instance.
func NewProvider(cfg Config, logger ports.Logger) (*Provider, error) {
	if cfg.Directory == "" {
		return nil, errors.New(errors.CodeConfigValidation, "HCL provider requires a directory")
	}
	if cfg.Workspace == "" {
		cfg.Workspace = "default"
	}

	p := &Provider{
		config: cfg,
		logger: logger.WithFields(map[string]any{"provider": ProviderTypeTFHCL}),
	}
	return p, nil
}

// Type returns the provider type identifier.
func (p *Provider) Type() string {
	return ProviderTypeTFHCL
}

// ensureInitialized ensures HCL parsing and context building happens only once.
func (p *Provider) ensureInitialized(ctx context.Context) (*hcl.File, *hcl.EvalContext, error) {
	p.initMutex.RLock()
	if p.evalContext != nil { // Check if successfully initialized
		p.initMutex.RUnlock()
		return p.parsedFile, p.evalContext, nil
	}
	if p.initErr != nil { // Check if initialization failed previously
		p.initMutex.RUnlock()
		return nil, nil, p.initErr
	}
	p.initMutex.RUnlock()

	// Acquire write lock for initialization
	p.initMutex.Lock()
	defer p.initMutex.Unlock()

	// Double check now that we have the write lock
	if p.evalContext != nil {
		return p.parsedFile, p.evalContext, nil
	}
	if p.initErr != nil {
		return nil, nil, p.initErr
	}

	p.logger.Infof(ctx, "Initializing HCL provider: parsing directory '%s'", p.config.Directory)
	mergedFile, parseDiags, err := parseAndMergeHCLDirectory(ctx, p.config.Directory, p.logger)
	if err != nil {
		// Use a more specific error structure if possible
		p.initErr = errors.Wrap(&evaluator.HCLDiagnosticsError{Operation: "parsing/merging", FilePath: p.config.Directory, Diags: parseDiags}, errors.CodeStateParseError, "HCL parsing/merging failed")
		p.logger.Errorf(ctx, p.initErr, "Fatal HCL parsing/merging error(s)")
		if len(parseDiags) > 0 {
			p.logger.Debugf(ctx, "Parsing/Merging Diagnostics:\n%s", parseDiags.Error())
		}
		return nil, nil, p.initErr
	}
	// Log non-fatal parsing/merging diagnostics
	if len(parseDiags) > 0 && !evaluator.DiagsHasFatalErrors(parseDiags) {
		p.logger.Warnf(ctx, "Non-fatal HCL parsing/merging diagnostics occurred:\n%s", parseDiags.Error())
	}
	p.parsedFile = mergedFile
	p.logger.Infof(ctx, "HCL parsing and merging complete.")

	p.logger.Infof(ctx, "Building HCL evaluation context for workspace '%s'", p.config.Workspace)
	evalCtx, evalDiags := evaluator.BuildEvalContext(
		ctx,
		p.parsedFile.Body,
		p.config.VarFiles,
		p.config.Directory,
		p.config.Workspace,
		p.logger,
	)
	if evaluator.DiagsHasFatalErrors(evalDiags) { // Check fatal errors from context build
		p.initErr = errors.Wrap(&evaluator.HCLDiagnosticsError{Operation: "building context", FilePath: p.config.Directory, Diags: evalDiags}, errors.CodeStateParseError, "failed to build HCL evaluation context")
		p.logger.Errorf(ctx, p.initErr, "Fatal HCL evaluation context diagnostics")
		if len(evalDiags) > 0 {
			p.logger.Debugf(ctx, "Context Building Diagnostics:\n%s", evalDiags.Error())
		}
		return nil, nil, p.initErr // Return nil context on fatal error
	}
	// Log non-fatal context diagnostics
	if len(evalDiags) > 0 {
		p.logger.Warnf(ctx, "Non-fatal HCL context building diagnostics occurred:\n%s", evalDiags.Error())
	}

	p.evalContext = evalCtx
	p.logger.Infof(ctx, "HCL evaluation context built successfully.")
	p.initErr = nil // Mark initialization successful

	return p.parsedFile, p.evalContext, nil
}

// ListResources parses, evaluates, and maps HCL resources.
func (p *Provider) ListResources(ctx context.Context, kind domain.ResourceKind) ([]domain.StateResource, error) {
	parsedFile, evalContext, err := p.ensureInitialized(ctx)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeStateReadError, "HCL provider initialization failed")
	}

	p.logger.Debugf(ctx, "Finding HCL resource blocks for kind '%s'", kind)
	fmt.Printf("DEBUG: ListResources calling findResourceBlocksInBody for kind %s\n", kind)
	blocks, _, findDiags := findResourceBlocksInBody(parsedFile.Body, kind)
	fmt.Printf("DEBUG: ListResources received %d blocks from findResourceBlocksInBody\n", len(blocks))

	if evaluator.DiagsHasFatalErrors(findDiags) { // Check fatal errors finding blocks (e.g., duplicates)
		err = errors.Wrap(&evaluator.HCLDiagnosticsError{Operation: "finding blocks", FilePath: p.config.Directory, Diags: findDiags}, errors.CodeStateParseError, "Fatal error finding HCL blocks")
		p.logger.Errorf(ctx, err, "Cannot proceed with listing kind %s", kind)
		return nil, err
	}
	if len(findDiags) > 0 { // Log warnings
		p.logger.Warnf(ctx, "Non-fatal diagnostics finding resource blocks for kind %s:\n%s", kind, findDiags.Error())
	}

	domainResources := make([]domain.StateResource, 0, len(blocks))
	p.logger.Debugf(ctx, "Found %d potential HCL blocks for kind '%s', evaluating...", len(blocks), kind)

	for i, block := range blocks {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		address := fmt.Sprintf("%s.%s", block.Labels[0], block.Labels[1])
		fmt.Printf("DEBUG: Processing block[%d]: %s\n", i, address)
		blockLogger := p.logger.WithFields(map[string]any{"hcl_address": address})

		evaluatedAttrs, evalDiags := evaluator.EvaluateResourceBlock(ctx, block, evalContext, blockLogger)
		fmt.Printf("DEBUG: Block[%d] evaluation returned %d attributes\n", i, len(evaluatedAttrs))
		if evaluator.DiagsHasFatalErrors(evalDiags) { // Check fatal errors for this block
			fmt.Printf("DEBUG: Block[%d] has fatal evaluation diagnostics, skipping\n", i)
			blockLogger.Errorf(ctx, &evaluator.HCLDiagnosticsError{Operation: "evaluating block", Diags: evalDiags}, "Errors evaluating HCL block, skipping resource")
			// Optionally collect these errors instead of just skipping
			continue
		}
		if len(evalDiags) > 0 { // Log warnings for this block
			blockLogger.Warnf(ctx, "Non-fatal diagnostics evaluating HCL block:\n%s", evalDiags.Error())
		}

		fmt.Printf("DEBUG: Mapping block[%d] to domain resource\n", i)
		mappedRes, mapErr := mapEvaluatedHCLToDomain(kind, address, evaluatedAttrs)
		if mapErr != nil {
			fmt.Printf("DEBUG: Block[%d] mapping failed: %v\n", i, mapErr)
			blockLogger.Errorf(ctx, mapErr, "Failed to map evaluated HCL resource, skipping")
			continue
		}
		fmt.Printf("DEBUG: Block[%d] successfully mapped to domain resource\n", i)
		domainResources = append(domainResources, mappedRes)
	}

	fmt.Printf("DEBUG: ListResources returning %d domain resources\n", len(domainResources))
	p.logger.Debugf(ctx, "Successfully evaluated and mapped %d HCL resources for kind '%s'", len(domainResources), kind)
	return domainResources, nil
}

// GetResource finds, evaluates, and maps a single HCL resource.
func (p *Provider) GetResource(ctx context.Context, kind domain.ResourceKind, identifier string) (domain.StateResource, error) {
	parsedFile, evalContext, err := p.ensureInitialized(ctx)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeStateReadError, "HCL provider initialization failed")
	}

	resLogger := p.logger.WithFields(map[string]any{"hcl_address": identifier, "resource_kind": kind})
	resLogger.Debugf(ctx, "Finding specific HCL resource block")

	block, findDiags := findSpecificResourceBlock(parsedFile.Body, identifier)
	if evaluator.DiagsHasFatalErrors(findDiags) { // Check fatal errors (duplicates)
		err = errors.Wrap(&evaluator.HCLDiagnosticsError{Operation: "finding specific block", FilePath: p.config.Directory, Diags: findDiags}, errors.CodeStateParseError, "Fatal error finding specific HCL block")
		resLogger.Errorf(ctx, err, "Cannot proceed with GetResource")
		return nil, err
	}
	if len(findDiags) > 0 { // Log warnings
		resLogger.Warnf(ctx, "Non-fatal diagnostics finding specific resource block:\n%s", findDiags.Error())
	}
	if block == nil {
		return nil, errors.New(errors.CodeResourceNotFound, fmt.Sprintf("resource '%s' not found in HCL files", identifier))
	}

	blockKind, _ := mapping.MapTfTypeToDomainKind(block.Labels[0]) // Reuse mapping
	if blockKind != kind {
		return nil, errors.New(errors.CodeResourceNotFound, fmt.Sprintf("resource '%s' found, but it has kind '%s', expected '%s'", identifier, blockKind, kind))
	}

	resLogger.Debugf(ctx, "Evaluating found HCL resource block")
	evaluatedAttrs, evalDiags := evaluator.EvaluateResourceBlock(ctx, block, evalContext, resLogger)
	if evaluator.DiagsHasFatalErrors(evalDiags) { // Check fatal errors for this specific block
		err = errors.Wrap(&evaluator.ResourceEvaluationError{Address: identifier, Diags: evalDiags}, errors.CodeStateParseError, "Errors evaluating target HCL block")
		resLogger.Errorf(ctx, err, "Cannot return resource due to evaluation errors")
		return nil, err
	}
	if len(evalDiags) > 0 { // Log warnings
		resLogger.Warnf(ctx, "Non-fatal diagnostics evaluating HCL block:\n%s", evalDiags.Error())
	}

	resLogger.Debugf(ctx, "Mapping evaluated HCL resource")
	mappedRes, mapErr := mapEvaluatedHCLToDomain(kind, identifier, evaluatedAttrs)
	if mapErr != nil {
		return nil, errors.Wrap(mapErr, errors.CodeInternal, "failed to map evaluated HCL resource")
	}

	return mappedRes, nil
}
