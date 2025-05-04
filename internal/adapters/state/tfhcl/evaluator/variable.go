package evaluator

import (
	"context"
	"os"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/olusolaa/infra-drift-detector/internal/core/ports"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
)

var variableBlockSchema = &hcl.BodySchema{
	Attributes: []hcl.AttributeSchema{
		{Name: "type"},
		{Name: "description"},
		{Name: "default"},
		{Name: "sensitive"},
	},
}

func simpleTypeFromString(s string) cty.Type {
	switch strings.ToLower(s) {
	case "string":
		return cty.String
	case "number":
		return cty.Number
	case "bool", "boolean":
		return cty.Bool
	case "any":
		return cty.DynamicPseudoType
	default:
		return cty.DynamicPseudoType
	}
}

func decodeVariableBlock(block *hcl.Block) (*VariableDefinition, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	content, contentDiags := block.Body.Content(variableBlockSchema)
	diags = append(diags, contentDiags...)
	if DiagsHasFatalErrors(contentDiags) {
		return nil, diags
	}

	def := &VariableDefinition{
		Name:      block.Labels[0],
		FilePath:  block.DefRange.Filename,
		DeclRange: block.DefRange,
		Type:      cty.DynamicPseudoType,
	}

	if attr, exists := content.Attributes["type"]; exists {
		var targetType cty.Type
		typeDiags := gohcl.DecodeExpression(attr.Expr, nil, &targetType)
		diags = append(diags, typeDiags...)
		if !DiagsHasFatalErrors(typeDiags) && targetType != cty.DynamicPseudoType {
			def.Type = targetType
		} else {
			strVal, strDiags := attr.Expr.Value(nil)
			diags = append(diags, strDiags...)
			if !DiagsHasFatalErrors(strDiags) && strVal.IsKnown() && !strVal.IsNull() && strVal.Type() == cty.String {
				def.Type = simpleTypeFromString(strVal.AsString())
			}
		}
	}

	if attr, exists := content.Attributes["description"]; exists {
		descVal, descDiags := attr.Expr.Value(nil)
		diags = append(diags, descDiags...)
		if !DiagsHasFatalErrors(descDiags) && !descVal.IsNull() && descVal.IsKnown() && descVal.Type() == cty.String {
			def.Description = descVal.AsString()
		} else if !descVal.IsNull() && descVal.IsKnown() {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError, Summary: "Invalid variable description",
				Detail: "The 'description' attribute must be a string.", Subject: attr.Expr.Range().Ptr(),
			})
		}
	}

	if attr, exists := content.Attributes["default"]; exists {
		defaultVal, defaultDiags := attr.Expr.Value(nil)
		diags = append(diags, defaultDiags...)
		if !DiagsHasFatalErrors(defaultDiags) {
			convVal, convDiags := ConvertVarType(defaultVal, def.Type, attr.Expr.Range())
			diags = append(diags, convDiags...)
			if !DiagsHasFatalErrors(convDiags) {
				def.Default = convVal
			}
		}
	}

	if attr, exists := content.Attributes["sensitive"]; exists {
		sensVal, sensDiags := attr.Expr.Value(nil)
		diags = append(diags, sensDiags...)
		if !DiagsHasFatalErrors(sensDiags) && !sensVal.IsNull() && sensVal.IsKnown() && sensVal.Type() == cty.Bool {
			if sensVal.True() {
				def.Sensitive = true
			}
		} else if !sensVal.IsNull() && sensVal.IsKnown() {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError, Summary: "Invalid sensitive value",
				Detail: "The 'sensitive' attribute must be a boolean.", Subject: attr.Expr.Range().Ptr(),
			})
		}
	}

	if DiagsHasFatalErrors(diags) {
		return nil, diags
	}
	return def, diags
}

func loadVarsFromFile(ctx context.Context, parser *hclparse.Parser, path string, logger ports.Logger) (map[string]cty.Value, hcl.Diagnostics) {
	vars := make(map[string]cty.Value)
	var diags hcl.Diagnostics
	logger = logger.WithFields(map[string]any{"vars_file": path})

	if err := ctx.Err(); err != nil {
		logger.Warnf(ctx, "Context cancelled before reading vars file %s", path)
		return vars, diags
	}

	src, err := os.ReadFile(path)
	if err != nil {
		diags = diags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Cannot read variables file", Detail: err.Error(), Subject: &hcl.Range{Filename: path}})
		return nil, diags
	}

	if err := ctx.Err(); err != nil {
		logger.Warnf(ctx, "Context cancelled after reading, before parsing vars file %s", path)
		return vars, diags
	}

	var file *hcl.File
	var parseDiags hcl.Diagnostics
	if strings.HasSuffix(path, ".json") {
		file, parseDiags = parser.ParseJSON(src, path)
	} else {
		file, parseDiags = parser.ParseHCL(src, path)
	}
	diags = append(diags, parseDiags...)

	if file == nil && !DiagsHasFatalErrors(diags) {
		diags = diags.Append(&hcl.Diagnostic{Severity: hcl.DiagError, Summary: "Internal HCL parsing error", Detail: "Parser returned nil file without diagnostics.", Subject: &hcl.Range{Filename: path}})
	}
	if file == nil || DiagsHasFatalErrors(diags) {
		return nil, diags
	}

	attrs, attrDiags := file.Body.JustAttributes()
	diags = append(diags, attrDiags...)
	if DiagsHasFatalErrors(attrDiags) {
		return nil, diags
	}

	evalCtx := &hcl.EvalContext{}
	for name, attr := range attrs {
		if err := ctx.Err(); err != nil {
			logger.Warnf(ctx, "Context cancelled during vars file attribute evaluation loop (%s)", path)
			return vars, diags
		}
		val, valDiags := attr.Expr.Value(evalCtx)
		diags = append(diags, valDiags...)
		if !DiagsHasFatalErrors(valDiags) {
			vars[name] = val
		}
	}
	return vars, diags
}

func ConvertVarType(val cty.Value, targetType cty.Type, subjectRange hcl.Range) (cty.Value, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	if targetType == cty.DynamicPseudoType {
		return val, diags
	}
	convVal, err := convert.Convert(val, targetType)
	if err != nil {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError, Summary: "Incorrect variable type",
			Detail: err.Error(), Subject: &subjectRange,
		})
		return cty.NilVal, diags
	}
	return convVal, diags
}
