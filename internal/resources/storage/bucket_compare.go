package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/olusolaa/infra-drift-detector/pkg/compare"
	"github.com/olusolaa/infra-drift-detector/pkg/convert"
	"sort"

	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
	"github.com/olusolaa/infra-drift-detector/internal/resources/helper"
)

type BucketComparer struct {
	compareFuncs map[string]helper.AttributeComparerFunc
}

func NewBucketComparer() *BucketComparer {
	c := &BucketComparer{}
	c.compareFuncs = map[string]helper.AttributeComparerFunc{
		domain.KeyTags:                        c.compareTags,
		domain.StorageBucketACLKey:            c.compareACLGransts,
		domain.StorageBucketLifecycleRulesKey: c.compareLifecycleRules,
		domain.StorageBucketCorsRulesKey:      c.compareCorsRules,
		domain.StorageBucketPolicyKey:         c.comparePolicy,
		domain.StorageBucketLoggingKey:        c.compareSimpleBlockMap("Logging"),
		domain.StorageBucketWebsiteKey:        c.compareSimpleBlockMap("Website"),
		domain.StorageBucketEncryptionKey:     c.compareEncryption,
		domain.StorageBucketVersioningKey:     helper.DefaultAttributeCompare, // Bool comparison is fine
	}
	return c
}

func (c *BucketComparer) Kind() domain.ResourceKind {
	return domain.KindStorageBucket
}

func (c *BucketComparer) Compare(
	ctx context.Context,
	desired domain.StateResource,
	actual domain.PlatformResource,
	attributesToCheck []string,
) ([]domain.AttributeDiff, error) {
	if desired == nil || actual == nil {
		return nil, errors.New(errors.CodeInternal, "S3 Compare called with nil desired or actual resource")
	}

	desiredAttrs := desired.Attributes()
	actualAttrs, err := actual.Attributes(ctx)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeInternal, "failed to get attributes from actual resource")
	}
	diffs := make([]domain.AttributeDiff, 0)

	for _, attrKey := range attributesToCheck {
		// Check context at the beginning of each attribute comparison
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		desiredVal, dExists := desiredAttrs[attrKey]
		actualVal, aExists := actualAttrs[attrKey]

		var isEqual bool
		var details string
		var compareErr error

		if compareFunc, ok := c.compareFuncs[attrKey]; ok {
			isEqual, details, compareErr = compareFunc(ctx, desiredVal, actualVal, dExists, aExists)
		} else {
			isEqual, details, compareErr = helper.DefaultAttributeCompare(ctx, desiredVal, actualVal, dExists, aExists)
		}

		// Check context again after potentially long comparison function
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		if compareErr != nil {
			diffs = append(diffs, domain.AttributeDiff{
				AttributeName: attrKey, ExpectedValue: desiredVal, ActualValue: actualVal,
				Details: fmt.Sprintf("Comparison error: %v", compareErr),
			})
			continue
		}

		if !isEqual {
			diffs = append(diffs, domain.AttributeDiff{
				AttributeName: attrKey,
				ExpectedValue: desiredVal,
				ActualValue:   actualVal,
				Details:       details,
			})
		}
	}

	return diffs, nil
}

func (c *BucketComparer) compareTags(ctx context.Context, desired, actual any, dExists, aExists bool) (bool, string, error) {
	// Use helper, providing the AWS-specific prefix to ignore
	return helper.CompareTags(ctx, desired, actual, dExists, aExists, "aws:")
}

