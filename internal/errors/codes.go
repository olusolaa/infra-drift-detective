package errors

type Code string

const (
	CodeUnknown            Code = "UNKNOWN"
	CodeInternal           Code = "INTERNAL_ERROR"
	CodeConfigValidation   Code = "CONFIG_VALIDATION_ERROR"
	CodeConfigReadError    Code = "CONFIG_READ_ERROR"
	CodeConfigParseError   Code = "CONFIG_PARSE_ERROR"
	CodeConfigNotFound     Code = "CONFIG_NOT_FOUND"
	CodeStateReadError     Code = "STATE_READ_ERROR"
	CodeStateParseError    Code = "STATE_PARSE_ERROR"
	CodePlatformAPIError   Code = "PLATFORM_API_ERROR"
	CodePlatformAuthError  Code = "PLATFORM_AUTH_ERROR"
	CodeResourceNotFound   Code = "RESOURCE_NOT_FOUND"
	CodeMatchingError      Code = "MATCHING_ERROR"
	CodeComparisonError    Code = "COMPARISON_ERROR"
	CodeTypeAssertionError Code = "TYPE_ASSERTION_ERROR"
	CodeNotImplemented     Code = "NOT_IMPLEMENTED"
	CodeTimeout            Code = "TIMEOUT_ERROR"

	// HCL specific error codes
	CodeHCLParseError       Code = "HCL_PARSE_ERROR"
	CodeHCLEvalError        Code = "HCL_EVAL_ERROR"
	CodeHCLAttributeError   Code = "HCL_ATTRIBUTE_ERROR"
	CodeHCLVariableError    Code = "HCL_VARIABLE_ERROR"
	CodeHCLBlockError       Code = "HCL_BLOCK_ERROR"
	CodeStateProviderError  Code = "STATE_PROVIDER_ERROR"
	CodeMappingError        Code = "MAPPING_ERROR"
	CodeNotImplementedError Code = "NOT_IMPLEMENTED_ERROR"
	// Add more specific codes as needed
)

func (c Code) String() string {
	return string(c)
}
