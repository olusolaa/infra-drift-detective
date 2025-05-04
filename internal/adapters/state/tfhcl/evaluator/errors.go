package evaluator

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
)

type HCLDiagnosticsError struct {
	Operation string
	FilePath  string
	Address   string
	Diags     hcl.Diagnostics
}

func (e *HCLDiagnosticsError) Error() string {
	op := e.Operation
	if op == "" {
		op = "processing"
	}
	subj := e.FilePath
	if e.Address != "" {
		subj = e.Address
	}
	if subj == "" {
		subj = "HCL content"
	}
	return fmt.Sprintf("HCL %s error(s) in %s: %s", op, subj, e.Diags.Error())
}

type ValueConversionError struct {
	AttributeName string
	Err           error
}

func (e *ValueConversionError) Error() string {
	msg := "error converting cty value"
	if e.AttributeName != "" {
		msg += fmt.Sprintf(" for attribute %q", e.AttributeName)
	}
	return fmt.Sprintf("%s: %v", msg, e.Err)
}
func (e *ValueConversionError) Unwrap() error { return e.Err }

func DiagsHasFatalErrors(diags hcl.Diagnostics) bool {
	return diags.HasErrors()
}