func (c *BucketComparer) compareACLGransts(ctx context.Context, desired, actual any, dExists, aExists bool) (bool, string, error) {
	if !dExists && !aExists {
		return true, "", nil
	}

	var desiredSlice, actualSlice []map[string]any
	var err error
	if dExists && desired != nil {
		desiredSlice, err = convert.ToSliceOfMap(desired)
		if err != nil {
			return false, "Invalid desired ACL slice type", err
		}
	}
	if aExists && actual != nil {
		actualSlice, err = convert.ToSliceOfMap(actual)
		if err != nil {
			return false, "Invalid actual ACL slice type", err
		}
	}

	if len(desiredSlice) == 0 && len(actualSlice) == 0 {
		return true, "", nil
	}

	getGrantKey := func(grant map[string]any) (string, bool) {
		granteeType, _ := grant["type"].(string)
		permission, _ := grant["permission"].(string)
		id, _ := grant["id"].(string)
		uri, _ := grant["uri"].(string)
		if granteeType == "" || permission == "" {
			return "", false
		}
		identifier := id
		if granteeType == "Group" && uri != "" {
			identifier = uri
		}
		if identifier == "" && granteeType != "CanonicalUser" {
			return "", false
		} // Need ID/URI unless it's the owner
		return fmt.Sprintf("%s::%s::%s", granteeType, identifier, permission), true
	}

	desiredMap := make(map[string]map[string]any)
	duplicateDesiredKeys := false
	for _, grant := range desiredSlice {
		if ctx.Err() != nil {
			return false, "", ctx.Err()
		}
		key, ok := getGrantKey(grant)
		if !ok {
			continue
		}
		if _, exists := desiredMap[key]; exists {
			duplicateDesiredKeys = true
			break
		}
		desiredMap[key] = grant
	}
	if duplicateDesiredKeys {
		return false, "Duplicate effective grants found in desired state", errors.New(errors.CodeComparisonError, "duplicate desired grants")
	}

	actualMap := make(map[string]map[string]any)
	for _, grant := range actualSlice {
		if ctx.Err() != nil {
			return false, "", ctx.Err()
		}
		key, ok := getGrantKey(grant)
		if !ok {
			continue
		}
		actualMap[key] = grant
	}

	if len(desiredMap) != len(actualMap) {
		return false, fmt.Sprintf("ACL grant counts differ (desired: %d, actual: %d)", len(desiredMap), len(actualMap)), nil
	}

	for key := range desiredMap {
		if ctx.Err() != nil {
			return false, "", ctx.Err()
		}
		_, exists := actualMap[key]
		if !exists {
			return false, fmt.Sprintf("ACL grant differences found (e.g., missing grant for '%s')", key), nil
		}
		// ACL comparison is primarily about the presence/absence of the effective permission for the grantee
		// We don't need to deep-compare the grant maps themselves if the keys match.
	}

	return true, "", nil
}

func (c *BucketComparer) compareLifecycleRules(ctx context.Context, desired, actual any, dExists, aExists bool) (bool, string, error) {
	// Use generic unordered map slice comparison helper
	return helper.CompareSliceOfMapsUnordered(ctx, desired, actual, dExists, aExists, "id", "Lifecycle Rule")
}

func (c *BucketComparer) normalizeCorsRule(ctx context.Context, rule map[string]any) (map[string]any, error) {
	normalized := make(map[string]any)
	for k, v := range rule {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		switch k {
		case "allowed_headers", "allowed_methods", "allowed_origins", "expose_headers":
			// Convert to slice of strings and sort
			strSlice, err := convert.ToSliceOfString(v)
			if err != nil {
				// If conversion fails, keep original value but maybe log? Or return error?
				// Returning error might be too strict if API returns unexpected type. Let's keep original.
				normalized[k] = v
			} else {
				sort.Strings(strSlice)
				normalized[k] = strSlice // Store the sorted string slice
			}
		default:
			normalized[k] = v
		}
	}
	return normalized, nil
}

func (c *BucketComparer) compareCorsRules(ctx context.Context, desired, actual any, dExists, aExists bool) (bool, string, error) {
	if !dExists && !aExists {
		return true, "", nil
	}

	var desiredSlice, actualSlice []map[string]any
	var err error
	if dExists && desired != nil {
		desiredSlice, err = convert.ToSliceOfMap(desired)
		if err != nil {
			return false, "Invalid desired CORS rules slice type", err
		}
	}
	if aExists && actual != nil {
		actualSlice, err = convert.ToSliceOfMap(actual)
		if err != nil {
			return false, "Invalid actual CORS rules slice type", err
		}
	}

	if len(desiredSlice) == 0 && len(actualSlice) == 0 {
		return true, "", nil
	}

	// Normalize and serialize each rule to create a comparable representation
	normalizeAndSerialize := func(rules []map[string]any) (map[string]map[string]any, error) {
		serializedMap := make(map[string]map[string]any)
		for _, rule := range rules {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			normalizedRule, normErr := c.normalizeCorsRule(ctx, rule)
			if normErr != nil {
				return nil, fmt.Errorf("failed to normalize CORS rule: %w", normErr)
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			} // Check after normalization

			jsonBytes, jsonErr := json.Marshal(normalizedRule)
			if jsonErr != nil {
				return nil, fmt.Errorf("failed to serialize normalized CORS rule to JSON key: %w", jsonErr)
			}
			key := string(jsonBytes)
			if _, exists := serializedMap[key]; exists {
				return nil, fmt.Errorf("duplicate CORS rule content detected after normalization")
			}
			serializedMap[key] = normalizedRule
		}
		return serializedMap, nil
	}

	desiredRuleSet, dErr := normalizeAndSerialize(desiredSlice)
	if dErr != nil {
		return false, fmt.Sprintf("Error processing desired CORS rules: %v", dErr), dErr
	}
	if ctx.Err() != nil {
		return false, "", ctx.Err()
	} // Check context

	actualRuleSet, aErr := normalizeAndSerialize(actualSlice)
	if aErr != nil {
		return false, fmt.Sprintf("Error processing actual CORS rules: %v", aErr), aErr
	}
	if ctx.Err() != nil {
		return false, "", ctx.Err()
	} // Check context

	// Compare the sets of serialized rules
	if len(desiredRuleSet) != len(actualRuleSet) {
		return false, fmt.Sprintf("Number of unique CORS rules differ (desired: %d, actual: %d)", len(desiredRuleSet), len(actualRuleSet)), nil
	}

	for key := range desiredRuleSet {
		if ctx.Err() != nil {
			return false, "", ctx.Err()
		}
		if _, exists := actualRuleSet[key]; !exists {
			var ruleContent map[string]any
			_ = json.Unmarshal([]byte(key), &ruleContent) // Best effort to show content
			return false, fmt.Sprintf("CORS rule content mismatch (rule %v present in desired, missing in actual)", ruleContent), nil
		}
	}

	return true, "", nil
}

