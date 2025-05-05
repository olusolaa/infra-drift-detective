package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
	"github.com/olusolaa/infra-drift-detector/internal/resources/helper"
	localconvert "github.com/olusolaa/infra-drift-detector/internal/resources/helper/convert"
	"github.com/olusolaa/infra-drift-detector/pkg/compare"
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
		desiredSlice, err = localconvert.ToSliceOfMap(desired)
		if err != nil {
			return false, "Invalid desired ACL slice type", err
		}
	}
	if aExists && actual != nil {
		actualSlice, err = localconvert.ToSliceOfMap(actual)
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
	// Normalize the lifecycle rules to account for Terraform structure vs AWS structure
	normalizedDesired, err := normalizeLifecycleRules(desired)
	if err != nil {
		return false, fmt.Sprintf("Failed to normalize desired lifecycle rules: %v", err), err
	}

	normalizedActual, err := normalizeLifecycleRules(actual)
	if err != nil {
		return false, fmt.Sprintf("Failed to normalize actual lifecycle rules: %v", err), err
	}

	return helper.CompareSliceOfMapsUnordered(ctx, normalizedDesired, normalizedActual, dExists, aExists, "id", "Lifecycle Rule")
}

// normalizeLifecycleRules transforms lifecycle rules to a consistent format for comparison
func normalizeLifecycleRules(input any) ([]map[string]any, error) {
	if input == nil {
		return nil, nil
	}

	rulesSlice, err := localconvert.ToSliceOfMap(input)
	if err != nil {
		return nil, err
	}

	result := make([]map[string]any, 0, len(rulesSlice))
	for _, rule := range rulesSlice {
		normalized := make(map[string]any)

		if id, exists := rule["id"]; exists {
			normalized["id"] = id
		}
		if status, exists := rule["status"]; exists {
			normalized["status"] = status
		}

		if expiration, exists := rule["expiration"]; exists {
			if expArr, ok := expiration.([]any); ok && len(expArr) > 0 {
				if expMap, ok := expArr[0].(map[string]any); ok {
					normalized["expiration"] = map[string]any{
						"days": expMap["days"],
					}
				}
			} else if expMap, ok := expiration.(map[string]any); ok {
				// Already in AWS format
				normalized["expiration"] = expMap
			}
		}

		if filter, exists := rule["filter"]; exists {
			if filterArr, ok := filter.([]any); ok && len(filterArr) > 0 {
				if filterMap, ok := filterArr[0].(map[string]any); ok {
					normalized["filter"] = map[string]any{
						"prefix": filterMap["prefix"],
					}
				}
			} else if filterMap, ok := filter.(map[string]any); ok {
				// Already in AWS format
				normalized["filter"] = filterMap
			}
		}

		result = append(result, normalized)
	}

	return result, nil
}

func (c *BucketComparer) normalizeCorsRule(ctx context.Context, rule map[string]any) (map[string]any, error) {
	normalized := make(map[string]any)
	for k, v := range rule {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		switch k {
		case "allowed_headers", "allowed_methods", "allowed_origins", "expose_headers":
			strSlice, err := localconvert.ToSliceOfString(v)
			if err != nil {
				normalized[k] = v
			} else {
				sort.Strings(strSlice)
				normalized[k] = strSlice
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
		desiredSlice, err = localconvert.ToSliceOfMap(desired)
		if err != nil {
			return false, "Invalid desired CORS rules slice type", err
		}
	}
	if aExists && actual != nil {
		actualSlice, err = localconvert.ToSliceOfMap(actual)
		if err != nil {
			return false, "Invalid actual CORS rules slice type", err
		}
	}

	if len(desiredSlice) == 0 && len(actualSlice) == 0 {
		return true, "", nil
	}

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
			}

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
	}

	actualRuleSet, aErr := normalizeAndSerialize(actualSlice)
	if aErr != nil {
		return false, fmt.Sprintf("Error processing actual CORS rules: %v", aErr), aErr
	}
	if ctx.Err() != nil {
		return false, "", ctx.Err()
	}

	if len(desiredRuleSet) != len(actualRuleSet) {
		return false, fmt.Sprintf("Number of unique CORS rules differ (desired: %d, actual: %d)", len(desiredRuleSet), len(actualRuleSet)), nil
	}

	for key := range desiredRuleSet {
		if ctx.Err() != nil {
			return false, "", ctx.Err()
		}
		if _, exists := actualRuleSet[key]; !exists {
			var ruleContent map[string]any
			_ = json.Unmarshal([]byte(key), &ruleContent)
			return false, fmt.Sprintf("CORS rule content mismatch (rule %v present in desired, missing in actual)", ruleContent), nil
		}
	}

	return true, "", nil
}

func (c *BucketComparer) comparePolicy(ctx context.Context, desired, actual any, dExists, aExists bool) (bool, string, error) {
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

		details := compare.GenerateDetailedMapDiff(ctx, dMap, aMap)
		if ctx.Err() != nil {
			return false, "", ctx.Err()
		}

		isEqual := details == ""
		return isEqual, details, nil
	}
}

func (c *BucketComparer) compareEncryption(ctx context.Context, desired, actual any, dExists, aExists bool) (bool, string, error) {
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
			}
			return nil, true
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

	details := compare.GenerateDetailedMapDiff(ctx, desiredRule, actualRule)
	if ctx.Err() != nil {
		return false, "", ctx.Err()
	}

	isEqual := details == ""
	return isEqual, details, nil
}
