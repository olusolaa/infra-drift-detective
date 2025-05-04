package compare

import (
	"context" // Keep context for potential future use, even if not checked everywhere here
	"encoding/json"
	"fmt"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/olusolaa/infra-drift-detector/pkg/reflectutil" // Use generic reflect utils
)

// RobustCompare checks for differences between desired and actual values, considering existence.
// It specifically handles the drift detection scenario where one or both values might be absent.
// It delegates complex type comparisons internally.
// Returns true if they are considered equal in the context of drift detection.
func RobustCompare(expected, actual any, expectedExists, actualExists bool) (bool, error) {
	if !expectedExists && !actualExists {
		return true, nil
	}
	if !expectedExists && !reflectutil.IsEmptyValue(actual) {
		return false, nil
	}
	if !actualExists && !reflectutil.IsEmptyValue(expected) {
		return false, nil
	}
	if (!expectedExists && reflectutil.IsEmptyValue(actual)) || (!actualExists && reflectutil.IsEmptyValue(expected)) {
		return true, nil
	}

	if expected == nil && actual == nil {
		return true, nil
	}
	if expected == nil || actual == nil {
		return false, nil
	}

	expVal := reflectutil.DerefValue(reflect.ValueOf(expected))
	actVal := reflectutil.DerefValue(reflect.ValueOf(actual))

	if !expVal.IsValid() && !actVal.IsValid() {
		return true, nil
	}
	if !expVal.IsValid() || !actVal.IsValid() {
		return false, nil
	}

	expType := expVal.Type()
	actType := actVal.Type()

	// Handle non-comparable types of the same Kind (maps, slices) recursively
	if (!expType.Comparable() || !actType.Comparable()) && expVal.Kind() == actVal.Kind() {
		switch expVal.Kind() {
		case reflect.Map:
			return CompareMapsRecursive(expVal, actVal) // Internal helper
		case reflect.Slice:
			return compareSlicesRecursive(expected, actual) // Internal helper (ordered)
		default:
			// Let specific resource comparers handle other complex types
			return false, fmt.Errorf("cannot robustly compare non-comparable types %s and %s", expType, actType)
		}
	}

	// Handle specific type comparisons with potential type coercion
	if expVal.Kind() == reflect.Bool || actVal.Kind() == reflect.Bool {
		expBool, expOk := tryConvertToBool(expVal)
		actBool, actOk := tryConvertToBool(actVal)
		if expOk && actOk {
			return expBool == actBool, nil
		}
	}

	if reflectutil.IsNumberOrNumericString(expVal) && reflectutil.IsNumberOrNumericString(actVal) {
		expFloat, expOk := reflectutil.ToFloat64(expVal)
		actFloat, actOk := reflectutil.ToFloat64(actVal)
		if expOk && actOk {
			const tolerance = 1e-9
			diff := expFloat - actFloat
			return diff < tolerance && diff > -tolerance, nil
		}
	}

	if expVal.Kind() == reflect.String && actVal.Kind() == reflect.String {
		return expVal.String() == actVal.String(), nil
	}

	// Handle types that are directly comparable by Go
	if expType == actType && expType.Comparable() {
		return expVal.Interface() == actVal.Interface(), nil
	}

	// Fallback: consider different types or unhandled comparable types as different
	return false, nil
}

// GenerateDetailedMapDiff creates a string describing drift differences between two maps.
func GenerateDetailedMapDiff(ctx context.Context, desired, actual map[string]any) string {
	if desired == nil {
		desired = map[string]any{}
	}
	if actual == nil {
		actual = map[string]any{}
	}

	var details strings.Builder
	keys := make(map[string]struct{})
	for k := range desired {
		keys[k] = struct{}{}
	}
	for k := range actual {
		keys[k] = struct{}{}
	}

	sortedKeys := make([]string, 0, len(keys))
	for k := range keys {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)

	diffCount := 0
	for _, k := range sortedKeys {
		if ctx.Err() != nil {
			return ctx.Err().Error()
		}
		dVal, dExists := desired[k]
		aVal, aExists := actual[k]

		// Use RobustCompare specifically for drift context equality check
		equal, _ := RobustCompare(dVal, aVal, dExists, aExists)

		if !equal {
			if diffCount > 0 {
				details.WriteString("; ")
			}
			dStr := "<missing>"
			if dExists {
				dStr = fmt.Sprintf("'%v'", dVal)
			}
			aStr := "<missing>"
			if aExists {
				aStr = fmt.Sprintf("'%v'", aVal)
			}
			details.WriteString(fmt.Sprintf("%s: expected %s, actual %s", k, dStr, aStr))
			diffCount++
		}
	}
	return details.String()
}

