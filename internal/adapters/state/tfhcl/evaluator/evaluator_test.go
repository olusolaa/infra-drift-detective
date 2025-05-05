// --- START OF FILE infra-drift-detector/internal/adapters/state/tfhcl/evaluator/evaluator_test.go ---

package evaluator

import (
	"context"
	portsmocks "github.com/olusolaa/infra-drift-detector/internal/core/ports/mocks"
	"github.com/stretchr/testify/mock"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax" // Import for Body cast
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func parseTestResourceBlock(t *testing.T, content string) *hcl.Block {
	t.Helper()
	parser := hclparse.NewParser()
	file, diags := parser.ParseHCL([]byte(content), "test.tf")
	if !strings.Contains(t.Name(), "Evaluation_Error") { // Allow parse errors only for eval error tests
		require.False(t, diags.HasErrors(), "Setup failed: Invalid test HCL: %s", diags.Error())
	}
	require.NotNil(t, file, "Setup failed: Parser returned nil file")
	require.NotNil(t, file.Body, "Setup failed: File body is nil")

	syntaxBody, ok := file.Body.(*hclsyntax.Body)
	require.True(t, ok, "Failed to cast body to syntax body")
	for _, block := range syntaxBody.Blocks {
		if block.Type == "resource" {
			// Convert back to *hcl.Block more reliably
			schema := &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "resource", LabelNames: block.Labels}}}
			bodyContent, contentDiags := file.Body.Content(schema)
			require.False(t, contentDiags.HasErrors(), "Error getting body content for block: %s", contentDiags) // Should not error here normally
			for _, b := range bodyContent.Blocks {
				// Match based on type, labels, and start pos (more robust than just start)
				if b.Type == block.Type && len(b.Labels) == len(block.Labels) && b.DefRange.Start == block.TypeRange.Start {
					match := true
					for i := range b.Labels {
						if b.Labels[i] != block.Labels[i] {
							match = false
							break
						}
					}
					if match {
						return b
					}
				}
			}
		}
	}
	require.FailNow(t, "Setup failed: No resource block found in test HCL")
	return nil // Should not be reached
}

func TestEvaluateBlock(t *testing.T) {
	mockLogger := portsmocks.NewLogger(t)
	mockLogger.On("WithFields", mock.Anything).Return(mockLogger).Maybe()
	mockLogger.On("Debugf", mock.Anything, mock.Anything, mock.Anything).Return().Maybe() // Adjust arg count if needed
	mockLogger.On("Errorf", mock.Anything, mock.Anything, mock.Anything).Return().Maybe()
	mockLogger.On("Warnf", mock.Anything, mock.Anything, mock.Anything).Return().Maybe() // Adjust arg count if needed
	ctx := context.Background()
	evalCtx := &hcl.EvalContext{
		Variables: map[string]cty.Value{
			"var":   cty.ObjectVal(map[string]cty.Value{"region": cty.StringVal("us-east-1")}),
			"local": cty.ObjectVal(map[string]cty.Value{"az": cty.StringVal("a")}),
		},
		Functions: StandardFunctions(),
	}

	t.Run("Literals and Refs", func(t *testing.T) {
		block := parseTestResourceBlock(t, `
            resource "t" "example" {
                name = "my-resource"
                location = var.region
                zone_letter = local.az
                enabled = true
                count = 5
            }
        `)
		evaluated, diags := EvaluateBlock(ctx, block, evalCtx, mockLogger)
		require.False(t, DiagsHasFatalErrors(diags), diags.Error())
		expected := EvaluatedResource{
			"name":        "my-resource",
			"location":    "us-east-1",
			"zone_letter": "a",
			"enabled":     true,
			"count":       float64(5),
		}
		assert.Equal(t, expected, evaluated)
	})

	t.Run("Function Call", func(t *testing.T) {
		block := parseTestResourceBlock(t, `resource "t" "example" { upper_reg = upper(var.region) }`)
		evaluated, diags := EvaluateBlock(ctx, block, evalCtx, mockLogger)
		require.False(t, DiagsHasFatalErrors(diags))
		assert.Equal(t, "US-EAST-1", evaluated["upper_reg"])
	})

	t.Run("Evaluation Error", func(t *testing.T) {
		block := parseTestResourceBlock(t, `resource "t" "example" { bad = var.nope ; good = 1 }`)
		evaluated, diags := EvaluateBlock(ctx, block, evalCtx, mockLogger)
		assert.True(t, DiagsHasFatalErrors(diags))
		assert.Nil(t, evaluated)
		assert.Contains(t, diags.Error(), "Unsupported attribute")
	})

	t.Run("Unknown Value", func(t *testing.T) {
		block := parseTestResourceBlock(t, `resource "t" "example" { known = "yes" }`)
		evaluated, diags := EvaluateBlock(ctx, block, evalCtx, mockLogger)
		assert.False(t, DiagsHasFatalErrors(diags))
		assert.Equal(t, "yes", evaluated["known"])
	})

	t.Run("Context Cancellation During Evaluation", func(t *testing.T) {
		block := parseTestResourceBlock(t, `
            resource "t" "example" {
                a = 1
                b = 2 # Assume cancellation happens before this
            }
        `)
		ctxCancel, cancel := context.WithCancel(ctx)
		cancel()

		evaluated, diags := EvaluateBlock(ctxCancel, block, evalCtx, mockLogger)
		assert.False(t, DiagsHasFatalErrors(diags), "Cancellation diags shouldn't be fatal")
		assert.NotContains(t, evaluated, "b", "Should not contain attribute 'b' evaluated after cancel")
	})
}
