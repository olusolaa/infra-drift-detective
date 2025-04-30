package evaluator

import (
	"context" // Use context if logger methods require it
	"errors"
	"fmt"

	jsoniter "github.com/json-iterator/go"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/gocty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

// convertCtyValue converts cty.Value to a Go native type.
// It prioritizes gocty and falls back to JSON marshalling/unmarshalling.
func convertCtyValue(ctx context.Context, val cty.Value, logger ports.Logger) (any, error) {
	if !val.IsKnown() {
		// Cannot convert unknown value, this should ideally be caught before calling
		return nil, errors.New("value is unknown")
	}
	if val.IsNull() {
		return nil, nil // Null converts to Go nil
	}

	var goVal any
	// Use gocty first for direct conversion potential
	err := gocty.FromCtyValue(val, &goVal)
	if err == nil {
		// Post-process numbers: if float has no fractional part, convert to int64
		if numVal, ok := goVal.(float64); ok {
			if float64(int64(numVal)) == numVal {
				// logger.Debugf(ctx, "Converting float %v to int64", numVal)
				return int64(numVal), nil
			}
		}
		// Add other post-processing if needed (e.g., ensuring consistent map[string]any types)
		return goVal, nil
	}

	// Fallback to JSON intermediate representation if gocty fails
	logger.Debugf(ctx, "gocty conversion failed for type %s (%v), falling back to JSON intermediate", val.Type().FriendlyName(), err)
	jsonBytes, marshalErr := ctyjson.Marshal(val, val.Type())
	if marshalErr != nil {
		// Wrap error for context
		return nil, &ValueConversionError{Err: fmt.Errorf("failed to marshal cty value (%s) to intermediary JSON: %w", val.Type().FriendlyName(), marshalErr)}
	}

	var finalGoVal any
	// Use jsoniter for potentially better performance than standard json
	var json = jsoniter.ConfigCompatibleWithStandardLibrary
	unmarshalErr := json.Unmarshal(jsonBytes, &finalGoVal)
	if unmarshalErr != nil {
		// Wrap error for context
		return nil, &ValueConversionError{Err: fmt.Errorf("failed to unmarshal intermediary JSON (%s) to Go type: %w", val.Type().FriendlyName(), unmarshalErr)}
	}

	// logger.Debugf(ctx, "Successfully converted value via JSON fallback")
	return finalGoVal, nil
}
