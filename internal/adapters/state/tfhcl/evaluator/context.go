package evaluator

import (
	"context"
	"errors" // Use standard errors package
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"github.com/zclconf/go-cty/cty"
)

// BuildEvalContext constructs the HCL evaluation context.
func BuildEvalContext(
	ctx context.Context,
	mergedBody hcl.Body, // Use the merged body of all parsed files
	varsFilePaths []string,
	baseDir string, // Directory containing the HCL files
	workspaceName string,
	logger ports.Logger,
) (*hcl.EvalContext, hcl.Diagnostics) {

	logger = logger.WithFields(map[string]any{"component": "hcl_evaluator_context"})
	logger.Debugf(ctx, "Building HCL evaluation context", "vars_files", varsFilePaths, "base_dir", baseDir, "workspace", workspaceName)

	var mergedDiags hcl.Diagnostics
	mergedVars := make(map[string]cty.Value)

	// --- Load Variables ---
	for _, varsPath := range varsFilePaths {
		if varsPath == "" {
			continue
		}
		fullVarsPath := varsPath
		if !filepath.IsAbs(varsPath) {
			fullVarsPath = filepath.Join(baseDir, varsPath)
		}
		vars, diags := loadVariables(fullVarsPath, logger)
		mergedDiags = append(mergedDiags, diags...)
		if diags.HasErrors() {
			// Log the specific VariableLoadError which includes the path
			logger.Errorf(ctx, &VariableLoadError{VarFilePath: fullVarsPath, Err: errors.New(diags.Error())}, "Error loading variables file")
			// Continue processing other files but diagnostics will mark failure
		} else {
			logger.Debugf(ctx, "Loaded variables successfully", "vars_file", fullVarsPath, "vars_count", len(vars))
			for name, val := range vars {
				mergedVars[name] = val // Later files override earlier ones
			}
		}
	}

	// --- Prepare Context Base ---
	funcs := StandardFunctions()
	cwd, _ := filepath.Abs(".")
	if cwd == "" {
		cwd = "."
	}
	modulePath, _ := filepath.Abs(baseDir)
	if modulePath == "" {
		modulePath = baseDir
	}

	evalCtx := &hcl.EvalContext{
		Variables: map[string]cty.Value{
			"var":       cty.ObjectVal(mergedVars),
			"path":      cty.ObjectVal(map[string]cty.Value{"module": cty.StringVal(modulePath), "root": cty.StringVal(modulePath), "cwd": cty.StringVal(cwd)}),
			"terraform": cty.ObjectVal(map[string]cty.Value{"workspace": cty.StringVal(workspaceName)}),
			"local":     cty.EmptyObjectVal,
		},
		Functions: funcs,
	}

	// --- Evaluate Locals ---
	// Create a special wrapper body that allows any block type
	wrappedBody := &terraformHclBody{original: mergedBody}

	localsVal, localsDiags := evaluateLocals(wrappedBody, evalCtx, logger)
	mergedDiags = append(mergedDiags, localsDiags...)
	if localsDiags.HasErrors() {
		// Log actual error object
		logger.Errorf(ctx, &HCLDiagnosticsError{Operation: "evaluating locals", FilePath: baseDir, Diags: localsDiags}, "Error evaluating locals block")
		// If locals fail, context is likely unusable
		return nil, mergedDiags
	}

	if !localsVal.IsNull() && localsVal.IsKnown() && localsVal.Type().IsObjectType() && localsVal.LengthInt() > 0 {
		evalCtx.Variables["local"] = localsVal
		logger.Debugf(ctx, "Evaluated and added locals to context", "locals_count", localsVal.LengthInt())
	} else {
		logger.Debugf(ctx, "No locals block found or evaluated to empty/null/unknown")
	}

	// Check final diagnostics severity
	if DiagsHasFatalErrors(mergedDiags) {
		logger.Errorf(ctx, &HCLDiagnosticsError{Operation: "building context", FilePath: baseDir, Diags: mergedDiags}, "Fatal errors occurred during overall context building")
		return nil, mergedDiags // Return nil context on fatal error
	} else if len(mergedDiags) > 0 {
		logger.Warnf(ctx, "Non-fatal diagnostics during context building:\n%s", mergedDiags.Error())
	}

	logger.Debugf(ctx, "Successfully built evaluation context")
	return evalCtx, mergedDiags
}

// terraformHclBody is a wrapper around hcl.Body that accepts any block type, specifically for context building
type terraformHclBody struct {
	original hcl.Body
}

// Content implements hcl.Body
func (tfb *terraformHclBody) Content(schema *hcl.BodySchema) (*hcl.BodyContent, hcl.Diagnostics) {
	// Use an open schema that accepts any block type, including 'resource' blocks
	openSchema := &hcl.BodySchema{
		Attributes: schema.Attributes,
		Blocks: append(schema.Blocks, hcl.BlockHeaderSchema{
			Type:       "resource",
			LabelNames: []string{"type", "name"},
		}, hcl.BlockHeaderSchema{
			Type: "locals",
		}, hcl.BlockHeaderSchema{
			// Wildcard block type
			Type:       "",
			LabelNames: nil,
		}),
	}

	content, diags := tfb.original.Content(openSchema)

	var filteredDiags hcl.Diagnostics
	for _, diag := range diags {
		if !strings.Contains(diag.Summary, "Unsupported block type") &&
			!strings.Contains(diag.Detail, "Blocks of type") &&
			!strings.Contains(diag.Detail, "are not expected here") {
			filteredDiags = append(filteredDiags, diag)
		}
	}

	return content, filteredDiags
}

