package convert

import (
	"fmt"
	"reflect"
)

func ToSliceOfMap(input any) ([]map[string]any, error) {
	if input == nil {
		return nil, nil
	}

	if result, ok := input.([]map[string]any); ok {
		return result, nil
	}

	if slice, ok := input.([]any); ok {
		result := make([]map[string]any, 0, len(slice))

		for _, item := range slice {
			if m, ok := item.(map[string]any); ok {
				result = append(result, m)
			} else {
				return nil, fmt.Errorf("slice element is not a map[string]any but %T", item)
			}
		}

		return result, nil
	}

	val := reflect.ValueOf(input)
	if val.Kind() == reflect.Slice {
		result := make([]map[string]any, 0, val.Len())

		for i := 0; i < val.Len(); i++ {
			item := val.Index(i).Interface()

			if m, ok := item.(map[string]any); ok {
				result = append(result, m)
			} else {
				return nil, fmt.Errorf("slice element at index %d is not a map[string]any but %T", i, item)
			}
		}

		return result, nil
	}

	return nil, fmt.Errorf("input is not a slice of maps, but %T", input)
}

func ToStringMap(input any) (map[string]string, error) {
	if input == nil {
		return nil, nil
	}

	if result, ok := input.(map[string]string); ok {
		return result, nil
	}

	if m, ok := input.(map[string]any); ok {
		result := make(map[string]string, len(m))

		for k, v := range m {
			if str, ok := v.(string); ok {
				result[k] = str
			} else {
				result[k] = fmt.Sprintf("%v", v)
			}
		}

		return result, nil
	}

	return nil, fmt.Errorf("input is not a map, but %T", input)
}

func ToSliceOfString(input any) ([]string, error) {
	if input == nil {
		return nil, nil
	}

	if strSlice, ok := input.([]string); ok {
		return strSlice, nil
	}

	if anySlice, ok := input.([]any); ok {
		result := make([]string, 0, len(anySlice))
		for _, item := range anySlice {
			if str, ok := item.(string); ok {
				result = append(result, str)
			} else {
				result = append(result, fmt.Sprintf("%v", item))
			}
		}
		return result, nil
	}

	val := reflect.ValueOf(input)
	if val.Kind() == reflect.Slice {
		result := make([]string, 0, val.Len())
		for i := 0; i < val.Len(); i++ {
			item := val.Index(i).Interface()
			if str, ok := item.(string); ok {
				result = append(result, str)
			} else {
				result = append(result, fmt.Sprintf("%v", item))
			}
		}
		return result, nil
	}

	return nil, fmt.Errorf("input is not a slice, but %T", input)
}