func (c *BucketComparer) comparePolicy(ctx context.Context, desired, actual any, dExists, aExists bool) (bool, string, error) {
	// Use generic JSON string comparison helper
	return helper.CompareJSONStrings(ctx, desired, actual, dExists, aExists, "Policy")
}

func (c *BucketComparer) compareSimpleBlockMap(blockName string) helper.AttributeComparerFunc {
	return func(ctx context.Context, desired, actual any, dExists, aExists bool) (bool, string, error) {
		if !dExists && !aExists {
			return true, "", nil
		}
		if !dExists {
			return false, fmt.Sprintf("%s configuration exists only in actual state", blockName), nil
		}
		if !aExists {
			return false, fmt.Sprintf("%s configuration exists only in desired state", blockName), nil
		}

		dMap, dOk := desired.(map[string]any)
		aMap, aOk := actual.(map[string]any)

		if !dOk || !aOk {
			return helper.DefaultAttributeCompare(ctx, desired, actual, dExists, aExists)
		}
		if dMap == nil && aMap == nil {
			return true, "", nil
		}
		if dMap == nil {
			return false, fmt.Sprintf("%s configuration mismatch (desired is nil)", blockName), nil
		}
		if aMap == nil {
			return false, fmt.Sprintf("%s configuration mismatch (actual is nil)", blockName), nil
		}

		// Use internal diff generator which respects context
		details := compare.GenerateDetailedMapDiff(ctx, dMap, aMap)
		if ctx.Err() != nil {
			return false, "", ctx.Err()
		}

		isEqual := details == ""
		return isEqual, details, nil
	}
}

func (c *BucketComparer) compareEncryption(ctx context.Context, desired, actual any, dExists, aExists bool) (bool, string, error) {
	// Extracts the effective encryption rule map from potentially nested structures
	extractRule := func(input any) (map[string]any, bool) {
		if input == nil {
			return nil, true
		}
		outerMap, ok := input.(map[string]any)
		if !ok {
			return nil, false
		}

		rulesAny, rulesExist := outerMap["rules"]
		if !rulesExist {
			rulesAny, rulesExist = outerMap["rule"]
		}
		if !rulesExist {
			_, hasApply := outerMap["apply_server_side_encryption_by_default"]
			_, hasBke := outerMap["bucket_key_enabled"]
			if hasApply || hasBke {
				return outerMap, true
			} // Assume input is the rule map itself
			return nil, true // No recognizable rule structure found
		}

		rulesSlice, ok := rulesAny.([]any)
		if !ok || len(rulesSlice) == 0 {
			return nil, true
		}
		firstRule, ok := rulesSlice[0].(map[string]any)
		if !ok {
			return nil, false
		}
		return firstRule, true
	}

	desiredRule, dOk := extractRule(desired)
	actualRule, aOk := extractRule(actual)

	if !dOk || !aOk {
		// Fallback if structure is unexpected
		return helper.DefaultAttributeCompare(ctx, desired, actual, dExists, aExists)
	}

	// Now compare the extracted rule maps
	if !dExists && !aExists {
		return true, "", nil
	}
	if desiredRule == nil && actualRule == nil {
		return true, "", nil
	}

	if (!dExists || desiredRule == nil) && (aExists && actualRule != nil) {
		return false, "Encryption configuration exists only in actual state", nil
	}
	if (dExists && desiredRule != nil) && (!aExists || actualRule == nil) {
		return false, "Encryption configuration exists only in desired state", nil
	}
	if !dExists || !aExists { // Should be caught above, but defense
		return false, "Encryption configuration exists only in one state", nil
	}

	// Both exist and are extracted, compare the rule maps using internal diff
	details := compare.GenerateDetailedMapDiff(ctx, desiredRule, actualRule)
	if ctx.Err() != nil {
		return false, "", ctx.Err()
	}

	isEqual := details == ""
	return isEqual, details, nil
}
