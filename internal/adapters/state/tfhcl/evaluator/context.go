package evaluator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"github.com/zclconf/go-cty/cty"
)

func BuildEvalContext(
	ctx context.Context,
	mergedBody hcl.Body,
	varsFilePaths []string,
	baseDir string,
	workspaceName string,
	logger ports.Logger,
) (*hcl.EvalContext, hcl.Diagnostics) {

	logger = logger.WithFields(map[string]any{"component": "hcl_evaluator_context"})
	logger.Debugf(ctx, "Building HCL evaluation context", "vars_files", varsFilePaths, "base_dir", baseDir, "workspace", workspaceName)

	var mergedDiags hcl.Diagnostics
	mergedVars := make(map[string]cty.Value)

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
		if DiagsHasFatalErrors(diags) {
			logger.Errorf(ctx, &VariableLoadError{VarFilePath: fullVarsPath, Err: errors.New(diags.Error())}, "Error loading variables file")
		} else {
			logger.Debugf(ctx, "Loaded variables successfully", "vars_file", fullVarsPath, "vars_count", len(vars))
			for name, val := range vars {
				mergedVars[name] = val
			}
		}
	}

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

	localsSchema := &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "locals"}}}
	localsContent, contentDiags := mergedBody.Content(localsSchema)
	mergedDiags = append(mergedDiags, contentDiags...)

	localsVal, localsDiags := evaluateLocalsFromBody(localsContent, evalCtx, logger)
	mergedDiags = append(mergedDiags, localsDiags...)
	if localsDiags.HasErrors() {
		logger.Errorf(ctx, &HCLDiagnosticsError{Operation: "evaluating locals", FilePath: baseDir, Diags: localsDiags}, "Error evaluating locals block")
	}

	if !localsVal.IsNull() && localsVal.IsKnown() && localsVal.Type().IsObjectType() && localsVal.LengthInt() > 0 {
		evalCtx.Variables["local"] = localsVal
		logger.Debugf(ctx, "Added locals to context", "locals_count", localsVal.LengthInt())
	} else {
		logger.Debugf(ctx, "No locals block found or evaluated to empty/null/unknown")
	}

	if DiagsHasFatalErrors(mergedDiags) {
		logger.Errorf(ctx, &HCLDiagnosticsError{Operation: "building context", FilePath: baseDir, Diags: mergedDiags}, "Fatal errors occurred during overall context building")
		return nil, mergedDiags
	} else if len(mergedDiags) > 0 {
		logger.Warnf(ctx, "Non-fatal diagnostics during context building:\n%s", mergedDiags.Error())
	}

	logger.Debugf(ctx, "Successfully built evaluation context")
	return evalCtx, mergedDiags
}

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
	if file == nil && !diags.HasErrors() {
		diags = diags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Internal HCL parsing error", Detail: "Parser returned nil file without diagnostics.", Subject: &hcl.Range{Filename: varsFilePath}})
		logger.Errorf(nil, errors.New(diags.Error()), "Internal parser error loading variables")
	}
	if file == nil || diags.HasErrors() {
		return nil, diags
	}

	attrs, attrDiags := file.Body.JustAttributes()
	diags = append(diags, attrDiags...)
	if diags.HasErrors() {
		return nil, diags
	}

	evalCtx := &hcl.EvalContext{Variables: nil, Functions: nil}
	for name, attr := range attrs {
		val, valDiags := attr.Expr.Value(evalCtx)
		diags = append(diags, valDiags...)
		if !valDiags.HasErrors() {
			vars[name] = val
		}
	}

	return vars, diags
}

func evaluateLocalsFromBody(content *hcl.BodyContent, ctx *hcl.EvalContext, logger ports.Logger) (cty.Value, hcl.Diagnostics) {
	var allLocalsDiags hcl.Diagnostics
	locals := make(map[string]cty.Value)
	definedLocals := make(map[string]hcl.Range)

	for _, block := range content.Blocks {
		if block.Type != "locals" {
			continue
		}
		blockLogger := logger.WithFields(map[string]any{"hcl_block": "locals"})

		attrs, attrDiags := block.Body.JustAttributes()
		filteredAttrDiags := filterUnsupportedArgDiagnostics(attrDiags)
		allLocalsDiags = append(allLocalsDiags, filteredAttrDiags...)
		if DiagsHasFatalErrors(filteredAttrDiags) {
			blockLogger.Warnf(nil, "Skipping locals block due to attribute parsing diagnostics:\n%s", attrDiags.Error())
			continue
		}

		for name, attr := range attrs {
			if definedAt, exists := definedLocals[name]; exists {
				allLocalsDiags = allLocalsDiags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Duplicate local value definition", Detail: fmt.Sprintf("A local value named %q was already defined at %s.", name, definedAt.String()), Subject: &attr.NameRange})
				continue
			}
			definedLocals[name] = attr.NameRange

			val, diag := attr.Expr.Value(ctx)
			filteredValDiag := filterUnsupportedArgDiagnostics(diag)
			allLocalsDiags = append(allLocalsDiags, filteredValDiag...)
			if DiagsHasFatalErrors(filteredValDiag) {
				continue
			}
			locals[name] = val
		}
	}

	if DiagsHasFatalErrors(allLocalsDiags) {
		return cty.NilVal, allLocalsDiags
	}
	if len(locals) == 0 {
		return cty.EmptyObjectVal, allLocalsDiags
	}
	return cty.ObjectVal(locals), allLocalsDiags
}
