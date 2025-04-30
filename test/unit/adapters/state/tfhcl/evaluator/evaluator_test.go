package evaluator_test

import (
	"context"
	"github.com/olusolaa/infra-drift-detector/internal/adapters/state/tfhcl/evaluator"
	"github.com/olusolaa/infra-drift-detector/test/testutil"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func parseTestResourceBlock(t *testing.T, content string) *hcl.Block {
	t.Helper()
	parser := hclparse.NewParser()
	// Wrap content in a resource block for parsing ease if needed,
	// or assume content is the full file content containing the block.
	// Let's assume content is the full file content.
	file, diags := parser.ParseHCL([]byte(content), "test.tf")
	require.False(t, diags.HasErrors(), "Setup failed: Invalid test HCL: %s", diags.Error())

	// Find the first resource block
	schema := &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "resource", LabelNames: []string{"type", "name"}}}}
	bodyContent, contentDiags := file.Body.Content(schema)
	require.False(t, contentDiags.HasErrors(), "Setup failed: Cannot get body content: %s", contentDiags.Error())
	require.NotEmpty(t, bodyContent.Blocks, "Setup failed: No resource block found in test HCL")
	return bodyContent.Blocks[0] // Assume first block is the target
}

func TestEvaluateResourceBlock_Attributes(t *testing.T) {
	mockLogger := testutil.NewMockLogger()
	ctx := context.Background()
	baseDir := t.TempDir()

	// Build context with some vars/locals
	varsFile := createTestFile(t, baseDir, "vars.tfvars", `region = "us-east-1"`)
	localsBody := parseTestBody(t, `locals { zone_suffix = "a" }`)
	evalCtx, ctxDiags := evaluator.BuildEvalContext(ctx, localsBody, []string{varsFile}, baseDir, "default", mockLogger)
	require.False(t, ctxDiags.HasErrors(), "Setup failed: Cannot build context: %s", ctxDiags.Error())
	require.NotNil(t, evalCtx)

	tests := []struct {
		name             string
		hclContent       string
		expectedAttrs    map[string]interface{}
		expectFatalDiags bool
	}{
		{
			name: "Literal Attributes",
			hclContent: `
				resource "test" "example" {
				  name    = "literal-name"
				  count   = 5
				  enabled = false
				}
			`,
			expectedAttrs:    map[string]interface{}{"name": "literal-name", "count": int64(5), "enabled": false},
			expectFatalDiags: false,
		},
		{
			name: "Attributes referencing var and local",
			hclContent: `
				resource "test" "example" {
				  computed_name = "${var.region}-${local.zone_suffix}"
				  region        = var.region
				}
			`,
			expectedAttrs:    map[string]interface{}{"computed_name": "us-east-1-a", "region": "us-east-1"},
			expectFatalDiags: false,
		},
		{
			name: "Attribute using function",
			hclContent: `
				resource "test" "example" {
				  upper_name = upper("test")
				}
			`,
			expectedAttrs:    map[string]interface{}{"upper_name": "TEST"},
			expectFatalDiags: false,
		},
		{
			name: "Attribute with unknown value",
			hclContent: `
				resource "test" "example" {
				  # Data sources result in unknown values in this basic evaluator
				  data_value = data.foo.bar.id
				  known_value = "abc"
				}
			`,
			expectedAttrs:    map[string]interface{}{"known_value": "abc"}, // data_value is skipped
			expectFatalDiags: false,                                        // Unknown value is not fatal
		},
		{
			name: "Attribute with evaluation error",
			hclContent: `
				resource "test" "example" {
				  bad_ref = var.nonexistent
                  good_val = 123
				}
			`,
			expectedAttrs:    map[string]interface{}{"good_val": int64(123)}, // bad_ref skipped
			expectFatalDiags: true,                                           // Evaluation error is usually fatal
		},
		{
			name: "Mixed attributes and blocks",
			hclContent: `
                resource "aws_instance" "web" {
                  ami = "ami-123"
                  instance_type = "t2.micro"
                  root_block_device {
                      volume_size = 20
                  }
                }
            `,
			// Only top-level attributes expected here, blocks handled separately
			expectedAttrs:    map[string]interface{}{"ami": "ami-123", "instance_type": "t2.micro"},
			expectFatalDiags: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			block := parseTestResourceBlock(t, tc.hclContent)
			evaluated, diags := evaluator.EvaluateResourceBlock(ctx, block, evalCtx, mockLogger)

			// Check fatal diags based on expectation
			hasFatal := evaluator.DiagsHasFatalErrors(diags)
			assert.Equal(t, tc.expectFatalDiags, hasFatal, "Mismatch in fatal diagnostics expectation. Diags: %s", diags.Error())

			// Extract only top-level attributes from evaluated for comparison
			topLevelAttrs := make(map[string]interface{})
			for k, v := range evaluated {
				// Heuristic: if value is slice or map likely from block, skip
				_, isSlice := v.([]any)
				_, isMap := v.(map[string]any)
				if !isSlice && !isMap {
					topLevelAttrs[k] = v
				}
			}

			assert.Equal(t, tc.expectedAttrs, topLevelAttrs)
		})
	}
}

