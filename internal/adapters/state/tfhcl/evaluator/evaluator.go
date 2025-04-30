package evaluator

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
)

// EvaluateResourceBlock evaluates attributes and recursively evaluates nested blocks.
func EvaluateResourceBlock(
	ctx context.Context,
	block *hcl.Block,
	evalCtx *hcl.EvalContext,
	logger ports.Logger,
) (EvaluatedResource, hcl.Diagnostics) {

	resourceLogger := logger.WithFields(map[string]any{
		"component":    "hcl_resource_evaluator",
		"hcl_resource": fmt.Sprintf("%s.%s", block.Type, block.Labels[1]),
		"hcl_range":    block.DefRange.String(),
	})
	resourceLogger.Debugf(ctx, "Starting evaluation of resource block")

	evaluatedResource := make(EvaluatedResource)
	var allDiags hcl.Diagnostics

	// --- Evaluate Top-Level Attributes ---
	attrs, attrParseDiags := block.Body.JustAttributes()
	filteredAttrParseDiags := filterUnsupportedArgDiagnostics(attrParseDiags)
	allDiags = append(allDiags, filteredAttrParseDiags...)
	if DiagsHasFatalErrors(filteredAttrParseDiags) { // Check fatal errors *after* filtering
		resourceLogger.Errorf(ctx, attrParseDiags, "Fatal errors parsing attributes, stopping evaluation for this block")
		return nil, allDiags
	}

	fmt.Printf("DEBUG: EvaluateResourceBlock found %d attributes\n", len(attrs))
	for name, attr := range attrs {
		attrLogger := resourceLogger.WithFields(map[string]any{"hcl_attribute": name})
		val, valEvalDiags := attr.Expr.Value(evalCtx)
		filteredValEvalDiags := filterUnsupportedArgDiagnostics(valEvalDiags)
		allDiags = append(allDiags, filteredValEvalDiags...)

		// Check fatal errors for *this attribute's evaluation* after filtering
		if DiagsHasFatalErrors(filteredValEvalDiags) {
			attrLogger.Errorf(ctx, valEvalDiags, "Failed evaluation")
			continue // Skip this attribute, but continue with others
		}
		if !val.IsKnown() {
			attrLogger.Warnf(ctx, "Attribute evaluated to an unknown value (e.g., data source?), skipping")
			continue
		}

		goVal, err := convertCtyValue(ctx, val, attrLogger) // Pass context to converter
		if err != nil {
			attrLogger.Errorf(ctx, err, "Failed cty->Go conversion")
			allDiags = allDiags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Internal value conversion error", Detail: err.Error(), Subject: attr.Expr.Range().Ptr()})
			continue // Skip this attribute if conversion fails
		}
		evaluatedResource[name] = goVal
	}

	// --- Recursively Evaluate All Nested Blocks ---
	// Use a wrapper that can handle nested blocks
	wrappedBody := &terraformBodyWrapper{original: block.Body}

	// Create a schema that can handle any block type
	schema := &hcl.BodySchema{
		Blocks: []hcl.BlockHeaderSchema{
			// Include a wildcard block to match any block type
			{Type: "", LabelNames: nil},
		},
	}

	content, contentDiags := wrappedBody.Content(schema)
	filteredContentDiags := filterNestedBlockDiagnostics(contentDiags)
	allDiags = append(allDiags, filteredContentDiags...)

	// Debug raw content diagnostics
	if len(contentDiags) > 0 {
		fmt.Printf("DEBUG: Content diagnostics before filtering (%d total):\n", len(contentDiags))
		for i, diag := range contentDiags {
			fmt.Printf("  Diag[%d]: %s - %s\n", i, diag.Summary, diag.Detail)
		}
		fmt.Printf("DEBUG: Content diagnostics after filtering (%d remain):\n", len(filteredContentDiags))
		for i, diag := range filteredContentDiags {
			fmt.Printf("  Diag[%d]: %s - %s\n", i, diag.Summary, diag.Detail)
		}
	}

	// If we can't even get the content with this open schema, bail out
	if DiagsHasFatalErrors(filteredContentDiags) {
		resourceLogger.Errorf(ctx, contentDiags, "Fatal errors getting block content, stopping evaluation for this block")
		return nil, allDiags
	}

	fmt.Printf("DEBUG: EvaluateResourceBlock found %d nested blocks\n", len(content.Blocks))
	nestedBlocks := content.Blocks // Get all blocks from content
	for _, nestedBlock := range nestedBlocks {
		blockLogger := resourceLogger.WithFields(map[string]any{"nested_block_type": nestedBlock.Type})
		blockLogger.Debugf(ctx, "Recursively evaluating nested block")

		// Recursive Call
		evaluatedBlockContent, blockDiags := evaluateBlockContent(ctx, nestedBlock, evalCtx, blockLogger)
		filteredBlockDiags := filterUnsupportedArgDiagnostics(blockDiags)
		allDiags = append(allDiags, filteredBlockDiags...)

		// Only skip storing the block if fatal errors occurred *within it* after filtering
		if DiagsHasFatalErrors(filteredBlockDiags) {
			blockLogger.Errorf(ctx, blockDiags, "Skipping nested block due to fatal evaluation errors within it")
			continue
		}
		if evaluatedBlockContent == nil && !DiagsHasFatalErrors(blockDiags) {
			// If only warnings or no content, don't store nil map unless needed
			blockLogger.Debugf(ctx, "Nested block evaluation yielded no content or only warnings, not storing")
			continue
		}

		// Store nested blocks: Group by type.
		key := nestedBlock.Type
		if existingValue, exists := evaluatedResource[key]; exists {
			// If key exists, ensure it's a slice and append
			if slice, ok := existingValue.([]any); ok {
				evaluatedResource[key] = append(slice, evaluatedBlockContent)
				// blockLogger.Debugf(ctx, "Appended nested block '%s' to existing slice", key)
			} else {
				// Handle error: existing value is not a slice (e.g., duplicate non-repeating block)
				allDiags = allDiags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError, Summary: fmt.Sprintf("Duplicate block definition for non-repeating '%s'", key),
					Detail:  fmt.Sprintf("Block type '%s' can only be defined once.", key), // Adjust msg based on actual schema knowledge
					Subject: &nestedBlock.DefRange,
				})
				blockLogger.Errorf(ctx, allDiags[len(allDiags)-1], "Attempted to append to non-slice")
			}
		} else {
			// First block of this type encountered, store as a slice anticipating potential repeats
			// This is safer than guessing if a block is singular or repeating without a schema.
			evaluatedResource[key] = []any{evaluatedBlockContent}
			// blockLogger.Debugf(ctx, "Created new slice for nested block '%s'", key)
		}
	}

	// Include resource labels
	evaluatedResource["_address"] = fmt.Sprintf("%s.%s", block.Labels[0], block.Labels[1])
	evaluatedResource["_type"] = block.Labels[0]
	evaluatedResource["_name"] = block.Labels[1]

	resourceLogger.Debugf(ctx, "Completed resource evaluation with %d attributes and %d diagnostics", len(evaluatedResource), len(allDiags))
	return evaluatedResource, allDiags
}

