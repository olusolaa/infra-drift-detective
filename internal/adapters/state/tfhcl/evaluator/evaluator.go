package evaluator

import (
	"context"
	"strings"

	"github.com/hashicorp/hcl/v2"
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

	attrs, attrParseDiags := block.Body.JustAttributes()
	filteredAttrDiags := filterUnsupportedDiags(attrParseDiags)
	allDiags = append(allDiags, filteredAttrDiags...)
	if DiagsHasFatalErrors(filteredAttrDiags) {
		blockLogger.Errorf(ctx, &HCLDiagnosticsError{Diags: allDiags}, "Fatal errors parsing attributes, stopping evaluation")
		return nil, allDiags
	}

	for name, attr := range attrs {
		if err := ctx.Err(); err != nil {
			blockLogger.Warnf(ctx, "Context cancelled during attribute evaluation loop")
			return evaluatedContent, allDiags
		}
		attrLogger := blockLogger.WithFields(map[string]any{"attribute": name})
		val, valEvalDiags := attr.Expr.Value(evalCtx)
		filteredValDiags := filterUnsupportedDiags(valEvalDiags)
		allDiags = append(allDiags, filteredValDiags...)

		if DiagsHasFatalErrors(filteredValDiags) {
			attrLogger.Errorf(ctx, &HCLDiagnosticsError{Diags: filteredValDiags}, "Failed evaluation")
			continue
		}
		if !val.IsKnown() {
			attrLogger.Warnf(ctx, "Attribute evaluated to an unknown value, skipping")
			continue
		}

		goVal, err := ConvertCtyValue(ctx, val, attrLogger)
		if err != nil {
			attrLogger.Errorf(ctx, err, "Failed cty->Go conversion")
			allDiags = allDiags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError, Summary: "Internal value conversion error",
				Detail: err.Error(), Subject: attr.Expr.Range().Ptr(),
			})
			continue
		}
		evaluatedContent[name] = goVal
	}

	nestedBlocks, nestedContentDiags := block.Body.Content(blockContentSchema)
	allDiags = append(allDiags, nestedContentDiags...)

	if len(nestedBlocks.Blocks) > 0 {
		blockLogger.Debugf(ctx, "Evaluating %d nested blocks...", len(nestedBlocks.Blocks))
		for _, nestedBlock := range nestedBlocks.Blocks {
			if err := ctx.Err(); err != nil {
				blockLogger.Warnf(ctx, "Context cancelled during nested block evaluation loop")
				return evaluatedContent, allDiags
			}
			evaluatedNested, blockDiags := EvaluateBlock(ctx, nestedBlock, evalCtx, blockLogger)
			filteredBlockDiags := filterUnsupportedDiags(blockDiags)
			allDiags = append(allDiags, filteredBlockDiags...)

			if DiagsHasFatalErrors(filteredBlockDiags) {
				blockLogger.Errorf(ctx, &HCLDiagnosticsError{Diags: filteredBlockDiags}, "Skipping nested block '%s' due to fatal errors", nestedBlock.Type)
				continue
			}
			if evaluatedNested == nil {
				continue
			}

			key := nestedBlock.Type
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
		blockLogger.Errorf(ctx, &HCLDiagnosticsError{Diags: allDiags}, "Errors during block evaluation")
	} else if len(allDiags) > 0 {
		blockLogger.Warnf(ctx, "Non-fatal diagnostics during block evaluation:\n%s", allDiags.Error())
	}

	blockLogger.Debugf(ctx, "Finished evaluation of block")
	if DiagsHasFatalErrors(allDiags) {
		return nil, allDiags
	}
	return evaluatedContent, allDiags
}

var blockContentSchema = &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "*"}}}

func filterUnsupportedDiags(diags hcl.Diagnostics) hcl.Diagnostics {
	if len(diags) == 0 {
		return diags
	}
	var filteredDiags hcl.Diagnostics
	for _, diag := range diags {
		isUnsupported := diag.Severity == hcl.DiagError &&
			(strings.Contains(diag.Summary, "Unsupported argument") || strings.Contains(diag.Summary, "Unsupported block type"))
		if !isUnsupported {
			filteredDiags = append(filteredDiags, diag)
		}
	}
	return filteredDiags
}
