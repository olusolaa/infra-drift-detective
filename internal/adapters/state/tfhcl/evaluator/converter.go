package evaluator

import (
	"context"
	"fmt"
	"math"
	"math/big"

	jsoniter "github.com/json-iterator/go"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	apperrors "github.com/olusolaa/infra-drift-detector/internal/errors"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/gocty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

func ConvertCtyValue(ctx context.Context, val cty.Value, logger ports.Logger) (interface{}, error) {
	if !val.IsKnown() {
		return nil, apperrors.New(apperrors.CodeHCLEvalError, "cannot convert unknown cty value")
	}
	if val.IsNull() {
		return nil, nil
	}

	var goVal interface{}
	err := gocty.FromCtyValue(val, &goVal)
	if err == nil {
		if val.Type().Equals(cty.Number) {
			bf := val.AsBigFloat()
			if i64, acc := bf.Int64(); acc == big.Exact {
				return i64, nil
			}
			f64, _ := bf.Float64()
			if !math.IsInf(f64, 0) {
				return f64, nil
			}
			return bf.Text('g', -1), nil
		}
		if numVal, ok := goVal.(float64); ok {
			if numVal >= math.MinInt64 && numVal <= math.MaxInt64 && float64(int64(numVal)) == numVal {
				return int64(numVal), nil
			}
		}
		return goVal, nil
	}

	logger.Debugf(ctx, "gocty conversion failed (%v), falling back to JSON intermediate", err)

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
