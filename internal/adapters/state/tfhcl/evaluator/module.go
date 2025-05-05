package evaluator

import (
	"context"
	"path/filepath"
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	apperrors "github.com/olusolaa/infra-drift-detector/internal/errors"
	"github.com/zclconf/go-cty/cty"
)

type Module struct {
	logger      ports.Logger
	path        string
	workspace   string
	inputVars   map[string]cty.Value
	variables   map[string]*VariableDefinition
	locals      map[string]cty.Value
	evalContext *hcl.EvalContext
	initDiags   hcl.Diagnostics
	evalMutex   sync.RWMutex
}

type VariableDefinition struct {
	Name        string
	Type        cty.Type
	Description string
	Default     cty.Value
	Sensitive   bool
	FilePath    string
	DeclRange   hcl.Range
}

func LoadModule(
	ctx context.Context,
	dirPath string,
	varFilePaths []string,
	workspaceName string,
	logger ports.Logger,
) (map[string]*hcl.File, *Module, error) {

	logger = logger.WithFields(map[string]any{"component": "hcl_module_loader", "module_path": dirPath})
	logger.Debugf(ctx, "Loading HCL module...")

	parser := hclparse.NewParser()
	files, parseDiags, err := parseHCLFiles(ctx, parser, dirPath, logger)
	if err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			return files, nil, err
		}
		return files, nil, apperrors.Wrap(&HCLDiagnosticsError{Operation: "parsing", FilePath: dirPath, Diags: parseDiags}, apperrors.CodeStateParseError, err.Error())
	}
	if DiagsHasFatalErrors(parseDiags) {
		return files, nil, apperrors.Wrap(&HCLDiagnosticsError{Operation: "parsing", FilePath: dirPath, Diags: parseDiags}, apperrors.CodeStateParseError, "fatal parsing errors")
	}
	if len(files) == 0 {
		return files, nil, apperrors.New(apperrors.CodeStateParseError, "no HCL files found")
	}
	if err := ctx.Err(); err != nil {
		return files, nil, err
	}

	mod := &Module{
		logger:    logger,
		path:      dirPath,
		workspace: workspaceName,
		initDiags: parseDiags,
		variables: make(map[string]*VariableDefinition),
	}

	logger.Debugf(ctx, "Decoding variable definitions...")
	var varDefDiags hcl.Diagnostics
	definedVars := make(map[string]string)
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return files, mod, err
		}
		syntaxBody, ok := file.Body.(*hclsyntax.Body)
		if !ok {
			continue
		}

		for _, block := range syntaxBody.Blocks {
			if block.Type != "variable" {
				continue
			}

			hclBlock := syntaxBlockToHclBlock(block, file.Body)
			if hclBlock == nil {
				varDefDiags = append(varDefDiags, &hcl.Diagnostic{Severity: hcl.DiagWarning, Summary: "Internal error", Detail: "Could not convert syntax variable block back to hcl.Block", Subject: &block.TypeRange}) // Use TypeRange for subject
				continue
			}

			if len(hclBlock.Labels) != 1 {
				defRange := hclBlock.DefRange
				varDefDiags = append(varDefDiags, &hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Invalid variable block", Detail: "Variable block requires exactly one label (the name).", Subject: &defRange})
				continue
			}
			varName := hclBlock.Labels[0]
			blockDefRange := hclBlock.DefRange
			if prevPath, exists := definedVars[varName]; exists {
				varDefDiags = append(varDefDiags, &hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Duplicate variable definition", Detail: "Variable " + varName + " was already defined at " + prevPath, Subject: &blockDefRange}) // Use stored range
				continue
			}
			definedVars[varName] = blockDefRange.String()

			def, decodeDiags := decodeVariableBlock(hclBlock)
			varDefDiags = append(varDefDiags, decodeDiags...)
			if def != nil && !DiagsHasFatalErrors(decodeDiags) {
				mod.variables[varName] = def
			}
		}
	}
	mod.initDiags = append(mod.initDiags, varDefDiags...)
	if DiagsHasFatalErrors(mod.initDiags) {
		return files, mod, apperrors.Wrap(&HCLDiagnosticsError{Operation: "decoding variables", FilePath: dirPath, Diags: mod.initDiags}, apperrors.CodeStateParseError, "fatal errors decoding variable blocks")
	}
	logger.Debugf(ctx, "Decoded %d variable definitions", len(mod.variables))

	var mergeDiags hcl.Diagnostics
	mod.inputVars, mergeDiags = mergeVariablesAndDefaults(ctx, parser, mod.variables, varFilePaths, logger) // Pass decoded definitions
	mod.initDiags = append(mod.initDiags, mergeDiags...)
	if DiagsHasFatalErrors(mod.initDiags) {
		return files, mod, apperrors.Wrap(&HCLDiagnosticsError{Operation: "merging variables", FilePath: dirPath, Diags: mod.initDiags}, apperrors.CodeStateParseError, "fatal errors processing variable values")
	}
	logger.Debugf(ctx, "Final input variable count: %d", len(mod.inputVars))
	if err := ctx.Err(); err != nil {
		return files, mod, err
	}

	mod.initDiags = append(mod.initDiags, mod.buildInitialContext(ctx)...)
	if DiagsHasFatalErrors(mod.initDiags) {
		return files, mod, apperrors.Wrap(&HCLDiagnosticsError{Operation: "building initial context", FilePath: dirPath, Diags: mod.initDiags}, apperrors.CodeStateParseError, "fatal errors building initial context")
	}
	if err := ctx.Err(); err != nil {
		return files, mod, err
	}

	logger.Debugf(ctx, "Evaluating locals blocks...")
	var localsDiags hcl.Diagnostics
	definedLocals := make(map[string]string)
	evaluatedLocals := make(map[string]cty.Value)

	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return files, mod, err
		}
		syntaxBody, ok := file.Body.(*hclsyntax.Body)
		if !ok {
			continue
		}

		for _, block := range syntaxBody.Blocks {
			if block.Type != "locals" {
				continue
			}
			if err := ctx.Err(); err != nil {
				return files, mod, err
			}

			for name, attr := range block.Body.Attributes {
				if err := ctx.Err(); err != nil {
					return files, mod, err
				}
				attrNameRange := attr.NameRange
				if definedAtStr, exists := definedLocals[name]; exists {
					localsDiags = append(localsDiags, &hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Duplicate local value definition", Detail: "Local value " + name + " was already defined at " + definedAtStr, Subject: &attrNameRange}) // Use stored range
					continue
				}
				definedLocals[name] = ""

				val, valDiags := attr.Expr.Value(mod.evalContext)
				localsDiags = append(localsDiags, valDiags...)
				if !DiagsHasFatalErrors(valDiags) {
					evaluatedLocals[name] = val
				}
			}
		}
	}
	mod.initDiags = append(mod.initDiags, localsDiags...)
	if DiagsHasFatalErrors(mod.initDiags) {
		return files, mod, apperrors.Wrap(&HCLDiagnosticsError{Operation: "evaluating locals", FilePath: dirPath, Diags: mod.initDiags}, apperrors.CodeStateParseError, "fatal errors evaluating locals")
	}

	if len(evaluatedLocals) > 0 {
		mod.evalMutex.Lock()
		mod.locals = evaluatedLocals
		if mod.evalContext != nil && mod.evalContext.Variables != nil {
			mod.evalContext.Variables["local"] = cty.ObjectVal(evaluatedLocals)
		} else {
			mod.initDiags = append(mod.initDiags, &hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Internal Error", Detail: "Evaluation context was nil before updating locals."})
			mod.evalMutex.Unlock()
			return files, mod, apperrors.New(apperrors.CodeInternal, "evaluation context nil before updating locals")
		}
		mod.evalMutex.Unlock()
		logger.Debugf(ctx, "Evaluated and added %d local variables to context", len(evaluatedLocals))
	} else {
		logger.Debugf(ctx, "No local variables found or evaluated")
	}
	if err := ctx.Err(); err != nil {
		return files, mod, err
	}

	// --- Final Check ---
	if len(mod.initDiags) > 0 {
		logger.Warnf(ctx, "Non-fatal diagnostics during module load:\n%s", mod.initDiags.Error())
	}
	logger.Debugf(ctx, "HCL module loaded successfully")
	return files, mod, nil
}

