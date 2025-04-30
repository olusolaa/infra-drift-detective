package evaluator

import (
	"context"
	"errors"
	"fmt"

	jsoniter "github.com/json-iterator/go"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/gocty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

func ConvertCtyValue(ctx context.Context, val cty.Value, logger ports.Logger) (interface{}, error) {
	if !val.IsKnown() {
		return nil, errors.New("value is unknown")
	}
	if val.IsNull() {
		return nil, nil
	}

	var goVal interface{}
	err := gocty.FromCtyValue(val, &goVal)
	if err == nil {
		if numVal, ok := goVal.(float64); ok {
			if float64(int64(numVal)) == numVal {
				return int64(numVal), nil
			}
		}
		return goVal, nil
	}

	logger.Debugf(ctx, "gocty conversion failed for type %s (%v), falling back to JSON intermediate", val.Type().FriendlyName(), err)
	jsonBytes, marshalErr := ctyjson.Marshal(val, val.Type())
	if marshalErr != nil {
		return nil, &ValueConversionError{Err: fmt.Errorf("failed to marshal cty value (%s) to intermediary JSON: %w", val.Type().FriendlyName(), marshalErr)}
	}

	var finalGoVal interface{}
	var json = jsoniter.ConfigCompatibleWithStandardLibrary
	unmarshalErr := json.Unmarshal(jsonBytes, &finalGoVal)
	if unmarshalErr != nil {
		return nil, &ValueConversionError{Err: fmt.Errorf("failed to unmarshal intermediary JSON (%s) to Go type: %w", val.Type().FriendlyName(), unmarshalErr)}
	}

	return finalGoVal, nil
}
