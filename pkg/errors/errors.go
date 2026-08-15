// Package errors provides standardized error types and handling for kombifyTechstack.
package errors

import (
	"errors"
	"fmt"
)

// Common error types that can be used across the application.
var (
	// ErrValidation indicates input validation failed
	ErrValidation = errors.New("validation error")

	// ErrNotFound indicates a requested resource was not found
	ErrNotFound = errors.New("not found")

	// ErrAlreadyExists indicates a resource already exists
	ErrAlreadyExists = errors.New("already exists")

	// ErrUnauthorized indicates authentication or authorization failed
	ErrUnauthorized = errors.New("unauthorized")

	// ErrForbidden indicates the operation is not permitted
	ErrForbidden = errors.New("forbidden")

	// ErrInternal indicates an internal server error occurred
	ErrInternal = errors.New("internal error")

	// ErrTimeout indicates an operation timed out
	ErrTimeout = errors.New("timeout")

	// ErrUnavailable indicates a service is temporarily unavailable
	ErrUnavailable = errors.New("service unavailable")

	// ErrInvalidState indicates an operation was attempted in an invalid state
	ErrInvalidState = errors.New("invalid state")

	// ErrConflict indicates a conflicting operation
	ErrConflict = errors.New("conflict")
)

// Wrap adds context to an error. If err is nil, returns nil.
// This follows the standard error wrapping pattern using %w.
//
// Example:
//
//	if err := doSomething(); err != nil {
//	    return errors.Wrap(err, "failed to do something")
//	}
func Wrap(err error, msg string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", msg, err)
}

// Wrapf adds formatted context to an error. If err is nil, returns nil.
//
// Example:
//
//	if err := doSomething(id); err != nil {
//	    return errors.Wrapf(err, "failed to do something with id %s", id)
//	}
func Wrapf(err error, format string, args ...interface{}) error {
	if err == nil {
		return nil
	}
	msg := fmt.Sprintf(format, args...)
	return fmt.Errorf("%s: %w", msg, err)
}

// Is reports whether any error in err's chain matches target.
// This is a convenience wrapper around errors.Is.
func Is(err, target error) bool {
	return errors.Is(err, target)
}

// As finds the first error in err's chain that matches target.
// This is a convenience wrapper around errors.As.
func As(err error, target interface{}) bool {
	return errors.As(err, target)
}

// ValidationError represents a validation error with detailed information.
type ValidationError struct {
	Field   string // Field that failed validation
	Message string // Human-readable error message
	Value   string // Optional: the invalid value
}

func (e *ValidationError) Error() string {
	if e.Value != "" {
		return fmt.Sprintf("validation error on field %q: %s (value: %q)", e.Field, e.Message, e.Value)
	}
	return fmt.Sprintf("validation error on field %q: %s", e.Field, e.Message)
}

// Is implements error matching for ValidationError.
func (e *ValidationError) Is(target error) bool {
	return target == ErrValidation
}

// NewValidationError creates a new validation error.
func NewValidationError(field, message string) error {
	return &ValidationError{
		Field:   field,
		Message: message,
	}
}

// NewValidationErrorWithValue creates a validation error with the invalid value.
func NewValidationErrorWithValue(field, message, value string) error {
	return &ValidationError{
		Field:   field,
		Message: message,
		Value:   value,
	}
}

// MultiError represents multiple errors that occurred.
type MultiError struct {
	Errors []error
}

func (e *MultiError) Error() string {
	if len(e.Errors) == 0 {
		return "no errors"
	}
	if len(e.Errors) == 1 {
		return e.Errors[0].Error()
	}
	msg := fmt.Sprintf("%d errors occurred:", len(e.Errors))
	for i, err := range e.Errors {
		msg += fmt.Sprintf("\n  %d. %v", i+1, err)
	}
	return msg
}

// Add adds an error to the MultiError. If err is nil, it's not added.
func (e *MultiError) Add(err error) {
	if err != nil {
		e.Errors = append(e.Errors, err)
	}
}

// HasErrors returns true if there are any errors.
func (e *MultiError) HasErrors() bool {
	return len(e.Errors) > 0
}

// ErrorOrNil returns nil if there are no errors, otherwise returns the MultiError.
func (e *MultiError) ErrorOrNil() error {
	if !e.HasErrors() {
		return nil
	}
	return e
}

// NewMultiError creates a new MultiError.
func NewMultiError() *MultiError {
	return &MultiError{
		Errors: make([]error, 0),
	}
}
