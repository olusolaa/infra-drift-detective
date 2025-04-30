package compute

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/google/go-cmp/cmp"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

type InstanceComparer struct{}

func NewInstanceComparer() *InstanceComparer {
	return &InstanceComparer{}
}
func (c *InstanceComparer) Kind() domain.ResourceKind { return domain.KindComputeInstance }

func (c *InstanceComparer) Compare(
	ctx context.Context,
	desired domain.StateResource,
	actual domain.PlatformResource,
	attributesToCheck []string,
) ([]domain.AttributeDiff, error) {
	if desired == nil || actual == nil {
		return nil, errors.New(errors.CodeInternal, "compare called with nil desired or actual resource")
	}

	desiredAttrs := desired.Attributes()
	actualAttrs := actual.Attributes()
	diffs := make([]domain.AttributeDiff, 0)
	checkMap := make(map[string]struct{})
	for _, attr := range attributesToCheck {
		checkMap[attr] = struct{}{}
	}

	for attrKey := range checkMap {
		desiredVal, dExists := desiredAttrs[attrKey]
		actualVal, aExists := actualAttrs[attrKey]

		isEqual, compareErr := c.robustCompare(desiredVal, actualVal, dExists, aExists)
		if compareErr != nil {
			diffs = append(diffs, domain.AttributeDiff{
				AttributeName: attrKey, ExpectedValue: desiredVal, ActualValue: actualVal,
				Details: fmt.Sprintf("Comparison error: %v", compareErr),
			})
			continue
		}

		if !isEqual {
			details := ""
			if attrKey == domain.KeyTags {
				details = c.generateTagDiffDetails(desiredVal, actualVal)
			} else if attrKey == domain.ComputeRootBlockDeviceKey || attrKey == domain.ComputeEBSBlockDevicesKey {

				var detailErr error
				if attrKey == domain.ComputeRootBlockDeviceKey {
					_, details, detailErr = c.generateDetailedBlockDeviceDiff(desiredVal, actualVal, true)
				} else {
					_, details, detailErr = c.generateDetailedBlockDeviceSliceDiff(desiredVal, actualVal)
				}
				if detailErr != nil {
					details = fmt.Sprintf("Error generating diff details: %v", detailErr)
				}

			}

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

func (c *InstanceComparer) robustCompare(expected, actual any, expectedExists, actualExists bool) (bool, error) {
	if !expectedExists && !actualExists {
		return true, nil
	}
	if !expectedExists && isEmptyValue(actual) {
		return true, nil
	}
	if !actualExists && isEmptyValue(expected) {
		return true, nil
	}
	if !expectedExists || !actualExists {
		return false, nil
	}
	if expected == nil && actual == nil {
		return true, nil
	}
	if expected == nil || actual == nil {
		return false, nil
	}

	expVal := c.derefValue(reflect.ValueOf(expected))
	actVal := c.derefValue(reflect.ValueOf(actual))

	if !expVal.IsValid() && !actVal.IsValid() {
		return true, nil
	}
	if !expVal.IsValid() || !actVal.IsValid() {
		return false, nil
	}

	if !expVal.Type().Comparable() && !actVal.Type().Comparable() {
		if expVal.Kind() == reflect.Map && actVal.Kind() == reflect.Map {
			return c.compareMapsRecursive(expVal, actVal)
		}
		if expVal.Kind() == reflect.Slice && actVal.Kind() == reflect.Slice {
			return c.compareSlicesRecursive(expected, actual)
		}

		return reflect.DeepEqual(expVal.Interface(), actVal.Interface()), nil
		//return false, fmt.Errorf("cannot compare non-comparable types %s and %s", expVal.Type(), actVal.Type())
	}

	if expVal.Kind() == reflect.Bool || actVal.Kind() == reflect.Bool {
		expBoolStr := fmt.Sprintf("%v", expVal.Interface())
		actBoolStr := fmt.Sprintf("%v", actVal.Interface())
		expB, expErr := strconv.ParseBool(expBoolStr)
		actB, actErr := strconv.ParseBool(actBoolStr)
		if expErr == nil && actErr == nil {
			return expB == actB, nil
		}
	}

	if c.isNumberOrNumericString(expVal) && c.isNumberOrNumericString(actVal) {
		expFloat, expOk := c.convertToFloat64(expVal)
		actFloat, actOk := c.convertToFloat64(actVal)
		if expOk && actOk {
			const tolerance = 1e-9
			diff := expFloat - actFloat
			if diff < tolerance && diff > -tolerance {
				return true, nil
			} else {
				return false, nil
			}
		}
	}

	if expVal.Kind() == reflect.String && actVal.Kind() == reflect.String {
		return expVal.String() == actVal.String(), nil
	}

	if expVal.Type() == actVal.Type() && expVal.Type().Comparable() {
		return expVal.Interface() == actVal.Interface(), nil
	}

	return cmp.Equal(expVal.Interface(), actVal.Interface()), nil
}

// --- Reflection Helpers (inspired by shared code) ---

func (c *InstanceComparer) derefValue(v reflect.Value) reflect.Value {
	for (v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface) && !v.IsNil() {
		v = v.Elem()
	}
	return v
}

func (c *InstanceComparer) isNumber(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}

func (c *InstanceComparer) isNumberOrNumericString(v reflect.Value) bool {
	if c.isNumber(v) {
		return true
	}
	if v.Kind() == reflect.String {
		_, err := strconv.ParseFloat(v.String(), 64)
		return err == nil
	}
	return false
}

func (c *InstanceComparer) convertToFloat64(v reflect.Value) (float64, bool) {
	v = c.derefValue(v)
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(v.Uint()), true
	case reflect.Float32, reflect.Float64:
		return v.Float(), true
	case reflect.String:
		f, err := strconv.ParseFloat(v.String(), 64)
		if err == nil {
			return f, true
		}
	}
	return 0, false
}

func isEmptyValue(v any) bool {
	if v == nil {
		return true
	}
	val := reflect.ValueOf(v)
	switch val.Kind() {
	case reflect.Map, reflect.Slice, reflect.Array, reflect.Chan, reflect.String:
		return val.Len() == 0
	case reflect.Ptr, reflect.Interface:
		return val.IsNil()
	}
	return false
}

func (c *InstanceComparer) compareMapsRecursive(expMapVal, actMapVal reflect.Value) (bool, error) {
	if expMapVal.Kind() != reflect.Map || actMapVal.Kind() != reflect.Map {
		return false, fmt.Errorf("internal error: compareMapsRecursive called with non-map types %s, %s", expMapVal.Type(), actMapVal.Type())
	}
	if expMapVal.IsNil() && actMapVal.IsNil() {
		return true, nil
	}
	if expMapVal.IsNil() != actMapVal.IsNil() {
		return false, nil
	}
	if expMapVal.Len() != actMapVal.Len() {
		return false, nil
	}
	if expMapVal.Len() == 0 {
		return true, nil
	}

	expMapStrKeys := make(map[string]reflect.Value)
	iterExp := expMapVal.MapRange()
	for iterExp.Next() {
		keyStr := fmt.Sprintf("%v", iterExp.Key().Interface())
		expMapStrKeys[keyStr] = iterExp.Value()
	}

	actMapStrKeys := make(map[string]reflect.Value)
	iterAct := actMapVal.MapRange()
	for iterAct.Next() {
		keyStr := fmt.Sprintf("%v", iterAct.Key().Interface())
		actMapStrKeys[keyStr] = iterAct.Value()
	}

	if len(expMapStrKeys) != len(actMapStrKeys) {
		return false, nil
	}

	for key, expV := range expMapStrKeys {
		actV, exists := actMapStrKeys[key]
		if !exists {
			return false, nil
		}

		equal, err := c.robustCompare(expV.Interface(), actV.Interface(), true, true)
		if err != nil {
			return false, fmt.Errorf("error comparing map key '%s': %w", key, err)
		}
		if !equal {
			return false, nil
		}
	}
	return true, nil
}

func (c *InstanceComparer) compareSlicesRecursive(expected, actual any) (bool, error) {
	expSliceVal := reflect.ValueOf(expected)
	actSliceVal := reflect.ValueOf(actual)

	if expSliceVal.Kind() != reflect.Slice || actSliceVal.Kind() != reflect.Slice {
		return false, fmt.Errorf("internal error: compareSlicesRecursive called with non-slice types %s, %s", expSliceVal.Type(), actSliceVal.Type())
	}
	if expSliceVal.IsNil() && actSliceVal.IsNil() {
		return true, nil
	}
	if expSliceVal.IsNil() != actSliceVal.IsNil() {
		return false, nil
	}
	if expSliceVal.Len() != actSliceVal.Len() {
		return false, nil
	}
	if expSliceVal.Len() == 0 {
		return true, nil
	}

	for i := 0; i < expSliceVal.Len(); i++ {
		equal, err := c.robustCompare(expSliceVal.Index(i).Interface(), actSliceVal.Index(i).Interface(), true, true)
		if err != nil {
			return false, fmt.Errorf("error comparing slice element %d: %w", i, err)
		}
		if !equal {
			return false, nil
		}
	}
	return true, nil
}

// compareSliceOfMapsUnordered compares slices of maps using a key field, ignoring order.
func (c *InstanceComparer) compareSliceOfMapsUnordered(expected, actual any, keyField string) (bool, error) {
	expSliceVal := reflect.ValueOf(expected)
	actSliceVal := reflect.ValueOf(actual)

	if expSliceVal.Kind() != reflect.Slice || actSliceVal.Kind() != reflect.Slice { /* ... error ... */
		return false, fmt.Errorf("...")
	}
	if expSliceVal.IsNil() && actSliceVal.IsNil() {
		return true, nil
	}
	if expSliceVal.IsNil() != actSliceVal.IsNil() {
		return false, nil
	}
	if expSliceVal.Len() != actSliceVal.Len() {
		return false, nil
	}
	if expSliceVal.Len() == 0 {
		return true, nil
	}

	actualMap := make(map[string]reflect.Value)
	processedActualKeys := make(map[string]struct{})
	for i := 0; i < actSliceVal.Len(); i++ {
		actItemVal := c.derefValue(actSliceVal.Index(i))
		if !actItemVal.IsValid() || actItemVal.Kind() != reflect.Map {
			return false, fmt.Errorf("actual slice element %d is not a map", i)
		}
		keyVal := actItemVal.MapIndex(reflect.ValueOf(keyField))
		if !keyVal.IsValid() {
			return false, fmt.Errorf("key field '%s' not found in actual slice element %d", keyField, i)
		}
		keyStr := fmt.Sprintf("%v", c.derefValue(keyVal).Interface())
		if _, exists := actualMap[keyStr]; exists {
			return false, fmt.Errorf("duplicate key '%s' found in actual slice", keyStr)
		}
		actualMap[keyStr] = actSliceVal.Index(i)
		processedActualKeys[keyStr] = struct{}{}
	}

	matchedKeys := make(map[string]struct{})
	for i := 0; i < expSliceVal.Len(); i++ {
		expItemVal := c.derefValue(expSliceVal.Index(i))
		if !expItemVal.IsValid() || expItemVal.Kind() != reflect.Map {
			return false, fmt.Errorf("expected slice element %d is not a map", i)
		}
		keyVal := expItemVal.MapIndex(reflect.ValueOf(keyField))
		if !keyVal.IsValid() {
			return false, fmt.Errorf("key field '%s' not found in expected slice element %d", keyField, i)
		}
		keyStr := fmt.Sprintf("%v", c.derefValue(keyVal).Interface())

		actItemVal, found := actualMap[keyStr]
		if !found {
			return false, nil
		}

		equal, err := c.robustCompare(expSliceVal.Index(i).Interface(), actItemVal.Interface(), true, true)
		if err != nil {
			return false,
				fmt.Errorf("error comparing items with key '%s': %w", keyStr, err)
		}
		if !equal {
			return false, nil
		}

		if _, alreadyMatched := matchedKeys[keyStr]; alreadyMatched {
			return false, fmt.Errorf("duplicate key '%s' found in expected slice", keyStr)
		}
		matchedKeys[keyStr] = struct{}{}
	}

	return len(matchedKeys) == len(actualMap), nil
}

func (c *InstanceComparer) generateTagDiffDetails(desired, actual any) string {
	desiredTags, _ := desired.(map[string]string)
	actualTags, _ := actual.(map[string]string)
	if desiredTags == nil {
		desiredTags = map[string]string{}
	}
	if actualTags == nil {
		actualTags = map[string]string{}
	}

	filteredDesired := make(map[string]any)
	for k, v := range desiredTags {
		if !strings.HasPrefix(strings.ToLower(k), "aws:") {
			filteredDesired[k] = v
		}
	}
	filteredActual := make(map[string]any)
	for k, v := range actualTags {
		if !strings.HasPrefix(strings.ToLower(k), "aws:") {
			filteredActual[k] = v
		}
	}

	return c.generateDetailedMapDiffGeneric(filteredDesired, filteredActual) + " (Ignoring aws:* tags)"
}

func (c *InstanceComparer) generateDetailedBlockDeviceDiff(desiredVal, actualVal any, isRoot bool) (bool, string, error) {
	normDesired := c.normalizeBlockDevice(desiredVal, isRoot)
	normActual := c.normalizeBlockDevice(actualVal, isRoot)

	if (normDesired == nil && normActual != nil) || (normDesired != nil && normActual == nil) {
		return false, "One block device is nil/invalid, the other is not.", nil
	}
	if normDesired == nil && normActual == nil {
		return true, "", nil
	}

	details := c.generateDetailedMapDiffGeneric(normDesired, normActual)
	isEqual := details == ""
	return isEqual, details, nil
}

func (c *InstanceComparer) generateDetailedBlockDeviceSliceDiff(desiredVal, actualVal any) (bool, string, error) {
	desiredSlice, _ := desiredVal.([]any)
	if desiredSlice == nil {
		desiredSlice = []any{}
	}
	actualSlice, _ := actualVal.([]any)
	if actualSlice == nil {
		actualSlice = []any{}
	}

	if len(desiredSlice) == 0 && len(actualSlice) == 0 {
		return true, "", nil
	}

	normDesiredMap := make(map[string]map[string]any)
	desiredDeviceNames := make(map[string]struct{})
	for _, item := range desiredSlice {
		norm := c.normalizeBlockDevice(item, false)
		if norm != nil {
			if devName, ok := norm["device_name"].(string); ok && devName != "" {
				if _, exists := normDesiredMap[devName]; exists {
					return false, "", errors.New(errors.CodeInternal, fmt.Sprintf("duplicate desired EBS device name '%s'", devName))
				}
				normDesiredMap[devName] = norm
				desiredDeviceNames[devName] = struct{}{}
			}
		}
	}

	normActualMap := make(map[string]map[string]any)
	actualDeviceNames := make(map[string]struct{})
	for _, item := range actualSlice {
		norm := c.normalizeBlockDevice(item, false)
		if norm != nil {
			if devName, ok := norm["device_name"].(string); ok && devName != "" {
				normActualMap[devName] = norm
				actualDeviceNames[devName] = struct{}{}
			}
		}
	}

	subDiffs := make(map[string]string)

	for devName, normDesired := range normDesiredMap {
		normActual, exists := normActualMap[devName]
		if !exists {
			subDiffs[devName] = "<missing in actual>"
			continue
		}
		if !cmp.Equal(normDesired, normActual) {
			subDiffs[devName] = c.generateDetailedMapDiffGeneric(normDesired, normActual)
		}
	}
	for devName := range normActualMap {
		if _, exists := desiredDeviceNames[devName]; !exists {
			subDiffs[devName] = "<unexpected in actual>"
		}
	}

	if len(subDiffs) == 0 {
		return true, "", nil
	}

	// Format details
	var details strings.Builder
	details.WriteString("Differences by device name: ")
	devNames := make([]string, 0, len(subDiffs))
	for k := range subDiffs {
		devNames = append(devNames, k)
	}
	sort.Strings(devNames)
	for i, devName := range devNames {
		if i > 0 {
			details.WriteString("; ")
		}
		details.WriteString(fmt.Sprintf("%s=[%s]", devName, subDiffs[devName]))
	}
	return false, details.String(), nil
}

func (c *InstanceComparer) generateDetailedMapDiffGeneric(desired, actual map[string]any) string {
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

	i := 0
	for _, k := range sortedKeys {
		dVal, dExists := desired[k]
		aVal, aExists := actual[k]

		if dExists && !aExists {
			if i > 0 {
				details.WriteString(", ")
			}
			details.WriteString(fmt.Sprintf("%s: expected %v, actual <missing>", k, dVal))
			i++
		} else if !dExists && aExists {
			if i > 0 {
				details.WriteString(", ")
			}
			details.WriteString(fmt.Sprintf("%s: expected <missing>, actual %v", k, aVal))
			i++
		} else if dExists && aExists && !reflect.DeepEqual(dVal, aVal) {
			if i > 0 {
				details.WriteString(", ")
			}
			details.WriteString(fmt.Sprintf("%s: expected %v, actual %v", k, dVal, aVal))
			i++
		}
	}
	if i == 0 {
		return ""
	}
	return details.String()
}

func (c *InstanceComparer) normalizeBlockDevice(input any, isRoot bool) map[string]any {
	rawMap, ok := input.(map[string]any)
	if !ok {
		return nil
	}
	norm := make(map[string]any)
	copyIfPresentNorm(rawMap, norm, "device_name", "device_name")
	copyIfPresentNorm(rawMap, norm, "volume_type", "volume_type")
	copyIfPresentNorm(rawMap, norm, "encrypted", "encrypted")
	copyIfPresentNorm(rawMap, norm, "kms_key_id", "kms_key_id")
	copyIfPresentNorm(rawMap, norm, "throughput", "throughput")
	copyIfPresentNorm(rawMap, norm, "iops", "iops")
	copyIfPresentNorm(rawMap, norm, "volume_size", "volume_size")
	copyIfPresentNorm(rawMap, norm, "delete_on_termination", "delete_on_termination")
	normalizeIntField(norm, "volume_size")
	normalizeIntField(norm, "iops")
	normalizeIntField(norm, "throughput")
	if _, exists := norm["delete_on_termination"]; !exists {
		norm["delete_on_termination"] = isRoot
	}
	return norm
}
func copyIfPresentNorm(src, dest map[string]any, srcKey, destKey string) {
	if val, ok := src[srcKey]; ok {
		dest[destKey] = val
	}
}
func normalizeIntField(m map[string]any, key string) { /* ... as before ... */ }