func (m *Module) EvalContext() *hcl.EvalContext {
	m.evalMutex.RLock()
	defer m.evalMutex.RUnlock()
	if m.evalContext == nil {
		return nil
	}
	copiedVars := make(map[string]cty.Value, len(m.evalContext.Variables))
	for k, v := range m.evalContext.Variables {
		copiedVars[k] = v
	}
	return &hcl.EvalContext{Variables: copiedVars, Functions: m.evalContext.Functions}
}

func mergeVariablesAndDefaults(ctx context.Context, parser *hclparse.Parser, definitions map[string]*VariableDefinition, varFilePaths []string, logger ports.Logger) (map[string]cty.Value, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	finalVars := make(map[string]cty.Value)
	logger.Debugf(ctx, "Loading variables from tfvars files: %v", varFilePaths)
	loadedTfVars := make(map[string]cty.Value)
	varsFileAttrRanges := make(map[string]map[string]hcl.Range)

	for _, path := range varFilePaths {
		if err := ctx.Err(); err != nil {
			logger.Warnf(ctx, "Context cancelled during tfvars loading")
			return finalVars, diags
		}
		if path == "" {
			continue
		}

		vars, loadDiags, attrRanges := loadVarsFromFile(ctx, parser, path, logger)
		diags = append(diags, loadDiags...)
		if DiagsHasFatalErrors(loadDiags) {
			continue
		}
		varsFileAttrRanges[path] = attrRanges

		for name, val := range vars {
			if err := ctx.Err(); err != nil {
				logger.Warnf(ctx, "Context cancelled during tfvars processing")
				return finalVars, diags
			}
			if _, defined := definitions[name]; !defined {
				var subjectRange *hcl.Range
				if rangesMap, ok := varsFileAttrRanges[path]; ok {
					if attrRange, attrOk := rangesMap[name]; attrOk {
						subjectRange = &attrRange
					}
				}
				if subjectRange == nil {
					subjectRange = &hcl.Range{Filename: path}
				}
				diags = diags.Append(&hcl.Diagnostic{Severity: hcl.DiagWarning, Summary: "Undefined variable in vars file", Detail: "Variable " + name + " is set in " + path + " but not defined.", Subject: subjectRange})
			} else {
				loadedTfVars[name] = val
			}
		}
	}
	logger.Debugf(ctx, "Loaded %d variables from tfvars files", len(loadedTfVars))
	if DiagsHasFatalErrors(diags) {
		return nil, diags
	}

	logger.Debugf(ctx, "Merging variables (tfvars override defaults)")
	for name, def := range definitions {
		if err := ctx.Err(); err != nil {
			logger.Warnf(ctx, "Context cancelled during variable merging")
			return finalVars, diags
		}
		var finalVal cty.Value
		var convDiags hcl.Diagnostics
		targetType := def.Type

		if val, ok := loadedTfVars[name]; ok {
			finalVal, convDiags = convertVarType(val, targetType, def.DeclRange)
			diags = append(diags, convDiags...)
		} else if !def.Default.IsNull() && def.Default.IsKnown() {
			finalVal, convDiags = convertVarType(def.Default, targetType, def.DeclRange)
			diags = append(diags, convDiags...)
		} else {
			diags = diags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Missing required variable", Detail: "Variable " + name + " has no default value and was not provided.", Subject: &def.DeclRange})
			continue
		}

		if !DiagsHasFatalErrors(convDiags) {
			finalVars[name] = finalVal
		}
	}
	logger.Debugf(ctx, "Final input variable count: %d", len(finalVars))
	return finalVars, diags
}

