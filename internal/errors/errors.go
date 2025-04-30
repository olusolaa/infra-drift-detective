package errors

import (
	"errors"
	"fmt"
	"runtime/debug"
)

type AppError struct {
	Code            Code
	Message         string
	InternalDetails string
	IsUserFacing    bool
	SuggestedAction string
	WrappedError    error
	StackTrace      string
}

func (e *AppError) Error() string {
	if e.WrappedError != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.WrappedError)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func (e *AppError) Unwrap() error {
	return e.WrappedError
}

func New(code Code, message string) *AppError {
	return &AppError{
		Code:         code,
		Message:      message,
		IsUserFacing: false, // Default to not user facing
		StackTrace:   string(debug.Stack()),
	}
}

func NewUserFacing(code Code, message string, suggestion string) *AppError {
	return &AppError{
		Code:            code,
		Message:         message,
		IsUserFacing:    true,
		SuggestedAction: suggestion,
		StackTrace:      string(debug.Stack()),
	}
}

func Wrap(err error, code Code, message string) *AppError {
	if err == nil {
		return nil
	}

	var appErr *AppError
	if errors.As(err, &appErr) {
		// If it's already an AppError, perhaps just update code/message if needed,
		// or return as is to preserve original context. Let's return as is.
		return appErr
	}

	return &AppError{
		Code:         code,
		Message:      message,
		WrappedError: err,
		IsUserFacing: false,
		StackTrace:   string(debug.Stack()),
	}
}

func WrapUserFacing(err error, code Code, message string, suggestion string) *AppError {
	if err == nil {
		return nil
	}

	var appErr *AppError
	if errors.As(err, &appErr) {
		// If it's already an AppError, maybe update user-facing properties?
		// For now, let's wrap it again if we want a *new* user-facing message.
		// Or, perhaps better, let the original user-facing property stand.
		// Let's prioritize the original IsUserFacing property.
		// If we specifically want THIS layer to make it user-facing, create a new one.
		// This needs careful thought based on error propagation strategy.
		// Let's assume WrapUserFacing means "ensure this is user facing now".
		return &AppError{
			Code:            code,    // Or appErr.Code if preferred
			Message:         message, // Or appErr.Message
			InternalDetails: appErr.Error(),
			IsUserFacing:    true,
			SuggestedAction: suggestion,
			WrappedError:    err,               // Keep original chain
			StackTrace:      appErr.StackTrace, // Keep original stack
		}

	}

	return &AppError{
		Code:            code,
		Message:         message,
		WrappedError:    err,
		IsUserFacing:    true,
		SuggestedAction: suggestion,
		StackTrace:      string(debug.Stack()),
	}
}

func GetCode(err error) Code {
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr.Code
	}
	return CodeUnknown
}

func Is(err error, code Code) bool {
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr.Code == code
	}
	return false
}

func As(err error, target any) bool {
	if errors.As(err, &target) {
		return true
	}
	return false
}

func GetUserFacingMessage(err error) (string, string, bool) {
	var appErr *AppError
	if errors.As(err, &appErr) {
		if appErr.IsUserFacing {
			return appErr.Message, appErr.SuggestedAction, true
		}
		// Optionally traverse wrapped errors to find the *first* user-facing one
		nextErr := errors.Unwrap(appErr)
		for nextErr != nil {
			if errors.As(nextErr, &appErr) {
				if appErr.IsUserFacing {
					return appErr.Message, appErr.SuggestedAction, true
				}
				nextErr = errors.Unwrap(appErr)
			} else {
				break
			}
		}
	}
	// Default generic message if no specific user-facing error found
	return "An unexpected error occurred.", "Check logs for more details.", false
}
