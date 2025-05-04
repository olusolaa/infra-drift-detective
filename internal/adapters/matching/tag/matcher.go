package tag

import (
	"context"
	"strings"

	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

const MatcherTypeTag = "tag"

type Config struct {
	// TagKey specifies the tag key used to store the unique source identifier
	// (e.g., Terraform address) on the actual cloud resource.
	TagKey string `yaml:"key"`
}

type Matcher struct {
	config Config
	logger ports.Logger
}

func NewMatcher(cfg Config, logger ports.Logger) (*Matcher, error) {
	if cfg.TagKey == "" {
		return nil, errors.New(errors.CodeConfigValidation, "tag matcher requires a non-empty tag key configuration")
	}
	// Tag keys starting with 'aws:' are typically reserved. Add validation?
	if strings.HasPrefix(strings.ToLower(cfg.TagKey), "aws:") {
		logger.Warnf(context.Background(), "Configured tag key '%s' starts with 'aws:', which might conflict with reserved AWS tags", cfg.TagKey)
	}
	return &Matcher{
		config: cfg,
		logger: logger,
	}, nil
}

func (m *Matcher) Match(
	ctx context.Context,
	desired []domain.StateResource,
	actual []domain.PlatformResource,
) (ports.MatchingResult, error) {

	m.logger.Debugf(ctx, "Starting tag matching with key '%s' (%d desired, %d actual)", m.config.TagKey, len(desired), len(actual))

	result := ports.MatchingResult{
		Matched:          make([]ports.MatchedPair, 0),
		UnmatchedDesired: make([]domain.StateResource, 0),
		UnmatchedActual:  make([]domain.PlatformResource, 0),
	}

	actualIndex := make(map[string]domain.PlatformResource)
	actualProcessed := make(map[string]bool)

	for _, res := range actual {
		if ctx.Err() != nil {
			return ports.MatchingResult{}, ctx.Err()
		}

		meta := res.Metadata()
		actualProcessed[meta.ProviderAssignedID] = false

		attrs, err := res.Attributes(ctx)
		if err != nil {
			m.logger.Debugf(ctx, "Failed to get attributes for actual resource %s (%s): %v", meta.ProviderAssignedID, meta.Kind, err)
			continue
		}
		tagsVal, ok := attrs[domain.KeyTags].(map[string]string)
		if !ok {
			m.logger.Debugf(ctx, "Actual resource %s (%s) missing tags attribute or not a map[string]string, cannot use for tag matching", meta.ProviderAssignedID, meta.Kind)
			continue
		}

		identifierTagValue, found := tagsVal[m.config.TagKey]
		if !found || identifierTagValue == "" {
			m.logger.Debugf(ctx, "Actual resource %s (%s) does not have the configured tag key '%s' or its value is empty", meta.ProviderAssignedID, meta.Kind, m.config.TagKey)
			continue
		}

		if existing, exists := actualIndex[identifierTagValue]; exists {
			existingMeta := existing.Metadata()
			m.logger.Errorf(ctx, nil, "Duplicate tag value '%s' found on actual resources: %s (%s) and %s (%s). Only one will be matched.",
				identifierTagValue, meta.ProviderAssignedID, meta.Kind, existingMeta.ProviderAssignedID, existingMeta.Kind)
			continue
		}
		actualIndex[identifierTagValue] = res
	}

	m.logger.Debugf(ctx, "Built index of %d actual resources based on tag '%s'", len(actualIndex), m.config.TagKey)

	desiredProcessed := make(map[string]bool)

	for _, desRes := range desired {
		if ctx.Err() != nil {
			return ports.MatchingResult{}, ctx.Err()
		}

		desMeta := desRes.Metadata()
		sourceID := desMeta.SourceIdentifier

		if sourceID == "" {
			m.logger.Warnf(ctx, "Desired resource of kind %s has empty SourceIdentifier, cannot match via tag", desMeta.Kind)
			result.UnmatchedDesired = append(result.UnmatchedDesired, desRes)
			continue
		}

		if _, processed := desiredProcessed[sourceID]; processed {
			m.logger.Errorf(ctx, nil, "Duplicate desired resource identifier '%s' found. Skipping duplicate.", sourceID)
			continue
		}
		desiredProcessed[sourceID] = true

		actualRes, found := actualIndex[sourceID]
		if !found {
			result.UnmatchedDesired = append(result.UnmatchedDesired, desRes)
			continue
		}

		actualMeta := actualRes.Metadata()
		result.Matched = append(result.Matched, ports.MatchedPair{
			Desired: desRes,
			Actual:  actualRes,
		})
		actualProcessed[actualMeta.ProviderAssignedID] = true

		m.logger.Debugf(ctx, "Matched desired '%s' to actual '%s' via tag '%s'", sourceID, actualMeta.ProviderAssignedID, m.config.TagKey)
	}

	for _, actRes := range actualIndex {
		actMeta := actRes.Metadata()
		if processed, exists := actualProcessed[actMeta.ProviderAssignedID]; exists && !processed {
			result.UnmatchedActual = append(result.UnmatchedActual, actRes)
		}
	}

	result.UnmatchedActual = make([]domain.PlatformResource, 0)
	for _, actRes := range actual {
		actMeta := actRes.Metadata()
		if processed, exists := actualProcessed[actMeta.ProviderAssignedID]; !exists || !processed {
			result.UnmatchedActual = append(result.UnmatchedActual, actRes)
		}
	}

	m.logger.Debugf(ctx, "Tag matching finished: %d matched, %d missing, %d unmanaged", len(result.Matched), len(result.UnmatchedDesired), len(result.UnmatchedActual))
	return result, nil
}