// terraformBodyWrapper wraps an hcl.Body to accept any block type
type terraformBodyWrapper struct {
	original hcl.Body
}

// Content implements hcl.Body
func (tbw *terraformBodyWrapper) Content(schema *hcl.BodySchema) (*hcl.BodyContent, hcl.Diagnostics) {
	// Create an open schema that accepts any block type
	openSchema := &hcl.BodySchema{
		Attributes: schema.Attributes,
		Blocks: append(schema.Blocks,
			// Add specific blocks that we know might be present
			hcl.BlockHeaderSchema{Type: "root_block_device", LabelNames: nil},
			hcl.BlockHeaderSchema{Type: "ebs_block_device", LabelNames: nil},
			hcl.BlockHeaderSchema{Type: "versioning", LabelNames: nil},
			// Wildcard for any other block type
			hcl.BlockHeaderSchema{Type: "", LabelNames: nil},
		),
	}

	content, diags := tbw.original.Content(openSchema)

	// Filter diagnostics to remove "Unsupported block type" errors
	var filteredDiags hcl.Diagnostics
	for _, diag := range diags {
		if !strings.Contains(diag.Summary, "Unsupported block type") &&
			!strings.Contains(diag.Detail, "Blocks of type") &&
			!strings.Contains(diag.Detail, "are not expected here") {
			filteredDiags = append(filteredDiags, diag)
		}
	}

	fmt.Printf("DEBUG: terraformBodyWrapper.Content found %d blocks\n", len(content.Blocks))
	for i, block := range content.Blocks {
		fmt.Printf("DEBUG: Block[%d]: Type=%s\n", i, block.Type)
	}

	return content, filteredDiags
}

