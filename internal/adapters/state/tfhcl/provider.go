package tfhcl

import (
	"context"
	"fmt"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/mapping"
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

const ProviderTypeTFHCL = "tfhcl"

type Provider struct {
	dirPath string
	// Caching parsed files
	parsedFiles     map[string]*hcl.File
	parsedAddresses map[string]string // block identifier -> TF address
	hclMutex        sync.RWMutex
	parseErr        error
}

type Config struct {
	Directory string `yaml:"directory"`
	// Workspace string `yaml:"workspace"` // Add workspace support later if needed
}

func NewProvider(cfg Config) (*Provider, error) {
	if cfg.Directory == "" {
		return nil, errors.New(errors.CodeConfigValidation, "Terraform HCL provider requires a non-empty directory path")
	}
	// Check if directory exists?
	// _, err := os.Stat(cfg.Directory); if os.IsNotExist(err) { ... }

	p := &Provider{
		dirPath: cfg.Directory,
	}
	return p, nil
}

func (p *Provider) Type() string {
	return ProviderTypeTFHCL
}

// ensureHCLParsed parses the HCL directory if not already done. Thread-safe.
func (p *Provider) ensureHCLParsed(ctx context.Context) (map[string]*hcl.File, map[string]string, error) {
	p.hclMutex.RLock()
	if p.parsedFiles != nil {
		p.hclMutex.RUnlock()
		return p.parsedFiles, p.parsedAddresses, nil
	}
	if p.parseErr != nil {
		p.hclMutex.RUnlock()
		return nil, nil, p.parseErr
	}
	p.hclMutex.RUnlock()

	p.hclMutex.Lock()
	defer p.hclMutex.Unlock()

	if p.parsedFiles != nil {
		return p.parsedFiles, p.parsedAddresses, nil
	} // Double check
	if p.parseErr != nil {
		return nil, nil, p.parseErr
	}

	// Parse directory
	files, err := parseHCLDirectory(p.dirPath) // Calls helper from parser.go
	if err != nil {
		p.parseErr = err // Cache error
		return nil, nil, err
	}
	p.parsedFiles = files
	// Store addresses map after initial parse if needed (currently done inside ListResources)
	// p.parsedAddresses = ??? // Need findHCLResources to return addresses map

	return p.parsedFiles, p.parsedAddresses, nil
}

func (p *Provider) ListResources(ctx context.Context, kind domain.ResourceKind) ([]domain.StateResource, error) {
	files, _, err := p.ensureHCLParsed(ctx) // Ignore addresses map from cache for now
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeStateReadError, "failed to ensure HCL directory is parsed")
	}
	if files == nil {
		return nil, errors.New(errors.CodeStateParseError, "parsed HCL files map is nil")
	}

	// Find the relevant resource blocks
	blocks, addresses, err := findHCLResources(files, kind) // Calls helper from parser.go
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "failed finding HCL resource blocks")
	}
	// Cache addresses map if needed: p.parsedAddresses = addresses (requires mutex update)

	domainResources := make([]domain.StateResource, 0, len(blocks))
	for _, block := range blocks {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// Get TF address using block identifier (less efficient than passing map index)
		// Refactor findHCLResources slightly if needed
		blockIdentifier := fmt.Sprintf("%s:%s.%s", block.DefRange.Filename, block.Labels[0], block.Labels[1]) // Reconstruct identifier
		address := addresses[blockIdentifier]                                                                 // Lookup address
		if address == "" {                                                                                    /* Handle error: address not found */
			continue
		}

		// Extract only literal attributes
		literalAttrs, extractErr := extractLiteralAttributes(block.Body) // Calls helper from parser.go
		if extractErr != nil {
			// Log warning but proceed? The file parse succeeded overall.
			// fmt.Printf("Warning: extracting literal attributes for %s: %v\n", address, extractErr)
			// Continue with potentially empty literalAttrs
		}

		// Map to domain resource
		mappedRes, mapErr := mapHCLBlockToDomain(block, address, literalAttrs) // Calls helper from mapper.go
		if mapErr != nil {
			// Log or collect errors? Fail fast? Let's wrap and return.
			return nil, errors.Wrap(mapErr, errors.CodeInternal, fmt.Sprintf("failed to map HCL resource block %s", address))
		}
		domainResources = append(domainResources, mappedRes)
	}

	return domainResources, nil
}

// GetResource for HCL would involve parsing all files and finding the specific block by identifier (address).
func (p *Provider) GetResource(ctx context.Context, kind domain.ResourceKind, identifier string) (domain.StateResource, error) {
	files, _, err := p.ensureHCLParsed(ctx)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeStateReadError, "failed to ensure HCL directory is parsed")
	}
	if files == nil {
		return nil, errors.New(errors.CodeStateParseError, "parsed HCL files map is nil")
	}

	// Define schema and search all files
	resourceSchema := &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "resource", LabelNames: []string{"type", "name"}}}}
	var foundBlock *hcl.Block
	foundAddress := ""

	for _, file := range files {
		content, _ := file.Body.Content(resourceSchema) // Ignore diagnostics for search
		for _, block := range content.Blocks {
			if block.Type == "resource" && len(block.Labels) == 2 {
				tfType := block.Labels[0]
				tfName := block.Labels[1]
				address := fmt.Sprintf("%s.%s", tfType, tfName)
				if address == identifier {
					resKind, _ := mapping.MapTfTypeToDomainKind(tfType) // Reuse mapping
					if resKind == kind {
						foundBlock = block
						foundAddress = address
						break // Found it
					}
				}
			}
		}
		if foundBlock != nil {
			break
		}
	}

	if foundBlock == nil {
		return nil, errors.New(errors.CodeResourceNotFound, fmt.Sprintf("resource '%s' of kind '%s' not found in HCL files", identifier, kind))
	}

	// Extract literals and map
	literalAttrs, _ := extractLiteralAttributes(foundBlock.Body) // Ignore extraction errors for Get
	mappedRes, mapErr := mapHCLBlockToDomain(foundBlock, foundAddress, literalAttrs)
	if mapErr != nil {
		return nil, errors.Wrap(mapErr, errors.CodeInternal, fmt.Sprintf("failed to map HCL resource block %s", foundAddress))
	}

	return mappedRes, nil
}
