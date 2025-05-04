package evaluator

import (
	"context"
	"path/filepath"
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
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
		inputVars: make(map[string]cty.Value),
		initDiags: parseDiags,
	}

	mod.initDiags = append(mod.initDiags, mod.loadVariables(ctx, parser, files, varFilePaths)...)
	if DiagsHasFatalErrors(mod.initDiags) {
		return files, mod, apperrors.Wrap(&HCLDiagnosticsError{Operation: "loading variables", FilePath: dirPath, Diags: mod.initDiags}, apperrors.CodeStateParseError, "fatal errors loading variables")
	}
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

	mod.initDiags = append(mod.initDiags, mod.evaluateLocals(ctx, files)...)
	if DiagsHasFatalErrors(mod.initDiags) {
		return files, mod, apperrors.Wrap(&HCLDiagnosticsError{Operation: "evaluating locals", FilePath: dirPath, Diags: mod.initDiags}, apperrors.CodeStateParseError, "fatal errors evaluating locals")
	}
	if err := ctx.Err(); err != nil {
		return files, mod, err
	}

	if len(mod.initDiags) > 0 {
		logger.Warnf(ctx, "Non-fatal diagnostics during module load:\n%s", mod.initDiags.Error())
	}

	return files, mod, nil
}

func (m *Module) EvalContext() *hcl.EvalContext {
	m.evalMutex.RLock()
	defer m.evalMutex.RUnlock()
	return m.evalContext
}

func (m *Module) loadVariables(ctx context.Context, parser *hclparse.Parser, files map[string]*hcl.File, varFilePaths []string) hcl.Diagnostics {
	var diags hcl.Diagnostics
	m.variables = make(map[string]*VariableDefinition)
	var variableBlocks []*hcl.Block
	schema := &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "variable", LabelNames: []string{"name"}}}}

	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return diags
		}
		content, contentDiags := file.Body.Content(schema)
		diags = append(diags, contentDiags...)
		for _, b := range content.Blocks {
			if b.Type == "variable" {
				variableBlocks = append(variableBlocks, b)
			}
		}
	}
	if DiagsHasFatalErrors(diags) {
		return diags
	}

	definedVars := make(map[string]string)
	for _, block := range variableBlocks {
		if err := ctx.Err(); err != nil {
			return diags
		}
		varName := block.Labels[0]
		if prevPath, exists := definedVars[varName]; exists {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Duplicate variable definition",
				Detail:   "Variable " + varName + " was already defined at " + prevPath,
				Subject:  &block.DefRange,
			})
			continue
		}
		definedVars[varName] = block.DefRange.Filename

		def, varDiags := decodeVariableBlock(block)
		diags = append(diags, varDiags...)
		if def != nil {
			m.variables[varName] = def
		}
	}
	if DiagsHasFatalErrors(diags) {
		return diags
	}

	loadedTfVars := make(map[string]cty.Value)
	varsFileAttrRanges := make(map[string]map[string]hcl.Range)

	for _, path := range varFilePaths {
		if err := ctx.Err(); err != nil {
			return diags
		}
		if path == "" {
			continue
		}
		vars, loadDiags := loadVarsFromFile(ctx, parser, path, m.logger)
		diags = append(diags, loadDiags...)
		if DiagsHasFatalErrors(loadDiags) {
			continue
		}

		varsFileAttrRanges[path] = make(map[string]hcl.Range)
		parsedVarsFile, parseVarsDiags := parser.ParseHCLFile(path)
		diags = append(diags, parseVarsDiags...)
		if !DiagsHasFatalErrors(parseVarsDiags) && parsedVarsFile != nil {
			attrsFromFile, attrDiags := parsedVarsFile.Body.JustAttributes()
			diags = append(diags, attrDiags...)
			if !DiagsHasFatalErrors(attrDiags) {
				for name, attr := range attrsFromFile {
					varsFileAttrRanges[path][name] = attr.Range
				}
			}
		}

		for name, val := range vars {
			if err := ctx.Err(); err != nil {
				return diags
			}
			if _, defined := m.variables[name]; !defined {
				var subjectRange *hcl.Range
				if rangesMap, ok := varsFileAttrRanges[path]; ok {
					if attrRange, attrOk := rangesMap[name]; attrOk {
						subjectRange = &attrRange
					}
				}
				if subjectRange == nil {
					subjectRange = &hcl.Range{Filename: path}
				}
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagWarning,
					Summary:  "Undefined variable in vars file",
					Detail:   "Variable " + name + " is set in " + path + " but not defined in the module.",
					Subject:  subjectRange,
				})
			} else {
				loadedTfVars[name] = val
			}
		}
	}
	if DiagsHasFatalErrors(diags) {
		return diags
	}

	m.inputVars = make(map[string]cty.Value)
	for name, def := range m.variables {
		if err := ctx.Err(); err != nil {
			return diags
		}
		if val, ok := loadedTfVars[name]; ok {
			convVal, convDiags := ConvertVarType(val, def.Type, def.DeclRange)
			diags = append(diags, convDiags...)
			if !DiagsHasFatalErrors(convDiags) {
				m.inputVars[name] = convVal
			}
		} else if !def.Default.IsNull() && def.Default.IsKnown() {
			m.inputVars[name] = def.Default
		} else {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Missing required variable",
				Detail:   "Variable " + name + " has no default value and was not provided.",
				Subject:  &def.DeclRange,
			})
		}
	}

	return diags
}

func (m *Module) buildInitialContext(ctx context.Context) hcl.Diagnostics {
	var diags hcl.Diagnostics
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
	return diags
}

func (m *Module) evaluateLocals(ctx context.Context, files map[string]*hcl.File) hcl.Diagnostics {
	var allDiags hcl.Diagnostics
	locals := make(map[string]cty.Value)
	definedLocals := make(map[string]string)
	var localsBlocks []*hcl.Block
	schema := &hcl.BodySchema{Blocks: []hcl.BlockHeaderSchema{{Type: "locals"}}}

	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return allDiags
		}
		content, contentDiags := file.Body.Content(schema)
		allDiags = append(allDiags, contentDiags...)
		localsBlocks = append(localsBlocks, content.Blocks...)
	}
	if DiagsHasFatalErrors(allDiags) {
		return allDiags
	}

	for _, block := range localsBlocks {
		if err := ctx.Err(); err != nil {
			return allDiags
		}
		attrs, attrDiags := block.Body.JustAttributes()
		allDiags = append(allDiags, attrDiags...)
		if DiagsHasFatalErrors(attrDiags) {
			continue
		}

		for name, attr := range attrs {
			if err := ctx.Err(); err != nil {
				return allDiags
			}
			if definedAt, exists := definedLocals[name]; exists {
				allDiags = allDiags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Duplicate local value definition",
					Detail:   "Local value " + name + " was already defined at " + definedAt,
					Subject:  &attr.NameRange,
				})
				continue
			}
			definedLocals[name] = attr.NameRange.String()

			val, valDiags := attr.Expr.Value(m.evalContext)
			allDiags = append(allDiags, valDiags...)
			if !DiagsHasFatalErrors(valDiags) {
				locals[name] = val
			}
		}
	}

	if DiagsHasFatalErrors(allDiags) {
		return allDiags
	}

	if len(locals) > 0 {
		m.evalMutex.Lock()
		m.locals = locals
		m.evalContext.Variables["local"] = cty.ObjectVal(locals)
		m.evalMutex.Unlock()
	}

	return allDiags
}