func (m *Module) buildInitialContext(ctx context.Context) hcl.Diagnostics {
	var diags hcl.Diagnostics
	m.logger.Debugf(ctx, "Building initial evaluation context")

	cwd, err := filepath.Abs(".")
	if err != nil {
		diags = diags.Append(&hcl.Diagnostic{Severity: hcl.DiagWarning, Summary: "Failed to get current working directory", Detail: err.Error()})
		cwd = "."
	}
	modulePath, err := filepath.Abs(m.path)
	if err != nil {
		diags = diags.Append(&hcl.Diagnostic{Severity: hcl.DiagWarning, Summary: "Failed to get module directory absolute path", Detail: err.Error()})
		modulePath = m.path
	}

	m.evalContext = &hcl.EvalContext{
		Variables: map[string]cty.Value{
			"var":       cty.ObjectVal(m.inputVars),
			"path":      cty.ObjectVal(map[string]cty.Value{"module": cty.StringVal(modulePath), "root": cty.StringVal(modulePath), "cwd": cty.StringVal(cwd)}),
			"terraform": cty.ObjectVal(map[string]cty.Value{"workspace": cty.StringVal(m.workspace)}),
			"local":     cty.EmptyObjectVal,
		},
		Functions: StandardFunctions(),
	}
	m.logger.Debugf(ctx, "Initial context built")
	return diags
}

func syntaxBlockToHclBlock(syntaxBlock *hclsyntax.Block, parentBody hcl.Body) *hcl.Block {
	schema := &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: syntaxBlock.Type, LabelNames: syntaxBlock.Labels}}}
	content, _, _ := parentBody.PartialContent(schema) // Ignore diags for this helper

	for _, b := range content.Blocks {
		if b.Type == syntaxBlock.Type && len(b.Labels) == len(syntaxBlock.Labels) && b.DefRange.Start == syntaxBlock.TypeRange.Start {
			match := true
			for i := range b.Labels {
				if b.Labels[i] != syntaxBlock.Labels[i] {
					match = false
					break
				}
			}
			if match {
				return b
			}
		}
	}
	return nil
}
