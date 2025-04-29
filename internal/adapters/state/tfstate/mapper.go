package tfstate

import (
	"fmt"
	"github.com/go-viper/mapstructure/v2"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/mapping"
	"strings"

	terraformjson "github.com/hashicorp/terraform-json"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

// tfStateResource struct remains the same
type tfStateResource struct {
	meta domain.ResourceMetadata
	attr map[string]any
}

func (r *tfStateResource) Metadata() domain.ResourceMetadata { return r.meta }
func (r *tfStateResource) Attributes() map[string]any        { return r.attr }

// mapStateResourceToDomain uses the centralized mappings.
func mapStateResourceToDomain(stateRes *terraformjson.StateResource) (domain.StateResource, error) {
	if stateRes == nil {
		return nil, errors.New(errors.CodeInternal, "cannot map nil state resource")
	}

	kind, err := mapping.MapTfTypeToDomainKind(stateRes.Type)
	if err != nil {
		// Log? Skip? This indicates an internal inconsistency if findResourcesInState allowed it.
		return nil, errors.Wrap(err, errors.CodeInternal, "mapping inconsistent state resource type")
	}

	// Use mapstructure for safer access to potentially complex nested attributes
	// Decode AttributeValues into a map[string]any first
	var rawAttrs map[string]any
	// mapstructure configuration can be added for more complex decoding if needed
	// e.g., handling time formats, custom decoders
	decoderConfig := &mapstructure.DecoderConfig{
		Result:           &rawAttrs,
		WeaklyTypedInput: true,   // Allow some flexibility in types from JSON
		TagName:          "json", // Use json tags if needed, though state usually doesn't have them
	}
	decoder, err := mapstructure.NewDecoder(decoderConfig)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "failed to create mapstructure decoder")
	}
	if stateRes.AttributeValues != nil {
		err = decoder.Decode(stateRes.AttributeValues)
		if err != nil {
			return nil, errors.Wrap(err, errors.CodeStateParseError, fmt.Sprintf("failed decoding resource %s attributes: %v", stateRes.Address, err))
		}
	} else {
		// Handle resources with potentially null attributes (though rare for managed resources)
		rawAttrs = make(map[string]any)
	}

	// Perform normalization and copy using centralized logic
	targetAttrs := make(map[string]any)
	err = mapping.NormalizeAndCopyAttributes(kind, rawAttrs, targetAttrs) // Uses mappings.go logic
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, fmt.Sprintf("failed normalizing attributes for %s", stateRes.Address))
	}

	// Extract common metadata fields from normalized attributes
	providerAssignedID := ""
	if idVal, ok := targetAttrs[domain.KeyID].(string); ok {
		providerAssignedID = idVal
	}
	// Region might be available at a higher level in state or provider config, not always per-resource attribute
	region := ""

	meta := domain.ResourceMetadata{
		Kind:               kind,
		ProviderType:       mapProviderToType(stateRes.ProviderName),
		ProviderAssignedID: providerAssignedID,
		SourceIdentifier:   stateRes.Address,
		Region:             region,
	}

	return &tfStateResource{
		meta: meta,
		attr: targetAttrs, // Use the normalized attributes
	}, nil
}

// mapProviderToType remains the same
func mapProviderToType(providerAddr string) string {
	// ... (Implementation as before) ... //
	parts := strings.Split(providerAddr, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}
	return "unknown"
}
