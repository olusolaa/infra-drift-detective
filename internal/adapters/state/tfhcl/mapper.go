package tfhcl

import (
	"fmt"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/mapping"
	"strings"

	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

type tfHCLResource struct {
	meta domain.ResourceMetadata
	attr map[string]any
}

func (r *tfHCLResource) Metadata() domain.ResourceMetadata { return r.meta }
func (r *tfHCLResource) Attributes() map[string]any        { return r.attr }

func MapEvaluatedHCLToDomain(
	kind domain.ResourceKind,
	address string,
	evaluatedAttrs map[string]any,
) (domain.StateResource, error) {

	if evaluatedAttrs == nil {
		evaluatedAttrs = make(map[string]any)
	}

	targetAttrs := make(map[string]any)
	// Use the *same* normalization logic, check for errors now
	err := mapping.NormalizeAndCopyAttributes(kind, evaluatedAttrs, targetAttrs)
	if err != nil {
		// Wrap the normalization error for context
		return nil, errors.Wrap(err, errors.CodeInternal, fmt.Sprintf("failed normalizing evaluated HCL attributes for %s", address))
	}

	providerType := ""
	parts := strings.SplitN(address, "_", 2)
	if len(parts) > 0 {
		providerType = parts[0]
	}

	meta := domain.ResourceMetadata{
		Kind:             kind,
		ProviderType:     providerType,
		SourceIdentifier: address,
	}

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
