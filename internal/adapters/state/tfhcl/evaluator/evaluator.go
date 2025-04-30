package evaluator

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
)

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

	attrs, attrParseDiags := block.Body.JustAttributes()
	filteredAttrParseDiags := filterUnsupportedArgDiagnostics(attrParseDiags)
	allDiags = append(allDiags, filteredAttrParseDiags...)
	if DiagsHasFatalErrors(filteredAttrParseDiags) {
		resourceLogger.Errorf(ctx, attrParseDiags, "Fatal errors parsing attributes, stopping evaluation for this block")
		return nil, allDiags
	}

	for name, attr := range attrs {
		attrLogger := resourceLogger.WithFields(map[string]any{"hcl_attribute": name})
		val, valEvalDiags := attr.Expr.Value(evalCtx)
		filteredValEvalDiags := filterUnsupportedArgDiagnostics(valEvalDiags)
		allDiags = append(allDiags, filteredValEvalDiags...)

		if DiagsHasFatalErrors(filteredValEvalDiags) {
			attrLogger.Errorf(ctx, valEvalDiags, "Failed evaluation")
			continue
		}
		if !val.IsKnown() {
			attrLogger.Warnf(ctx, "Attribute evaluated to an unknown value, skipping")
			continue
		}

		goVal, err := ConvertCtyValue(ctx, val, attrLogger)
		if err != nil {
			attrLogger.Errorf(ctx, err, "Failed cty->Go conversion")
			allDiags = allDiags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Internal value conversion error", Detail: err.Error(), Subject: attr.Expr.Range().Ptr()})
			continue
		}
		evaluatedResource[name] = goVal
	}

	nestedBlockSchema := &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "*"}}}
	content, contentDiags := block.Body.Content(nestedBlockSchema)
	allDiags = append(allDiags, contentDiags...)

	nestedBlocks := content.Blocks
	if len(nestedBlocks) > 0 {
		resourceLogger.Debugf(ctx, "Evaluating %d nested blocks (recursively)", len(nestedBlocks))
		for _, nestedBlock := range nestedBlocks {
			blockLogger := resourceLogger.WithFields(map[string]any{"nested_block_type": nestedBlock.Type})
			evaluatedBlockContent, blockDiags := evaluateBlockContent(ctx, nestedBlock, evalCtx, blockLogger)
			filteredBlockDiags := filterUnsupportedArgDiagnostics(blockDiags)
			allDiags = append(allDiags, filteredBlockDiags...)

			if DiagsHasFatalErrors(filteredBlockDiags) {
				blockLogger.Errorf(ctx, blockDiags, "Skipping nested block due to fatal evaluation errors within it")
				continue
			}
			if evaluatedBlockContent == nil && !DiagsHasFatalErrors(blockDiags) {
				blockLogger.Debugf(ctx, "Nested block evaluation yielded no content or only warnings, not storing")
				continue
			}

			key := nestedBlock.Type
			if existingValue, exists := evaluatedResource[key]; exists {
				if slice, ok := existingValue.([]any); ok {
					evaluatedResource[key] = append(slice, evaluatedBlockContent)
				} else {
					if _, isMap := existingValue.(map[string]any); isMap {
						evaluatedResource[key] = []any{existingValue, evaluatedBlockContent}
					} else {
						allDiags = allDiags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Internal state error", Detail: fmt.Sprintf("Cannot append block '%s' to existing non-map/non-slice value", key), Subject: &nestedBlock.DefRange})
					}
				}
			} else {
				evaluatedResource[key] = []any{evaluatedBlockContent}
			}
		}
	} else {
		resourceLogger.Debugf(ctx, "No nested blocks found for evaluation")
	}

	if DiagsHasFatalErrors(allDiags) {
		resourceLogger.Errorf(ctx, allDiags, "Errors encountered during resource block evaluation")
	} else if len(allDiags) > 0 {
		resourceLogger.Warnf(ctx, "Non-fatal diagnostics during resource block evaluation:\n%s", allDiags.Error())
	}

	resourceLogger.Debugf(ctx, "Finished evaluation of resource block")
	return evaluatedResource, allDiags
}

func evaluateBlockContent(
	ctx context.Context,
	block *hcl.Block,
	evalCtx *hcl.EvalContext,
	logger ports.Logger,
) (map[string]any, hcl.Diagnostics) {

	evaluatedContent := make(map[string]any)
	var allDiags hcl.Diagnostics

	attrs, attrParseDiags := block.Body.JustAttributes()
	filteredAttrParseDiags := filterUnsupportedArgDiagnostics(attrParseDiags)
	allDiags = append(allDiags, filteredAttrParseDiags...)
	if DiagsHasFatalErrors(filteredAttrParseDiags) {
		logger.Errorf(ctx, attrParseDiags, "Fatal errors parsing attributes in nested block content")
		return nil, allDiags
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
		goVal, err := ConvertCtyValue(ctx, val, attrLogger)
		if err != nil {
			attrLogger.Errorf(ctx, err, "Conversion failed")
			allDiags = allDiags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Internal conversion error", Detail: err.Error(), Subject: attr.Expr.Range().Ptr()})
			continue
		}
		evaluatedContent[name] = goVal
	}

	nestedBlockSchema := &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "*"}}}
	content, contentDiags := block.Body.Content(nestedBlockSchema)
	allDiags = append(allDiags, contentDiags...)

	innerBlocks := content.Blocks
	for _, innerBlock := range innerBlocks {
		blockLogger := logger.WithFields(map[string]any{"nested_block_type": innerBlock.Type})
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
			evaluatedContent[key] = []any{evaluatedInnerContent}
		}
	}

	if DiagsHasFatalErrors(allDiags) {
		return nil, allDiags
	}
	return evaluatedContent, allDiags
}

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
	}
	return filteredDiags
}