func TestEvaluateResourceBlock_NestedBlocks(t *testing.T) {
	mockLogger := testutil.NewMockLogger()
	ctx := context.Background()
	baseDir := t.TempDir()

	// Simple context is enough
	evalCtx := &hcl.EvalContext{Variables: map[string]cty.Value{"var": cty.EmptyObjectVal}, Functions: evaluator.StandardFunctions()}

	tests := []struct {
		name             string
		hclContent       string
		expectedBlocks   map[string]interface{} // Keyed by block type
		expectFatalDiags bool
	}{
		{
			name: "Single Root Block Device",
			hclContent: `
				resource "aws_instance" "web" {
				  ami = "ami-123"
				  root_block_device {
					volume_size = 10
					encrypted   = true
				  }
				}
			`,
			expectedBlocks: map[string]interface{}{
				"root_block_device": []any{ // Stored as slice even if single
					map[string]interface{}{"volume_size": int64(10), "encrypted": true},
				},
			},
			expectFatalDiags: false,
		},
		{
			name: "Multiple EBS Block Devices",
			hclContent: `
				resource "aws_instance" "web" {
				  ebs_block_device {
					device_name = "/dev/sdf"
					volume_size = 20
				  }
				  ebs_block_device {
					device_name = "/dev/sdg"
					volume_size = 30
					encrypted   = true
				  }
				}
			`,
			expectedBlocks: map[string]interface{}{
				"ebs_block_device": []any{
					map[string]interface{}{"device_name": "/dev/sdf", "volume_size": int64(20)},
					map[string]interface{}{"device_name": "/dev/sdg", "volume_size": int64(30), "encrypted": true},
				},
			},
			expectFatalDiags: false,
		},
		{
			name: "Nested Block with Variable",
			hclContent: `
                resource "aws_instance" "web" {
                    variable "size" { default = 50 }
                    root_block_device {
                        volume_size = var.size
                    }
                }
            `,
			expectedBlocks: map[string]interface{}{
				"root_block_device": []any{
					map[string]interface{}{"volume_size": int64(50)},
				},
			},
			expectFatalDiags: false, // Variable defined locally
		},
		{
			name: "Nested Block with Evaluation Error",
			hclContent: `
                resource "aws_instance" "web" {
                    root_block_device {
                        volume_size = var.nonexistent
                    }
                    ebs_block_device { # This block should still evaluate if possible
                        device_name = "/dev/sdf"
                    }
                }
            `,
			expectedBlocks: map[string]interface{}{
				// root_block_device is skipped due to error within it
				"ebs_block_device": []any{
					map[string]interface{}{"device_name": "/dev/sdf"},
				},
			},
			expectFatalDiags: true, // The overall resource eval has fatal diags
		},
		{
			name: "Recursive Nested Blocks (Artificial Example)",
			hclContent: `
                resource "test" "example" {
                    outer_block {
                        name = "level1"
                        inner_block {
                             value = 123
                             deeper_block { enabled = true }
                        }
                    }
                }
            `,
			expectedBlocks: map[string]interface{}{
				"outer_block": []any{
					map[string]interface{}{
						"name": "level1",
						"inner_block": []any{
							map[string]interface{}{
								"value": int64(123),
								"deeper_block": []any{
									map[string]interface{}{"enabled": true},
								},
							},
						},
					},
				},
			},
			expectFatalDiags: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			block := parseTestResourceBlock(t, tc.hclContent)
			// Adjust context if vars needed for test case
			currentEvalCtx := evalCtx
			if strings.Contains(tc.hclContent, `variable "size"`) { // Hacky way to setup var for test case
				tmpBody := parseTestBody(t, `variable "size" { default = 50 }`)
				currentEvalCtx, _ = evaluator.BuildEvalContext(ctx, tmpBody, nil, baseDir, "default", mockLogger)
			}

			evaluated, diags := evaluator.EvaluateResourceBlock(ctx, block, currentEvalCtx, mockLogger)

			hasFatal := evaluator.DiagsHasFatalErrors(diags)
			assert.Equal(t, tc.expectFatalDiags, hasFatal, "Mismatch in fatal diagnostics expectation. Diags: %s", diags.Error())

			// Extract only block results for comparison
			blockResults := make(map[string]interface{})
			for k, v := range evaluated {
				_, isSlice := v.([]any)
				_, isMap := v.(map[string]any)                                              // Single instance blocks might be maps if not stored as slice
				if isSlice || (isMap && (k == "root_block_device" || k == "outer_block")) { // Check known block keys
					blockResults[k] = v
				}
			}

			// Use require.Equal for potentially nested structures
			require.Equal(t, tc.expectedBlocks, blockResults)
		})
	}
}
