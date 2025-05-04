package convert

import (
	"fmt"
	"reflect"
)

var errNotMap = fmt.Errorf("input data is not a map")
var errNotStringValue = fmt.Errorf("map value is not a string")
var errNotSlice = fmt.Errorf("input data is not a slice")
var errNotStringElement = fmt.Errorf("slice element is not a string")
var errNotMapElement = fmt.Errorf("slice element is not a map[string]any")

// ToStringMap converts map[string]any or map[string]string to map[string]string.
// Returns an error if input is not a map or if map[string]any contains non-string values.
// Returns nil map if input is nil.
func ToStringMap(data any) (map[string]string, error) {
	if data == nil {
		return nil, nil // Return nil map for nil input, not an error
	}
	if m, ok := data.(map[string]string); ok {
		return m, nil
	}
	if mAny, ok := data.(map[string]any); ok {
		result := make(map[string]string, len(mAny))
		for k, v := range mAny {
			if vStr, okStr := v.(string); okStr {
				result[k] = vStr
			} else {
				// Return error if any value is not a string
				return nil, fmt.Errorf("key '%s': %w (type %T)", k, errNotStringValue, v)
			}
		}
		return result, nil
	}
	return nil, fmt.Errorf("%w: input type %T", errNotMap, data)
}

// ToSliceOfString converts various slice types to []string.
// Handles []string and []any (converting elements via fmt.Sprintf).
// Returns an error if the input is not a slice.
func ToSliceOfString(data any) ([]string, error) {
	if data == nil {
		return []string{}, nil // Return empty slice for nil input
	}

	if slice, ok := data.([]string); ok {
		return slice, nil
	}

	val := reflect.ValueOf(data)
	if val.Kind() != reflect.Slice {
		return nil, fmt.Errorf("%w: input type %T", errNotSlice, data)
	}

	result := make([]string, 0, val.Len())
	for i := 0; i < val.Len(); i++ {
		item := val.Index(i).Interface()
		result = append(result, fmt.Sprintf("%v", item)) // Use fmt.Sprintf for broad conversion
	}
	return result, nil
}

// ToSliceOfMap converts slice types ([]map[string]any, []any) to []map[string]any.
// Returns an error if input is not a slice or elements are not map[string]any.
func ToSliceOfMap(data any) ([]map[string]any, error) {
	if data == nil {
		return []map[string]any{}, nil // Return empty slice for nil input
	}

	if sliceMap, ok := data.([]map[string]any); ok {
		return sliceMap, nil
	}

	val := reflect.ValueOf(data)
	if val.Kind() != reflect.Slice {
		return nil, fmt.Errorf("%w: input type %T", errNotSlice, data)
	}

	result := make([]map[string]any, 0, val.Len())
	for i := 0; i < val.Len(); i++ {
		item := val.Index(i).Interface()
		if mapItem, okMap := item.(map[string]any); okMap {
			result = append(result, mapItem)
		} else {
			return nil, fmt.Errorf("index %d: %w (type %T)", i, errNotMapElement, item)
		}
	}
	return result, nil
}