// PartialContent implements hcl.Body
func (tbw *terraformBodyWrapper) PartialContent(schema *hcl.BodySchema) (*hcl.BodyContent, hcl.Body, hcl.Diagnostics) {
	// Create an open schema that accepts any block type
	openSchema := &hcl.BodySchema{
		Attributes: schema.Attributes,
		Blocks: append(schema.Blocks,
			// Add specific blocks that we know might be present
			hcl.BlockHeaderSchema{Type: "root_block_device", LabelNames: nil},
			hcl.BlockHeaderSchema{Type: "ebs_block_device", LabelNames: nil},
			hcl.BlockHeaderSchema{Type: "versioning", LabelNames: nil},
			// Wildcard for any other block type
			hcl.BlockHeaderSchema{Type: "", LabelNames: nil},
		),
	}

	content, remain, diags := tbw.original.PartialContent(openSchema)

	// Filter diagnostics to remove "Unsupported block type" errors
	var filteredDiags hcl.Diagnostics
	for _, diag := range diags {
		if !strings.Contains(diag.Summary, "Unsupported block type") &&
			!strings.Contains(diag.Detail, "Blocks of type") &&
			!strings.Contains(diag.Detail, "are not expected here") {
			filteredDiags = append(filteredDiags, diag)
		}
	}

	// Wrap the remaining body
	wrappedRemain := &terraformBodyWrapper{original: remain}

	return content, wrappedRemain, filteredDiags
}

// JustAttributes implements hcl.Body
func (tbw *terraformBodyWrapper) JustAttributes() (hcl.Attributes, hcl.Diagnostics) {
	return tbw.original.JustAttributes()
}

// MissingItemRange implements hcl.Body
func (tbw *terraformBodyWrapper) MissingItemRange() hcl.Range {
	return tbw.original.MissingItemRange()
}

// filterNestedBlockDiagnostics filters out diagnostics related to unsupported block types
func filterNestedBlockDiagnostics(diags hcl.Diagnostics) hcl.Diagnostics {
	var filtered hcl.Diagnostics
	for _, diag := range diags {
		if !strings.Contains(diag.Summary, "Unsupported block type") &&
			!strings.Contains(diag.Detail, "Blocks of type") &&
			!strings.Contains(diag.Detail, "are not expected here") {
			filtered = append(filtered, diag)
		}
	}
	return filtered
}

