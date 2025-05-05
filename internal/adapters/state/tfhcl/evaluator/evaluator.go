package evaluator

import (
	"context"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
)

type EvaluatedResource map[string]any

func EvaluateBlock(
	ctx context.Context,
	block *hcl.Block,
	evalCtx *hcl.EvalContext,
	logger ports.Logger,
) (EvaluatedResource, hcl.Diagnostics) {

	blockLogger := logger.WithFields(map[string]any{
		"component":   "hcl_block_evaluator",
		"block_type":  block.Type,
		"block_range": block.DefRange.String(),
	})
	if len(block.Labels) > 0 {
		blockLogger = blockLogger.WithFields(map[string]any{"block_labels": block.Labels})
	}
	blockLogger.Debugf(ctx, "Starting evaluation of block")

	evaluatedContent := make(EvaluatedResource)
	var allDiags hcl.Diagnostics

	attrs, attrDiags := block.Body.JustAttributes()
	allDiags = append(allDiags, attrDiags...)
	if DiagsHasFatalErrors(allDiags) {
		blockLogger.Errorf(ctx, &HCLDiagnosticsError{Diags: allDiags}, "Fatal errors parsing attributes, stopping evaluation")
		return nil, allDiags
	}

	for name, attr := range attrs {
		if err := ctx.Err(); err != nil {
			blockLogger.Warnf(ctx, "Context cancelled during attribute evaluation")
			return evaluatedContent, allDiags
		}
		attrLogger := blockLogger.WithFields(map[string]any{"attribute": name})
		val, valEvalDiags := attr.Expr.Value(evalCtx)
		filteredValEvalDiags := filterUnsupportedDiags(valEvalDiags)
		allDiags = append(allDiags, filteredValEvalDiags...)

		if DiagsHasFatalErrors(filteredValEvalDiags) { // Check filtered diags
			attrLogger.Errorf(ctx, &HCLDiagnosticsError{Diags: valEvalDiags}, "Failed evaluation") // Log original
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
		evaluatedContent[name] = goVal
	}

	syntaxBody, ok := block.Body.(*hclsyntax.Body)
	if !ok {
		blockLogger.Warnf(ctx, "Could not cast block body to syntax body, skipping nested block evaluation")
	} else if len(syntaxBody.Blocks) > 0 {
		blockLogger.Debugf(ctx, "Evaluating %d nested blocks...", len(syntaxBody.Blocks))
		for _, nestedSyntaxBlock := range syntaxBody.Blocks {
			if err := ctx.Err(); err != nil {
				blockLogger.Warnf(ctx, "Context cancelled during nested block evaluation")
				return evaluatedContent, allDiags
			}

			hclNestedBlock := syntaxBlockToHclBlock(nestedSyntaxBlock, block.Body)
			if hclNestedBlock == nil {
				blockLogger.Warnf(ctx, "Failed to convert syntax block %s back to hcl.Block, skipping", nestedSyntaxBlock.Type)
				continue
			}

			evaluatedNested, blockDiags := EvaluateBlock(ctx, hclNestedBlock, evalCtx, blockLogger)
			filteredBlockDiags := filterUnsupportedDiags(blockDiags) // Filter nested block diags
			allDiags = append(allDiags, filteredBlockDiags...)

			if DiagsHasFatalErrors(filteredBlockDiags) { // Check filtered nested diags
				blockLogger.Errorf(ctx, &HCLDiagnosticsError{Diags: blockDiags}, "Skipping nested block '%s' due to fatal errors within it", nestedSyntaxBlock.Type) // Log original
				continue
			}
			if evaluatedNested == nil {
				if !DiagsHasFatalErrors(blockDiags) {
					blockLogger.Debugf(ctx, "Nested block '%s' evaluation yielded no content or only warnings, not storing", nestedSyntaxBlock.Type)
				}
				continue
			}

			key := nestedSyntaxBlock.Type
			if existingValue, exists := evaluatedContent[key]; exists {
				if slice, ok := existingValue.([]any); ok {
					evaluatedContent[key] = append(slice, evaluatedNested)
				} else {
					evaluatedContent[key] = []any{existingValue, evaluatedNested}
				}
			} else {
				evaluatedContent[key] = []any{evaluatedNested}
			}
		}
	}

	if DiagsHasFatalErrors(allDiags) {
		blockLogger.Errorf(ctx, &HCLDiagnosticsError{Diags: allDiags}, "Errors encountered during block evaluation")
		return nil, allDiags
	} else if len(allDiags) > 0 {
		blockLogger.Warnf(ctx, "Non-fatal diagnostics during block evaluation:\n%s", allDiags.Error())
	}

	blockLogger.Debugf(ctx, "Finished evaluation of block")
	return evaluatedContent, allDiags
}

func filterUnsupportedDiags(diags hcl.Diagnostics) hcl.Diagnostics {
	if len(diags) == 0 {
		return diags
	}
	var filteredDiags hcl.Diagnostics
	for _, diag := range diags {
		isUnsupportedArg := strings.Contains(diag.Summary, "Unsupported argument")
		isUnsupportedBlock := strings.Contains(diag.Summary, "Unsupported block type")
		if !(diag.Severity == hcl.DiagError && (isUnsupportedArg || isUnsupportedBlock)) {
			filteredDiags = append(filteredDiags, diag)
		}
	}
	return filteredDiags
}