// CompareMapsRecursive compares two maps recursively, checking for key-value equality.
func CompareMapsRecursive(expMapVal, actMapVal reflect.Value) (bool, error) {
	// Assumes maps have string keys or keys convertible to strings via fmt
	if expMapVal.Len() != actMapVal.Len() {
		return false, nil
	}
	if expMapVal.Len() == 0 {
		return true, nil
	}

	actMapKeys := make(map[string]reflect.Value)
	iterAct := actMapVal.MapRange()
	for iterAct.Next() {
		keyStr := fmt.Sprintf("%v", iterAct.Key().Interface())
		actMapKeys[keyStr] = iterAct.Value()
	}

	iterExp := expMapVal.MapRange()
	for iterExp.Next() {
		keyStr := fmt.Sprintf("%v", iterExp.Key().Interface())
		expV := iterExp.Value()
		actV, exists := actMapKeys[keyStr]
		if !exists {
			return false, nil
		}

		equal, err := RobustCompare(expV.Interface(), actV.Interface(), true, true)
		if err != nil {
			return false, fmt.Errorf("recurse map key '%s': %w", keyStr, err)
		}
		if !equal {
			return false, nil
		}
	}
	return true, nil
}

func compareSlicesRecursive(expected, actual any) (bool, error) {
	// Performs ordered comparison using RobustCompare on elements
	expSliceVal := reflect.ValueOf(expected)
	actSliceVal := reflect.ValueOf(actual)

	if expSliceVal.Len() != actSliceVal.Len() {
		return false, nil
	}
	if expSliceVal.Len() == 0 {
		return true, nil
	}

	for i := 0; i < expSliceVal.Len(); i++ {
		expElem := expSliceVal.Index(i).Interface()
		actElem := actSliceVal.Index(i).Interface()
		equal, err := RobustCompare(expElem, actElem, true, true)
		if err != nil {
			return false, fmt.Errorf("recurse slice idx %d: %w", i, err)
		}
		if !equal {
			return false, nil
		}
	}
	return true, nil
}

func tryConvertToBool(val reflect.Value) (bool, bool) {
	if val.Kind() == reflect.Bool {
		return val.Bool(), true
	}
	if val.Kind() == reflect.String {
		b, err := strconv.ParseBool(val.String())
		if err == nil {
			return b, true
		}
	}
	return false, false
}

// Sets checks if two string slices contain the same elements, ignoring order and duplicates.
// Returns true if the sets are equal, and a descriptive diff string otherwise.
func Sets(setA, setB []string) (bool, string) {
	if setA == nil {
		setA = []string{}
	}
	if setB == nil {
		setB = []string{}
	}

	countsA := make(map[string]int)
	for _, s := range setA {
		countsA[s]++
	}
	countsB := make(map[string]int)
	for _, s := range setB {
		countsB[s]++
	}

	if len(countsA) != len(countsB) {
		details := generateSetDiffDetails(countsA, countsB) // Pass counts directly
		return false, details
	}

	for k, count := range countsA {
		if countsB[k] != count {
			details := generateSetDiffDetails(countsA, countsB)
			return false, details
		}
	}

	return true, ""
}

// JSONStrings compares two strings containing JSON, ignoring formatting differences.
// Returns true if the JSON structures are semantically equal.
// If parsing fails or structures differ, it returns false and a descriptive detail string.
func JSONStrings(jsonStrA, jsonStrB string) (bool, string) {
	if jsonStrA == jsonStrB {
		return true, ""
	}
	if jsonStrA == "" {
		return false, "JSON differs (first empty, second not)"
	}
	if jsonStrB == "" {
		return false, "JSON differs (second empty, first not)"
	}

	var dataA, dataB interface{}
	errA := json.Unmarshal([]byte(jsonStrA), &dataA)
	if errA != nil {
		if jsonStrA != jsonStrB {
			return false, fmt.Sprintf("Strings differ (first is not valid JSON: %v)", errA)
		}
		return true, "" // Strings identical, even if not valid JSON
	}

	errB := json.Unmarshal([]byte(jsonStrB), &dataB)
	if errB != nil {
		return false, fmt.Sprintf("JSON differs (second is not valid JSON: %v)", errB)
	}

	if !cmp.Equal(dataA, dataB, cmpopts.EquateEmpty()) {
		return false, "JSON structures differ"
	}

	return true, ""
}

func generateSetDiffDetails(countsA, countsB map[string]int) string {
	var added, removed, changed []string
	allKeys := make(map[string]struct{})
	for k := range countsA {
		allKeys[k] = struct{}{}
	}
	for k := range countsB {
		allKeys[k] = struct{}{}
	}

	for k := range allKeys {
		countA := countsA[k]
		countB := countsB[k]
		if countA > 0 && countB == 0 {
			removed = append(removed, k)
		} else if countA == 0 && countB > 0 {
			added = append(added, k)
		} else if countA != countB {
			changed = append(changed, fmt.Sprintf("%s (count A: %d, count B: %d)", k, countA, countB))
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)

	var parts []string
	if len(added) > 0 {
		parts = append(parts, fmt.Sprintf("Added: [%s]", strings.Join(added, ", ")))
	}
	if len(removed) > 0 {
		parts = append(parts, fmt.Sprintf("Removed: [%s]", strings.Join(removed, ", ")))
	}
	if len(changed) > 0 {
		parts = append(parts, fmt.Sprintf("Count changed: [%s]", strings.Join(changed, ", ")))
	}

	if len(parts) == 0 {
		return "Set contents differ"
	}
	return strings.Join(parts, "; ")
}