// PartialContent implements hcl.Body
func (tfb *terraformHclBody) PartialContent(schema *hcl.BodySchema) (*hcl.BodyContent, hcl.Body, hcl.Diagnostics) {
	// Use an open schema that accepts any block type, including 'resource' blocks
	openSchema := &hcl.BodySchema{
		Attributes: schema.Attributes,
		Blocks: append(schema.Blocks, hcl.BlockHeaderSchema{
			Type:       "resource",
			LabelNames: []string{"type", "name"},
		}, hcl.BlockHeaderSchema{
			Type: "locals",
		}, hcl.BlockHeaderSchema{
			// Wildcard block type
			Type:       "",
			LabelNames: nil,
		}),
	}

	content, remain, diags := tfb.original.PartialContent(openSchema)

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
	wrappedRemain := &terraformHclBody{original: remain}

	return content, wrappedRemain, filteredDiags
}

// JustAttributes implements hcl.Body
func (tfb *terraformHclBody) JustAttributes() (hcl.Attributes, hcl.Diagnostics) {
	return tfb.original.JustAttributes()
}

// MissingItemRange implements hcl.Body
func (tfb *terraformHclBody) MissingItemRange() hcl.Range {
	return tfb.original.MissingItemRange()
}

// loadVariables loads a single .tfvars file.
func loadVariables(varsFilePath string, logger ports.Logger) (map[string]cty.Value, hcl.Diagnostics) {
	vars := make(map[string]cty.Value)
	var diags hcl.Diagnostics
	logger = logger.WithFields(map[string]any{"vars_file": varsFilePath})

	src, err := os.ReadFile(varsFilePath)
	if err != nil {
		diags = diags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Cannot read variables file", Detail: err.Error(), Subject: &hcl.Range{Filename: varsFilePath}})
		return nil, diags
	}

	file, parseDiags := hclsyntax.ParseConfig(src, varsFilePath, hcl.Pos{Line: 1, Column: 1})
	diags = append(diags, parseDiags...)
	// Check for nil file even if no diags reported (parser bug?)
	if file == nil && !diags.HasErrors() {
		diags = diags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Internal HCL parsing error", Detail: "Parser returned nil file without diagnostics.", Subject: &hcl.Range{Filename: varsFilePath}})
		logger.Errorf(nil, errors.New(diags.Error()), "Internal parser error loading variables") // Use nil context if logger requires it
	}
	if file == nil || diags.HasErrors() {
		return nil, diags
	}

	attrs, attrDiags := file.Body.JustAttributes()
	diags = append(diags, attrDiags...)
	if diags.HasErrors() {
		return nil, diags
	}

	evalCtx := &hcl.EvalContext{Variables: nil, Functions: nil} // Vars files have no external context
	for name, attr := range attrs {
		val, valDiags := attr.Expr.Value(evalCtx)
		diags = append(diags, valDiags...) // Collect all value diagnostics
		if !valDiags.HasErrors() {
			vars[name] = val
		}
	}

	// Return vars even if some values had errors, but include diagnostics
	return vars, diags
}

// evaluateLocals finds and evaluates all 'locals' blocks in the body.
func evaluateLocals(body hcl.Body, ctx *hcl.EvalContext, logger ports.Logger) (cty.Value, hcl.Diagnostics) {
	var allLocalsDiags hcl.Diagnostics
	locals := make(map[string]cty.Value)
	definedLocals := make(map[string]hcl.Range)

	schema := &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "locals"}}}
	content, contentDiags := body.Content(schema)
	allLocalsDiags = append(allLocalsDiags, contentDiags...)

	for _, block := range content.Blocks {
		if block.Type != "locals" {
			continue
		}
		blockLogger := logger.WithFields(map[string]any{"hcl_block": "locals"})

		attrs, attrDiags := block.Body.JustAttributes()
		allLocalsDiags = append(allLocalsDiags, attrDiags...)
		if attrDiags.HasErrors() {
			blockLogger.Warnf(nil, "Skipping locals block due to attribute parsing diagnostics:\n%s", attrDiags.Error())
			continue // Skip this locals block if attributes themselves are invalid
		}

		for name, attr := range attrs {
			if definedAt, exists := definedLocals[name]; exists {
				diag := &hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Duplicate local value definition", Detail: fmt.Sprintf("A local value named %q was already defined at %s.", name, definedAt.String()), Subject: &attr.NameRange}
				allLocalsDiags = allLocalsDiags.Append(diag)
				continue // Skip duplicate
			}
			definedLocals[name] = attr.NameRange

			val, diag := attr.Expr.Value(ctx) // Use the main eval context
			if diag.HasErrors() {
				allLocalsDiags = append(allLocalsDiags, diag...)
				// Do not add partially evaluated locals to the map if they errored
				continue
			}
			locals[name] = val
		}
	}

	// Only return valid object if no fatal errors occurred during evaluation
	if DiagsHasFatalErrors(allLocalsDiags) {
		return cty.NilVal, allLocalsDiags // Indicate failure
	}
	if len(locals) == 0 {
		return cty.EmptyObjectVal, allLocalsDiags // Return empty object, not null
	}
	return cty.ObjectVal(locals), allLocalsDiags
}
