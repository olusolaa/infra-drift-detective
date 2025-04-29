package compute

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/olusolaa/infra-drift-detector/internal/core/domain"
	"github.com/olusolaa/infra-drift-detector/internal/errors"
)

type InstanceComparer struct{}

func NewInstanceComparer() *InstanceComparer {
	return &InstanceComparer{}
}

func (c *InstanceComparer) Kind() domain.ResourceKind {
	return domain.KindComputeInstance
}

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
		if !dExists && !aExists {
			continue
		}

		var diff *domain.AttributeDiff
		var err error
		switch attrKey {
		case domain.KeyTags:
			diff = c.compareTags(attrKey, desiredVal, actualVal)
		case domain.ComputeSecurityGroupsKey:
			diff = c.compareStringSlices(attrKey, desiredVal, actualVal)
		case domain.ComputeRootBlockDeviceKey:
			diff, err = c.compareSingleBlockDevice(attrKey, desiredVal, actualVal, true) 
		case domain.ComputeEBSBlockDevicesKey:
			diff, err = c.compareEbsBlockDeviceSlice(attrKey, desiredVal, actualVal)
		default:
			diff = c.compareGeneric(attrKey, desiredVal, actualVal)
		}
		if err != nil {
			diffs = append(diffs, domain.AttributeDiff{AttributeName: attrKey, ExpectedValue: desiredVal, ActualValue: actualVal, Details: fmt.Sprintf("Comparison error: %v", err)})
		} else if diff != nil {
			diffs = append(diffs, *diff)
		}
	}
	return diffs, nil
}

func (c *InstanceComparer) compareSingleBlockDevice(
	attrKey string,
	desiredVal, actualVal any,
	isRoot bool,
) (*domain.AttributeDiff, error) {

	normDesired := c.normalizeBlockDevice(desiredVal, isRoot)
	normActual := c.normalizeBlockDevice(actualVal, isRoot)

	if normDesired == nil && normActual == nil {
		return nil, nil
	}
	if (normDesired == nil && normActual != nil) || (normDesired != nil && normActual == nil) {
		return &domain.AttributeDiff{AttributeName: attrKey, ExpectedValue: desiredVal, ActualValue: actualVal}, nil
	}

	if !cmp.Equal(normDesired, normActual) {
		details := c.generateDetailedMapDiff(normDesired, normActual)
		return &domain.AttributeDiff{
			AttributeName: attrKey,
			ExpectedValue: desiredVal, 
			ActualValue:   actualVal,  
			Details:       fmt.Sprintf("Differences found: %s", details),
		}, nil
	}

	return nil, nil 
}

func (c *InstanceComparer) compareEbsBlockDeviceSlice(
	attrKey string,
	desiredVal, actualVal any,
) (*domain.AttributeDiff, error) {

	desiredSlice, dOk := desiredVal.([]any)
	if !dOk {
		desiredSlice = []any{}
	}
	actualSlice, aOk := actualVal.([]any)
	if !aOk {
		actualSlice = []any{}
	}

	if len(desiredSlice) == 0 && len(actualSlice) > 0 {
		return &domain.AttributeDiff{AttributeName: attrKey, ExpectedValue: desiredVal, ActualValue: actualVal, Details: "Expected no EBS devices, but found some in actual state."}, nil
	}
	if len(desiredSlice) > 0 && len(actualSlice) == 0 {
		return &domain.AttributeDiff{AttributeName: attrKey, ExpectedValue: desiredVal, ActualValue: actualVal, Details: "Expected EBS devices, but found none in actual state."}, nil
	}
	if len(desiredSlice) == 0 && len(actualSlice) == 0 {
		return nil, nil
	}

	normDesiredMap := make(map[string]map[string]any)
	for _, item := range desiredSlice {
		norm := c.normalizeBlockDevice(item, false) 
		if norm != nil {
			if devName, ok := norm["device_name"].(string); ok && devName != "" {
				if _, exists := normDesiredMap[devName]; exists {
					return nil, errors.New(errors.CodeInternal, fmt.Sprintf("duplicate desired EBS device name '%s' found", devName))
				}
				normDesiredMap[devName] = norm
			}
		}
	}

	normActualMap := make(map[string]map[string]any)
	for _, item := range actualSlice {
		norm := c.normalizeBlockDevice(item, false) 
		if norm != nil {
			if devName, ok := norm["device_name"].(string); ok && devName != "" {
				normActualMap[devName] = norm 
			}
		}
	}

	mapComparer := cmpopts.SortMaps(func(a, b string) bool { return a < b })

	if !cmp.Equal(normDesiredMap, normActualMap, mapComparer) {
		details := c.generateDetailedSliceDiff(normDesiredMap, normActualMap)
		return &domain.AttributeDiff{
			AttributeName: attrKey,
			ExpectedValue: desiredVal, 
			ActualValue:   actualVal,  
			Details:       fmt.Sprintf("Differences found: %s", details),
		}, nil
	}

	return nil, nil 
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
		norm["delete_on_termination"] = true
	}
	return norm
}

