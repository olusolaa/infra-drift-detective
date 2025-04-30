package evaluator_test

import (
	"context"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl/evaluator"
	"github.com/olusolaa/infra-drift-detector/test/testutil"
	"github.com/zclconf/go-cty/cty/gocty"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestConvertCtyValue(t *testing.T) {
	mockLogger := testutil.NewMockLogger()
	ctx := context.Background()

	// Create big numbers
	bfPos := big.NewFloat(123.0)
	bfNeg := big.NewFloat(-456.0)
	bfFrac := big.NewFloat(12.5)
	biPos := big.NewInt(789)
	biNeg := big.NewInt(-101)

	// Create cty.Value using gocty.ToCtyValue
	ctyBfPos, errBfPos := gocty.ToCtyValue(bfPos, cty.Number)
	require.NoError(t, errBfPos)
	ctyBfNeg, errBfNeg := gocty.ToCtyValue(bfNeg, cty.Number)
	require.NoError(t, errBfNeg)
	ctyBfFrac, errBfFrac := gocty.ToCtyValue(bfFrac, cty.Number)
	require.NoError(t, errBfFrac)
	ctyBiPos, errBiPos := gocty.ToCtyValue(biPos, cty.Number) // Convert big.Int to cty.Number
	require.NoError(t, errBiPos)
	ctyBiNeg, errBiNeg := gocty.ToCtyValue(biNeg, cty.Number)
	require.NoError(t, errBiNeg)

	tests := []struct {
		name        string
		inputVal    cty.Value
		expectedGo  interface{}
		expectError bool
	}{
		{"String", cty.StringVal("hello"), "hello", false},
		{"Empty String", cty.StringVal(""), "", false},
		{"Number Int (Positive)", cty.NumberIntVal(123), int64(123), false},
		{"Number Int (Negative)", cty.NumberIntVal(-456), int64(-456), false},
		{"Number Int (Zero)", cty.NumberIntVal(0), int64(0), false},
		{"Number Float (Positive)", cty.NumberFloatVal(123.0), int64(123), false},   // Converts to int64
		{"Number Float (Negative)", cty.NumberFloatVal(-456.0), int64(-456), false}, // Converts to int64
		{"Number Float (Zero)", cty.NumberFloatVal(0.0), int64(0), false},           // Converts to int64
		{"Number Float (Fractional)", cty.NumberFloatVal(12.5), float64(12.5), false},
		{"Number BigFloat (Positive)", ctyBfPos, int64(123), false},
		{"Number BigFloat (Negative)", ctyBfNeg, int64(-456), false},
		{"Number BigFloat (Fractional)", ctyBfFrac, float64(12.5), false},
		{"Number BigInt (Positive)", ctyBiPos, int64(789), false},
		{"Number BigInt (Negative)", ctyBiNeg, int64(-101), false},
		{"Bool True", cty.True, true, false},
		{"Bool False", cty.False, false, false},
		{"Null Value", cty.NullVal(cty.String), nil, false},
		{"Unknown Value", cty.UnknownVal(cty.String), nil, true}, // Cannot convert unknown
		{"Simple List", cty.ListVal([]cty.Value{cty.StringVal("a"), cty.StringVal("b")}), []interface{}{"a", "b"}, false},
		{"Simple Map", cty.MapVal(map[string]cty.Value{"key": cty.StringVal("val")}), map[string]interface{}{"key": "val"}, false},
		{"Empty List", cty.ListValEmpty(cty.String), []interface{}{}, false},        // Uses JSON fallback
		{"Empty Map", cty.MapValEmpty(cty.String), map[string]interface{}{}, false}, // Uses JSON fallback
		{"Nested Object",
			cty.ObjectVal(map[string]cty.Value{
				"name": cty.StringVal("test"),
				"config": cty.ObjectVal(map[string]cty.Value{
					"enabled": cty.True,
					"count":   cty.NumberIntVal(5),
				}),
			}),
			map[string]interface{}{
				"name": "test",
				"config": map[string]interface{}{
					"enabled": true,
					"count":   float64(5), // Note: JSON fallback often uses float64 for numbers
				},
			}, false}, // Uses JSON fallback
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			goVal, err := evaluator.ConvertCtyValue(ctx, tc.inputVal, mockLogger)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				// Use require.Equal for potentially complex types after JSON fallback
				require.Equal(t, tc.expectedGo, goVal)
			}
		})
	}
}
