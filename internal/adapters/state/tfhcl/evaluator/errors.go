package evaluator

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
)

// EvaluatedResource represents the evaluated attributes and blocks of a resource.
type EvaluatedResource map[string]any

// HCLDiagnosticsError wraps HCL diagnostics for clearer error propagation.
type HCLDiagnosticsError struct {
	Operation string
	FilePath  string // Can represent the directory or primary file involved
	Diags     hcl.Diagnostics
}

func (e *HCLDiagnosticsError) Error() string {
	return fmt.Sprintf("HCL %s error processing %q: %s", e.Operation, e.FilePath, e.Diags.Error())
}

// VariableLoadError indicates failure loading a .tfvars file.
type VariableLoadError struct {
	VarFilePath string
	Err         error // Underlying error (e.g., file read, parse diag)
}

func (e *VariableLoadError) Error() string {
	return fmt.Sprintf("failed to load variables from %q: %v", e.VarFilePath, e.Err)
}
func (e *VariableLoadError) Unwrap() error { return e.Err }

// ValueConversionError indicates failure converting cty.Value to a Go type.
type ValueConversionError struct {
	AttributeName string // Optional: Attribute where conversion failed
	Err           error
}

func (e *ValueConversionError) Error() string {
	if e.AttributeName != "" {
		return fmt.Sprintf("error converting value for attribute %q: %v", e.AttributeName, e.Err)
	}
	return fmt.Sprintf("error converting cty value: %v", e.Err)
}
func (e *ValueConversionError) Unwrap() error { return e.Err }

// ResourceEvaluationError indicates failure during resource block evaluation, wrapping diagnostics.
type ResourceEvaluationError struct {
	Address string
	Diags   hcl.Diagnostics
}

func (e *ResourceEvaluationError) Error() string {
	return fmt.Sprintf("error evaluating resource %s: %s", e.Address, e.Diags.Error())
}
