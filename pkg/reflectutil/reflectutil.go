package reflectutil

import (
	"reflect"
	"strconv"
)

func DerefValue(v reflect.Value) reflect.Value {
	for (v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface) && !v.IsNil() {
		v = v.Elem()
	}
	return v
}

func IsEmptyValue(v any) bool {
	if v == nil {
		return true
	}
	val := reflect.ValueOf(v)
	switch val.Kind() {
	case reflect.Map, reflect.Slice, reflect.Array, reflect.Chan, reflect.String:
		return val.Len() == 0
	case reflect.Ptr, reflect.Interface:
		return val.IsNil()
	default:
		return reflect.DeepEqual(val.Interface(), reflect.Zero(val.Type()).Interface())
	}
}

func IsNumber(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}

func IsNumberOrNumericString(v reflect.Value) bool {
	if IsNumber(v) {
		return true
	}
	if v.Kind() == reflect.String {
		_, err := strconv.ParseFloat(v.String(), 64)
		return err == nil
	}
	return false
}

func ToFloat64(v reflect.Value) (float64, bool) {
	v = DerefValue(v)
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
