// --- START OF FILE infra-drift-detector/internal/adapters/state/tfhcl/mapper.go ---

package tfhcl

import (
	"fmt"
	"strings"

	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/mapping"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl/evaluator"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	apperrors "github.com/olusolaa/infra-drift-detector/internal/errors"
)

type tfHCLResource struct {
	meta domain.ResourceMetadata
	attr map[string]any
}

func (r *tfHCLResource) Metadata() domain.ResourceMetadata { return r.meta }
func (r *tfHCLResource) Attributes() map[string]any {
	attrCopy := make(map[string]any, len(r.attr))
	for k, v := range r.attr {
		attrCopy[k] = v
	}
	return attrCopy
}

func MapEvaluatedHCLToDomain(
	kind domain.ResourceKind,
	address string,
	evaluatedAttrs evaluator.EvaluatedResource,
) (domain.StateResource, error) {

	if evaluatedAttrs == nil {
		evaluatedAttrs = make(evaluator.EvaluatedResource)
	}

	targetAttrs := make(map[string]any)
	err := mapping.NormalizeAndCopyAttributes(kind, evaluatedAttrs, targetAttrs)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.CodeMappingError, fmt.Sprintf("failed normalizing evaluated HCL attributes for %s", address))
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

	return &tfHCLResource{
		meta: meta,
		attr: targetAttrs,
	}, nil
}
