// internal/adapters/state/tfstate/resource_mapping.go
package tfstate

import (
	"fmt"
	"strings"

	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/mapping"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

/*
   ──────────────────────────────────────────────────────────────────────────────
   Thin wrapper that turns a *raw‑state* resource + instance into the project’s
   domain‑level StateResource.

   The raw structures come from parser.go:

     type Resource  { Mode, Type, Name, Provider string; Instances []Instance }
     type Instance  { Attributes map[string]any … }

   We expose only what drift‑detection needs: metadata + normalised attributes.
   ──────────────────────────────────────────────────────────────────────────────
*/

// tfStateResource satisfies domain.StateResource.
type tfStateResource struct {
	meta domain.ResourceMetadata
	attr map[string]any
}

func (r *tfStateResource) Metadata() domain.ResourceMetadata { return r.meta }

func (r *tfStateResource) Attributes() map[string]any {
	dup := make(map[string]any, len(r.attr))
	for k, v := range r.attr {
		dup[k] = v
	}
	return dup
}

// mapRawInstanceToDomain converts a single *Instance* inside a *Resource*.
func mapRawInstanceToDomain(
	res *Resource,  // parent resource block
	inst *Instance, // concrete instance
	logger ports.Logger,
) (domain.StateResource, error) {
	if res == nil || inst == nil {
		return nil, errors.New(errors.CodeInternal, "nil terraform state resource/instance")
	}

	log := logger.WithFields(map[string]any{
		"tf_type":  res.Type,
		"tf_name":  res.Name,
		"provider": res.Provider,
	})

	// ── kind ───────────────────────────────────────────────────────────────────
	kind, err := mapping.MapTfTypeToDomainKind(res.Type)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal,
			fmt.Sprintf("unsupported terraform type %q", res.Type))
	}

	// ── attributes ─────────────────────────────────────────────────────────────
	rawAttrs := inst.Attributes
	if rawAttrs == nil {
		rawAttrs = map[string]any{}
	}

	targetAttrs := make(map[string]any)
	if err := mapping.NormalizeAndCopyAttributes(kind, rawAttrs, targetAttrs); err != nil {
		return nil, errors.Wrap(err, errors.CodeMappingError,
			fmt.Sprintf("normalising attributes for %s.%s", res.Type, res.Name))
	}

	// ── provider‑specific ID (optional) ────────────────────────────────────────
	var providerAssignedID string
	if id, ok := targetAttrs[domain.KeyID].(string); ok {
		providerAssignedID = id
	}

	// ── provider type (e.g. "aws") ────────────────────────────────────────────
	providerType, _ := mapProviderToType(res.Provider)

	// raw state has no single "address" string; fabricate one:
	address := buildResourceAddress(res)

	meta := domain.ResourceMetadata{
		Kind:               kind,
		ProviderType:       providerType,
		ProviderAssignedID: providerAssignedID,
		SourceIdentifier:   address,
	}

	log.Debugf(nil, "mapped terraform resource to domain object")
	return &tfStateResource{meta: meta, attr: targetAttrs}, nil
}

// mapProviderToType extracts the provider’s short name from a raw provider
// address such as `provider["registry.terraform.io/hashicorp/aws"]`.
func mapProviderToType(addr string) (string, error) {
	if addr == "" {
		return "unknown", errors.New(errors.CodeInternal, "provider address is empty")
	}
	// registry.terraform.io/hashicorp/aws  →  aws
	parts := strings.Split(addr, "/")
	last := parts[len(parts)-1]
	if last == "" {
		return "unknown", errors.New(errors.CodeInternal,
			fmt.Sprintf("invalid provider address %q", addr))
	}
	return last, nil
}