// evaluateBlockContent evaluates attributes AND recursively evaluates nested blocks within a block body.
func evaluateBlockContent(
	ctx context.Context,
	block *hcl.Block, // The nested block itself
	evalCtx *hcl.EvalContext,
	logger ports.Logger,
) (map[string]any, hcl.Diagnostics) { // Return map representing block content

	evaluatedContent := make(map[string]any)
	var allDiags hcl.Diagnostics

	// --- Attributes ---
	attrs, attrParseDiags := block.Body.JustAttributes()
	filteredAttrParseDiags := filterUnsupportedArgDiagnostics(attrParseDiags)
	allDiags = append(allDiags, filteredAttrParseDiags...)
	if DiagsHasFatalErrors(filteredAttrParseDiags) {
		logger.Errorf(ctx, attrParseDiags, "Fatal errors parsing attributes in nested block content")
		return nil, allDiags // Fatal within this block
	}

	for name, attr := range attrs {
		attrLogger := logger.WithFields(map[string]any{"attribute": name})
		val, valEvalDiags := attr.Expr.Value(evalCtx)
		filteredValEvalDiags := filterUnsupportedArgDiagnostics(valEvalDiags)
		allDiags = append(allDiags, filteredValEvalDiags...)

		if DiagsHasFatalErrors(filteredValEvalDiags) {
			attrLogger.Errorf(ctx, valEvalDiags, "Failed evaluation")
			continue
		}
		if !val.IsKnown() {
			attrLogger.Warnf(ctx, "Unknown value")
			continue
		}
		goVal, err := convertCtyValue(ctx, val, attrLogger)
		if err != nil {
			attrLogger.Errorf(ctx, err, "Conversion failed")
			allDiags = allDiags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Internal conversion error", Detail: err.Error(), Subject: attr.Expr.Range().Ptr()})
			continue
		}
		evaluatedContent[name] = goVal
	}

	// --- Nested Blocks (Recursive Call) ---
	// Create an open schema that accepts any block type
	schema := &hcl.BodySchema{
		Blocks: []hcl.BlockHeaderSchema{
			// This is a wildcard block header that accepts any block type
			{Type: "*", LabelNames: []string{}},
		},
	}

	content, contentDiags := block.Body.Content(schema)
	filteredContentDiags := filterUnsupportedArgDiagnostics(contentDiags)
	allDiags = append(allDiags, filteredContentDiags...)

	// If we can't even get the content with this open schema, bail out
	if DiagsHasFatalErrors(filteredContentDiags) {
		logger.Errorf(ctx, contentDiags, "Fatal errors getting block content, stopping evaluation for this block")
		return nil, allDiags
	}

	innerBlocks := content.Blocks // Get all blocks from content
	for _, innerBlock := range innerBlocks {
		blockLogger := logger.WithFields(map[string]any{"nested_block_type": innerBlock.Type})
		// RECURSIVE CALL
		evaluatedInnerContent, innerDiags := evaluateBlockContent(ctx, innerBlock, evalCtx, blockLogger)
		filteredInnerDiags := filterUnsupportedArgDiagnostics(innerDiags)
		allDiags = append(allDiags, filteredInnerDiags...)

		if DiagsHasFatalErrors(filteredInnerDiags) {
			blockLogger.Errorf(ctx, innerDiags, "Skipping inner nested block due to fatal errors")
			continue
		}
		if evaluatedInnerContent == nil && !DiagsHasFatalErrors(innerDiags) {
			blockLogger.Debugf(ctx, "Inner nested block '%s' yielded no content or only warnings", innerBlock.Type)
			continue
		}

		// Store inner nested block content (grouping by type)
		key := innerBlock.Type
		if existingValue, exists := evaluatedContent[key]; exists {
			if slice, ok := existingValue.([]any); ok {
				evaluatedContent[key] = append(slice, evaluatedInnerContent)
			} else {
				if _, isMap := existingValue.(map[string]any); isMap {
					evaluatedContent[key] = []any{existingValue, evaluatedInnerContent}
				} else {
					allDiags = allDiags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Internal state error", Detail: fmt.Sprintf("Cannot append block '%s' to existing non-map/non-slice value", key), Subject: &innerBlock.DefRange})
				}
			}
		} else {
			evaluatedContent[key] = []any{evaluatedInnerContent} // Assume slice is safer default
		}
	}

	// Return nil only if fatal errors occurred *at this level or below*
	if DiagsHasFatalErrors(allDiags) {
		return nil, allDiags
	}
	// Return the evaluated content (possibly empty map) if no fatal errors
	return evaluatedContent, allDiags
}

// filterUnsupportedArgDiagnostics remains crucial for ignoring expected noise.
func filterUnsupportedArgDiagnostics(diags hcl.Diagnostics) hcl.Diagnostics {
	if len(diags) == 0 {
		return diags
	}
	var filteredDiags hcl.Diagnostics
	for _, diag := range diags {
		isUnsupported := diag.Severity == hcl.DiagError &&
			(strings.Contains(diag.Summary, "Unsupported argument") || strings.Contains(diag.Summary, "Unsupported block type")) &&
			(strings.Contains(diag.Detail, "is not expected here") || strings.Contains(diag.Detail, "Blocks of type"))

		if !isUnsupported {
			filteredDiags = append(filteredDiags, diag)
		}
		// else: Silently filter the unsupported argument/block error
	}
	return filteredDiags
}

// DiagsHasFatalErrors helper remains the same
func DiagsHasFatalErrors(diags hcl.Diagnostics) bool {
	for _, diag := range diags {
		if diag.Severity == hcl.DiagError {
			return true
		}
	}
	return false
}
