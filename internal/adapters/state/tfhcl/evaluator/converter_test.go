package evaluator

import (
	"context"
	portsmocks "github.com/olusolaa/infra-drift-detector/internal/core/ports/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/zclconf/go-cty/cty"
	"testing"
)

func TestConvertCtyValue(t *testing.T) {
	mockLogger := portsmocks.NewLogger(t)
	mockLogger.On("Debugf", mock.Anything, mock.Anything, mock.Anything).Return()
	ctx := context.Background()

	tests := []struct {
		name        string
		inputVal    cty.Value
		expectedGo  interface{}
		expectError bool
	}{
		{"String", cty.StringVal("hello"), "hello", false},
		{"Number Int", cty.NumberIntVal(123), float64(123), false},
		{"Number Float Whole", cty.NumberFloatVal(123.0), float64(123), false},
		{"Number Float Fractional", cty.NumberFloatVal(12.5), float64(12.5), false},
		{"Bool True", cty.True, true, false},
		{"Null Value", cty.NullVal(cty.String), nil, false},
		{"Unknown Value", cty.UnknownVal(cty.String), nil, true},
		{"Simple List", cty.ListVal([]cty.Value{cty.NumberIntVal(1), cty.NumberIntVal(2)}), []interface{}{float64(1), float64(2)}, false}, // JSON fallback -> float64
		{"Simple Map", cty.MapVal(map[string]cty.Value{"key": cty.StringVal("val")}), map[string]interface{}{"key": "val"}, false},
		{"Empty List", cty.ListValEmpty(cty.String), []interface{}{}, false},
		{"Empty Map", cty.MapValEmpty(cty.String), map[string]interface{}{}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			goVal, err := ConvertCtyValue(ctx, tc.inputVal, mockLogger)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expectedGo, goVal)
			}
		})
	}
}
