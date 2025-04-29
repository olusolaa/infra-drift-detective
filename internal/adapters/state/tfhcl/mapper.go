package tfhcl

import (
	"fmt"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/mapping"
	"strings"

	"github.com/hashicorp/hcl/v2"

	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

type tfHCLResource struct {
	meta domain.ResourceMetadata
	attr map[string]any
}

func (r *tfHCLResource) Metadata() domain.ResourceMetadata { return r.meta }
func (r *tfHCLResource) Attributes() map[string]any        { return r.attr }

// mapHCLBlockToDomain converts a parsed HCL resource block and its literal attributes
// into the application's domain resource interface.
func mapHCLBlockToDomain(
	block *hcl.Block,
	address string, // TF Address (e.g., aws_instance.web)
	literalAttrs map[string]any,
) (domain.StateResource, error) {

	if block == nil || len(block.Labels) != 2 {
		return nil, errors.New(errors.CodeInternal, "invalid HCL block provided to mapper")
	}
	tfType := block.Labels[0]

	kind, err := mapping.MapTfTypeToDomainKind(tfType) // Reuse tfstate type mapping
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "mapping HCL resource type")
	}

	// Normalize the extracted literal attributes using the *same* logic as tfstate,
	// applying it only to the subset of attributes we could extract literally.
	targetAttrs := make(map[string]any)
	// We pass the *literal* attributes extracted from HCL as the 'rawAttrs'
	// The normalization function will only process keys defined in its mapping for the kind.
	err = mapping.NormalizeAndCopyAttributes(kind, literalAttrs, targetAttrs) // Reuse tfstate normalization
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, fmt.Sprintf("failed normalizing literal HCL attributes for %s", address))
	}

	// Metadata - limited information available from HCL block itself
	// ProviderType is harder to determine reliably from just HCL without provider blocks/config.
	// Assume AWS for now based on type prefix? Fragile. Leave empty or make best guess.
	providerType := ""
	if strings.HasPrefix(tfType, "aws_") {
		providerType = "aws"
	}

	meta := domain.ResourceMetadata{
		Kind:         kind,
		ProviderType: providerType, // Best guess
		// ProviderAssignedID: Not available from HCL
		SourceIdentifier: address, // The TF address IS the identifier here
		// Region: Often from provider block or variable, not directly here
		// AccountID: Not available from HCL
	}

	// Manually add keys that might be top-level literals but not in state attr map?
	// e.g., if tfstate attr map expects tags under "tags" key, but HCL has direct tags block.
	// This basic parser doesn't handle nested blocks like `tags = { ... }`.

	// Ensure domain keys for ID/ARN/Name are absent or explicitly nil if not found literally
	targetAttrs[domain.KeyID] = nil
	targetAttrs[domain.KeyARN] = nil
	if _, nameExists := targetAttrs[domain.KeyName]; !nameExists {
		targetAttrs[domain.KeyName] = nil // Ensure it's nil if not found via tags
	}

	return &tfHCLResource{
		meta: meta,
		attr: targetAttrs, // Contains only normalized *literal* attributes
	}, nil
}
