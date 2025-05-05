package tfstate

import (
	"fmt"
	"strings"

	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/mapping"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

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

func mapRawInstanceToDomain(
	res *Resource,
	inst *Instance,
	logger ports.Logger,
	state *State,
) (domain.StateResource, error) {
	if res == nil || inst == nil {
		return nil, errors.New(errors.CodeInternal, "nil terraform state resource/instance")
	}

	log := logger.WithFields(map[string]any{
		"tf_type":  res.Type,
		"tf_name":  res.Name,
		"provider": res.Provider,
	})

	kind, err := mapping.MapTfTypeToDomainKind(res.Type)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal,
			fmt.Sprintf("unsupported terraform type %q", res.Type))
	}

	rawAttrs := inst.Attributes
	if rawAttrs == nil {
		rawAttrs = map[string]any{}
	}

	targetAttrs := make(map[string]any)
	if err := mapping.NormalizeAndCopyAttributes(kind, rawAttrs, targetAttrs); err != nil {
		return nil, errors.Wrap(err, errors.CodeMappingError,
			fmt.Sprintf("normalising attributes for %s.%s", res.Type, res.Name))
	}

	if state != nil && (kind == domain.KindStorageBucket) {
		processRelatedResources(state, res, kind, targetAttrs, logger)
	}

	var providerAssignedID string
	if id, ok := targetAttrs[domain.KeyID].(string); ok {
		providerAssignedID = id
	}

	providerType, _ := mapProviderToType(res.Provider)

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

func processRelatedResources(state *State, baseResource *Resource, kind domain.ResourceKind, targetAttrs map[string]any, logger ports.Logger) {
	relatedResources := FindRelatedResources(state, baseResource)
	if len(relatedResources) == 0 {
		return
	}

	switch kind {
	case domain.KindStorageBucket:
		processS3RelatedResources(relatedResources, targetAttrs, logger)
	}
}

func processS3RelatedResources(relatedResources map[string][]*Resource, targetAttrs map[string]any, logger ports.Logger) {
	if lifecycleConfigs, ok := relatedResources["lifecycle_configuration"]; ok && len(lifecycleConfigs) > 0 {
		for _, lifecycleRes := range lifecycleConfigs {
			if len(lifecycleRes.Instances) == 0 || lifecycleRes.Instances[0].Attributes == nil {
				continue
			}

			rulesAttr, exists := lifecycleRes.Instances[0].Attributes["rule"]
			if !exists {
				continue
			}

			targetAttrs[domain.StorageBucketLifecycleRulesKey] = rulesAttr
			logger.Debugf(nil, "merged lifecycle rules from related resource %s", lifecycleRes.Type+"."+lifecycleRes.Name)
		}
	}

}

func mapProviderToType(addr string) (string, error) {
	if addr == "" {
		return "unknown", errors.New(errors.CodeInternal, "provider address is empty")
	}
	parts := strings.Split(addr, "/")
	last := parts[len(parts)-1]
	if last == "" {
		return "unknown", errors.New(errors.CodeInternal,
			fmt.Sprintf("invalid provider address %q", addr))
	}
	return last, nil
}
