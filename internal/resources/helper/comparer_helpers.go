package helper

import (
	"context"
	"fmt"
	"github.com/olusolaa/infra-drift-detector/pkg/compare"
	"github.com/olusolaa/infra-drift-detector/pkg/convert"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

// AttributeComparerFunc defines the signature for specific attribute comparison functions.
type AttributeComparerFunc func(ctx context.Context, desired, actual any, dExists, aExists bool) (isEqual bool, details string, err error)

// DefaultAttributeCompare uses the drift-specific RobustCompare.
func DefaultAttributeCompare(ctx context.Context, desired, actual any, dExists, aExists bool) (bool, string, error) {
	// Context is not directly used by RobustCompare but passed for consistency
	isEqual, err := compare.RobustCompare(desired, actual, dExists, aExists)
	details := ""
	if !isEqual && err == nil {
		details = "Values differ"
	} else if err != nil {
		details = fmt.Sprintf("Comparison error: %v", err)
	}
	return isEqual, details, err
}

// CompareTags uses generic conversion and internal diff generation, ignoring prefixes.
func CompareTags(ctx context.Context, desired, actual any, dExists, aExists bool, ignorePrefix string) (bool, string, error) {
	if !dExists && !aExists {
		return true, "", nil
	}

	dMap, err := convert.ToStringMap(desired)
	if err != nil {
		return false, "Invalid type for desired tags", errors.Wrap(err, errors.CodeComparisonError, "desired tags not map[string]string")
	}
	aMap, err := convert.ToStringMap(actual)
	if err != nil {
		return false, "Invalid type for actual tags", errors.Wrap(err, errors.CodeComparisonError, "actual tags not map[string]string")
	}

	if dMap == nil {
		dMap = map[string]string{}
	}
	if aMap == nil {
		aMap = map[string]string{}
	}

	filteredDesired := make(map[string]any)
	for k, v := range dMap {
		if ctx.Err() != nil {
			return false, "", ctx.Err()
		}
		if ignorePrefix == "" || !strings.HasPrefix(strings.ToLower(k), ignorePrefix) {
			filteredDesired[k] = v
		}
	}
	filteredActual := make(map[string]any)
	for k, v := range aMap {
		if ctx.Err() != nil {
			return false, "", ctx.Err()
		}
		if ignorePrefix == "" || !strings.HasPrefix(strings.ToLower(k), ignorePrefix) {
			filteredActual[k] = v
		}
	}

	details := compare.GenerateDetailedMapDiff(ctx, filteredDesired, filteredActual)
	if ctx.Err() != nil {
		return false, "", ctx.Err()
	}

	isEqual := details == ""
	if !isEqual && ignorePrefix != "" {
		details += fmt.Sprintf(" (Ignoring %s* keys)", ignorePrefix)
	}
	return isEqual, details, nil
}

// CompareStringSlicesUnordered adapts the generic pkgcompare.Sets for the AttributeComparerFunc signature.
func CompareStringSlicesUnordered(ctx context.Context, desired, actual any, dExists, aExists bool) (bool, string, error) {
	if !dExists && !aExists {
		return true, "", nil
	}
	if !dExists {
		return false, "Item missing in desired state", nil
	}
	if !aExists {
		return false, "Item missing in actual state", nil
	}

	dSlice, err := convert.ToSliceOfString(desired)
	if err != nil {
		return false, fmt.Sprintf("Cannot convert desired to string slice: %v", err), err
	}
	aSlice, err := convert.ToSliceOfString(actual)
	if err != nil {
		return false, fmt.Sprintf("Cannot convert actual to string slice: %v", err), err
	}

	// Use the generic Set equality checker
	isEqual, details := compare.Sets(dSlice, aSlice)
	// Check context error immediately after potential blocking call
	if ctx.Err() != nil {
		return false, "", ctx.Err()
	}
	return isEqual, details, nil // No specific error generation here, only conversion or context errors
}

// CompareJSONStrings adapts the generic pkgcompare.JSONStrings for the AttributeComparerFunc signature.
func CompareJSONStrings(ctx context.Context, desired, actual any, dExists, aExists bool, fieldName string) (bool, string, error) {
	if !dExists && !aExists {
		return true, "", nil
	}
	if !dExists {
		return false, fmt.Sprintf("%s missing in desired state", fieldName), nil
	}
	if !aExists {
		return false, fmt.Sprintf("%s missing in actual state", fieldName), nil
	}

	ds, dOk := desired.(string)
	as, aOk := actual.(string)
	if !dOk || !aOk {
		// Fallback uses RobustCompare which doesn't need context
		return DefaultAttributeCompare(ctx, desired, actual, dExists, aExists)
	}

	// Pass context to generic JSON string equality checker
	isEqual, details := compare.JSONStrings(ds, as)
	if !isEqual && details == "" {
		details = fmt.Sprintf("%s differ", fieldName)
	}
	return isEqual, details, nil
}

// CompareSliceOfMapsUnordered compares slices of maps using a key, generating detailed drift diffs.
func CompareSliceOfMapsUnordered(ctx context.Context, desired, actual any, dExists, aExists bool, keyField, itemType string) (bool, string, error) {
	if !dExists && !aExists {
		return true, "", nil
	}

	var desiredSlice, actualSlice []map[string]any
	var err error
	if dExists && desired != nil {
		desiredSlice, err = convert.ToSliceOfMap(desired)
		if err != nil {
			return false, fmt.Sprintf("Invalid desired %s slice type", itemType), err
		}
	}
	if aExists && actual != nil {
		actualSlice, err = convert.ToSliceOfMap(actual)
		if err != nil {
			return false, fmt.Sprintf("Invalid actual %s slice type", itemType), err
		}
	}

	if len(desiredSlice) == 0 && len(actualSlice) == 0 {
		return true, "", nil
	}

	// Build map from desired state for lookup
	desiredMap := make(map[string]map[string]any)
	duplicateDesiredKeys := make(map[string]bool)
	for _, item := range desiredSlice {
		if ctx.Err() != nil {
			return false, "", ctx.Err()
		}
		keyValStr, ok := getStringKey(item, keyField)
		if !ok {
			return false, "", errors.New(errors.CodeComparisonError, fmt.Sprintf("desired %s item missing valid string key field '%s'", itemType, keyField))
		}
		if _, exists := desiredMap[keyValStr]; exists {
			duplicateDesiredKeys[keyValStr] = true
		}
		desiredMap[keyValStr] = item
	}
	if len(duplicateDesiredKeys) > 0 {
		keys := make([]string, 0, len(duplicateDesiredKeys))
		for k := range duplicateDesiredKeys {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return false, "", errors.New(errors.CodeComparisonError, fmt.Sprintf("duplicate desired %s item keys found: %v", itemType, keys))
	}

	// Build map from actual state for lookup
	actualMap := make(map[string]map[string]any)
	unkeyedActualItems := make([]map[string]any, 0)
	for _, item := range actualSlice {
		if ctx.Err() != nil {
			return false, "", ctx.Err()
		}
		keyValStr, ok := getStringKey(item, keyField)
		if !ok {
			unkeyedActualItems = append(unkeyedActualItems, item)
			continue
		}
		actualMap[keyValStr] = item
	}

	var diffMutex sync.Mutex
	subDiffs := make(map[string]string) // Map to store diff details per key
	g, childCtx := errgroup.WithContext(ctx)

	// Compare items present in desired map against actual map
	for key, dItem := range desiredMap {
		currentKey := key
		currentDItem := dItem
		g.Go(func() error {
			select {
			case <-childCtx.Done():
				return childCtx.Err()
			default:
				aItem, exists := actualMap[currentKey]
				var diffDetail string
				if !exists {
					diffDetail = fmt.Sprintf("missing %s item with key '%s' in actual state", itemType, currentKey)
				} else {
					// Use internal GenerateDetailedMapDiff for drift-specific diffing
					diffDetail = compare.GenerateDetailedMapDiff(childCtx, currentDItem, aItem)
					if childCtx.Err() != nil {
						return childCtx.Err()
					} // Check context after diffing
					if diffDetail != "" {
						diffDetail = fmt.Sprintf("%s item '%s' differs: %s", itemType, currentKey, diffDetail)
					}
				}
				if diffDetail != "" {
					diffMutex.Lock()
					subDiffs[currentKey] = diffDetail
					diffMutex.Unlock()
				}
				return nil
			}
		})
	}

	// Identify items only present in actual map (or unkeyed)
	g.Go(func() error {
		select {
		case <-childCtx.Done():
			return childCtx.Err()
		default:
			for key := range actualMap {
				if _, exists := desiredMap[key]; !exists {
					diffMutex.Lock()
					subDiffs[key] = fmt.Sprintf("unexpected %s item with key '%s' in actual state", itemType, key)
					diffMutex.Unlock()
				}
			}
			if len(unkeyedActualItems) > 0 {
				diffMutex.Lock()
				unkeyedKey := fmt.Sprintf("~unkeyed_actual_%s", itemType) // Stable key for reporting unkeyed items
				subDiffs[unkeyedKey] = fmt.Sprintf("%d %s item(s) found in actual state without a usable key '%s'", len(unkeyedActualItems), itemType, keyField)
				diffMutex.Unlock()
			}
			return nil
		}
	})

	if err := g.Wait(); err != nil {
		return false, "", fmt.Errorf("error during concurrent %s slice comparison: %w", itemType, err)
	}
	if len(subDiffs) == 0 {
		return true, "", nil
	} // No differences found

	// Format final details string
	var details strings.Builder
	details.WriteString(fmt.Sprintf("%s slice differences (%d): ", itemType, len(subDiffs)))
	keys := make([]string, 0, len(subDiffs))
	for k := range subDiffs {
		keys = append(keys, k)
	}
	sort.Strings(keys) // Consistent output order
	for i, key := range keys {
		if i > 0 {
			details.WriteString("; ")
		}
		details.WriteString(subDiffs[key])
	}
	return false, details.String(), nil
}

// getStringKey remains a private helper within this package
func getStringKey(item map[string]any, keyField string) (string, bool) {
	keyValAny, keyExists := item[keyField]
	if !keyExists {
		return "", false
	}
	keyValStr, ok := keyValAny.(string)
	if !ok || keyValStr == "" {
		keyValStr = fmt.Sprintf("%v", keyValAny)
		if keyValStr == "" {
			return "", false
		}
		return keyValStr, true
	}
	return keyValStr, true
}
