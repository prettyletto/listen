// Package apperr defines user-facing errors with stable codes and short hints.
package apperr

import (
	"errors"
)

// Error separates concise user text from an optional underlying cause.
type Error struct {
	Code    string
	Message string
	Hint    string
	Cause   error
}

func New(code, message, hint string, cause error) *Error {
	return &Error{Code: code, Message: message, Hint: hint, Cause: cause}
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// HasCode reports whether err or one of its wrappers has the supplied code.
func HasCode(err error, code string) bool {
	var target *Error
	return errors.As(err, &target) && target.Code == code
}

// Wrap adds a stable user message while preserving an underlying cause.
func Wrap(code, message, hint string, cause error) error {
	return New(code, message, hint, cause)
}