func (c *InstanceComparer) generateDetailedMapDiff(desired, actual map[string]any) string {
	var details strings.Builder
	allKeys := make([]string, 0)
	
	for k := range desired {
		allKeys = append(allKeys, k)
	}
	for k := range actual {
		found := false
		for _, existingKey := range allKeys {
			if existingKey == k {
				found = true
				break
			}
		}
		if !found {
			allKeys = append(allKeys, k)
		}
	}
	
	sort.Strings(allKeys)
	
	for _, k := range allKeys {
		dVal, dExists := desired[k]
		aVal, aExists := actual[k]
		
		if !dExists {
			details.WriteString(fmt.Sprintf("+ %s: %v (added in actual)", k, aVal))
		} else if !aExists {
			details.WriteString(fmt.Sprintf("- %s: %v (missing in actual)", k, dVal))
		} else if !cmp.Equal(dVal, aVal) {
			details.WriteString(fmt.Sprintf("~ %s: expected %v, got %v", k, dVal, aVal))
		}
		details.WriteString("; ")
	}
	
	return details.String()
}

func (c *InstanceComparer) generateDetailedSliceDiff(desired, actual map[string]map[string]any) string {
	var details strings.Builder
	allDevices := make([]string, 0)
	
	for devName := range desired {
		allDevices = append(allDevices, devName)
	}
	for devName := range actual {
		found := false
		for _, existingDev := range allDevices {
			if existingDev == devName {
				found = true
				break
			}
		}
		if !found {
			allDevices = append(allDevices, devName)
		}
	}
	
	sort.Strings(allDevices)
	
	for _, devName := range allDevices {
		dDev, dExists := desired[devName]
		aDev, aExists := actual[devName]
		
		if !dExists {
			details.WriteString(fmt.Sprintf("+ Device %s: (unmanaged); ", devName))
		} else if !aExists {
			details.WriteString(fmt.Sprintf("- Device %s: (missing); ", devName))
		} else {
			devDiff := c.generateDetailedMapDiff(dDev, aDev)
			if devDiff != "" {
				details.WriteString(fmt.Sprintf("~ Device %s: %s; ", devName, devDiff))
			}
		}
	}
	
	return details.String()
}

func (c *InstanceComparer) compareGeneric(key string, desired, actual any) *domain.AttributeDiff {
	if desired == nil && actual == nil {
		return nil
	}
	
	if (desired == nil && actual != nil) || (desired != nil && actual == nil) {
		return &domain.AttributeDiff{AttributeName: key, ExpectedValue: desired, ActualValue: actual}
	}
	
	if !cmp.Equal(desired, actual) {
		return &domain.AttributeDiff{AttributeName: key, ExpectedValue: desired, ActualValue: actual}
	}
	
	return nil
}

func (c *InstanceComparer) compareTags(key string, desired, actual any) *domain.AttributeDiff {
	desiredMap, dOk := desired.(map[string]any)
	if !dOk {
		desiredMap = map[string]any{}
	}
	actualMap, aOk := actual.(map[string]any)
	if !aOk {
		actualMap = map[string]any{}
	}
	
	if len(desiredMap) == 0 && len(actualMap) == 0 {
		return nil
	}
	
	if !cmp.Equal(desiredMap, actualMap) {
		details := c.generateDetailedMapDiff(desiredMap, actualMap)
		return &domain.AttributeDiff{
			AttributeName: key,
			ExpectedValue: desired,
			ActualValue:   actual,
			Details:       fmt.Sprintf("Tag differences: %s", details),
		}
	}
	
	return nil
}

func (c *InstanceComparer) compareStringSlices(key string, desired, actual any) *domain.AttributeDiff {
	var desiredStrings, actualStrings []string
	
	switch v := desired.(type) {
	case []any:
		for _, item := range v {
			if strVal, ok := item.(string); ok {
				desiredStrings = append(desiredStrings, strVal)
			}
		}
	case []string:
		desiredStrings = v
	}
	
	switch v := actual.(type) {
	case []any:
		for _, item := range v {
			if strVal, ok := item.(string); ok {
				actualStrings = append(actualStrings, strVal)
			}
		}
	case []string:
		actualStrings = v
	}
	
	sort.Strings(desiredStrings)
	sort.Strings(actualStrings)
	
	if !cmp.Equal(desiredStrings, actualStrings) {
		return &domain.AttributeDiff{AttributeName: key, ExpectedValue: desired, ActualValue: actual}
	}
	
	return nil
}

func copyIfPresentNorm(src, dest map[string]any, srcKey, destKey string) {
	if val, ok := src[srcKey]; ok {
		dest[destKey] = val
	}
}

func normalizeIntField(m map[string]any, key string) {
	if val, exists := m[key]; exists {
		switch v := val.(type) {
		case int:
		case float64:
			m[key] = int(v)
		case string:
			if intVal, err := strconv.Atoi(v); err == nil {
				m[key] = intVal
			}
		}
	}
}
