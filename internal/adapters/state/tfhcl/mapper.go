package tfhcl

import (
	"fmt"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/mapping"
	"strings"

	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

// tfHCLResource struct remains the same
type tfHCLResource struct {
	meta domain.ResourceMetadata
	attr map[string]any
}

func (r *tfHCLResource) Metadata() domain.ResourceMetadata { return r.meta }
func (r *tfHCLResource) Attributes() map[string]any        { return r.attr }

// mapEvaluatedHCLToDomain takes the *evaluated* attributes map and maps/normalizes it.
func mapEvaluatedHCLToDomain(
	kind domain.ResourceKind,
	address string, // TF Address (e.g., aws_instance.web)
	evaluatedAttrs map[string]any, // Map returned by the evaluator
) (domain.StateResource, error) {

	fmt.Printf("DEBUG: mapEvaluatedHCLToDomain called for %s (kind: %s) with %d attributes\n", address, kind, len(evaluatedAttrs))

	if evaluatedAttrs == nil {
		evaluatedAttrs = make(map[string]any) // Handle case of no attributes evaluated
		fmt.Printf("DEBUG: evaluatedAttrs was nil, created empty map\n")
	}

	// Debug available attributes
	for k, v := range evaluatedAttrs {
		fmt.Printf("DEBUG: Attribute %s: %T = %v\n", k, v, v)
	}

	targetAttrs := make(map[string]any)
	// Use the *same* normalization logic as tfstate adapter for consistency
	// Pass the evaluated HCL attributes as the 'rawAttrs'
	err := mapping.NormalizeAndCopyAttributes(kind, evaluatedAttrs, targetAttrs)
	if err != nil {
		fmt.Printf("DEBUG: NormalizeAndCopyAttributes failed: %v\n", err)
		return nil, errors.Wrap(err, errors.CodeInternal, fmt.Sprintf("failed normalizing evaluated HCL attributes for %s", address))
	}
	fmt.Printf("DEBUG: NormalizeAndCopyAttributes succeeded, produced %d target attributes\n", len(targetAttrs))

	// Metadata
	// Determine provider type based on address prefix (best effort)
	providerType := ""
	parts := strings.SplitN(address, "_", 2)
	if len(parts) > 0 {
		providerType = parts[0]
	} // e.g., "aws" from "aws_instance"
	fmt.Printf("DEBUG: Determined provider type: %s\n", providerType)

	meta := domain.ResourceMetadata{
		Kind:             kind,
		ProviderType:     providerType,
		SourceIdentifier: address,
		// Other metadata fields (ProviderAssignedID, Region, AccountID) are N/A from HCL
	}

	// Ensure domain keys (ID, ARN, Name) are explicitly nil if not present
	// The normalization function might set Name from tags if present.
	if _, exists := targetAttrs[domain.KeyID]; !exists {
		targetAttrs[domain.KeyID] = nil
	}
	if _, exists := targetAttrs[domain.KeyARN]; !exists {
		targetAttrs[domain.KeyARN] = nil
	}
	if _, exists := targetAttrs[domain.KeyName]; !exists {
		targetAttrs[domain.KeyName] = nil
	}

	return &tfHCLResource{
		meta: meta,
		attr: targetAttrs,
	}, nil
}
