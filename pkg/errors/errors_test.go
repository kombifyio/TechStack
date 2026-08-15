package errors

import (
	"errors"
	"fmt"
	"testing"
)

func TestWrap(t *testing.T) {
	tests := []struct {
		name string
		err  error
		msg  string
		want string
	}{
		{
			name: "wrap error",
			err:  errors.New("original error"),
			msg:  "context message",
			want: "context message: original error",
		},
		{
			name: "wrap nil error",
			err:  nil,
			msg:  "context message",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Wrap(tt.err, tt.msg)
			if got == nil {
				if tt.want != "" {
					t.Errorf("Wrap() = nil, want %q", tt.want)
				}
				return
			}
			if got.Error() != tt.want {
				t.Errorf("Wrap() = %q, want %q", got.Error(), tt.want)
			}
			if tt.err != nil && !errors.Is(got, tt.err) {
				t.Errorf("Wrap() does not wrap original error")
			}
		})
	}
}

func TestWrapf(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		format string
		args   []interface{}
		want   string
	}{
		{
			name:   "wrapf with formatting",
			err:    errors.New("original error"),
			format: "failed to process %s",
			args:   []interface{}{"item1"},
			want:   "failed to process item1: original error",
		},
		{
			name:   "wrapf nil error",
			err:    nil,
			format: "failed to process %s",
			args:   []interface{}{"item1"},
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Wrapf(tt.err, tt.format, tt.args...)
			if got == nil {
				if tt.want != "" {
					t.Errorf("Wrapf() = nil, want %q", tt.want)
				}
				return
			}
			if got.Error() != tt.want {
				t.Errorf("Wrapf() = %q, want %q", got.Error(), tt.want)
			}
		})
	}
}

func TestIs(t *testing.T) {
	baseErr := errors.New("base error")
	wrappedErr := Wrap(baseErr, "context")

	tests := []struct {
		name   string
		err    error
		target error
		want   bool
	}{
		{
			name:   "is base error",
			err:    baseErr,
			target: baseErr,
			want:   true,
		},
		{
			name:   "is wrapped error",
			err:    wrappedErr,
			target: baseErr,
			want:   true,
		},
		{
			name:   "is validation error",
			err:    ErrValidation,
			target: ErrValidation,
			want:   true,
		},
		{
			name:   "not same error",
			err:    ErrValidation,
			target: ErrNotFound,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Is(tt.err, tt.target)
			if got != tt.want {
				t.Errorf("Is() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidationError(t *testing.T) {
	t.Run("without value", func(t *testing.T) {
		err := NewValidationError("username", "must not be empty")
		want := `validation error on field "username": must not be empty`
		if err.Error() != want {
			t.Errorf("Error() = %q, want %q", err.Error(), want)
		}

		if !Is(err, ErrValidation) {
			t.Error("ValidationError should match ErrValidation")
		}
	})

	t.Run("with value", func(t *testing.T) {
		err := NewValidationErrorWithValue("username", "invalid format", "user@")
		want := `validation error on field "username": invalid format (value: "user@")`
		if err.Error() != want {
			t.Errorf("Error() = %q, want %q", err.Error(), want)
		}
	})
}

func TestMultiError(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		me := NewMultiError()
		if me.HasErrors() {
			t.Error("new MultiError should have no errors")
		}
		if me.ErrorOrNil() != nil {
			t.Error("ErrorOrNil() should return nil for empty MultiError")
		}
	})

	t.Run("single error", func(t *testing.T) {
		me := NewMultiError()
		err := errors.New("error 1")
		me.Add(err)

		if !me.HasErrors() {
			t.Error("MultiError should have errors")
		}
		if me.ErrorOrNil() == nil {
			t.Error("ErrorOrNil() should not return nil")
		}
		if me.Error() != "error 1" {
			t.Errorf("Error() = %q, want %q", me.Error(), "error 1")
		}
	})

	t.Run("multiple errors", func(t *testing.T) {
		me := NewMultiError()
		me.Add(errors.New("error 1"))
		me.Add(errors.New("error 2"))
		me.Add(errors.New("error 3"))

		if !me.HasErrors() {
			t.Error("MultiError should have errors")
		}

		errMsg := me.Error()
		expected := "3 errors occurred:"
		if len(errMsg) < len(expected) || errMsg[:len(expected)] != expected {
			t.Errorf("Error() should start with %q, got %q", expected, errMsg)
		}
	})

	t.Run("add nil error", func(t *testing.T) {
		me := NewMultiError()
		me.Add(nil)

		if me.HasErrors() {
			t.Error("MultiError should not have errors when nil is added")
		}
	})

	t.Run("mixed nil and real errors", func(t *testing.T) {
		me := NewMultiError()
		me.Add(errors.New("error 1"))
		me.Add(nil)
		me.Add(errors.New("error 2"))
		me.Add(nil)

		if len(me.Errors) != 2 {
			t.Errorf("MultiError should have 2 errors, got %d", len(me.Errors))
		}
	})
}

func TestErrorTypes(t *testing.T) {
	// Test that all predefined errors are not nil
	errorTypes := []struct {
		name string
		err  error
	}{
		{"ErrValidation", ErrValidation},
		{"ErrNotFound", ErrNotFound},
		{"ErrAlreadyExists", ErrAlreadyExists},
		{"ErrUnauthorized", ErrUnauthorized},
		{"ErrForbidden", ErrForbidden},
		{"ErrInternal", ErrInternal},
		{"ErrTimeout", ErrTimeout},
		{"ErrUnavailable", ErrUnavailable},
		{"ErrInvalidState", ErrInvalidState},
		{"ErrConflict", ErrConflict},
	}

	for _, tt := range errorTypes {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err == nil {
				t.Errorf("%s should not be nil", tt.name)
			}
			if tt.err.Error() == "" {
				t.Errorf("%s should have an error message", tt.name)
			}
		})
	}
}

func TestErrorWrapping(t *testing.T) {
	// Test that wrapped errors preserve the chain
	base := ErrNotFound
	wrapped1 := Wrap(base, "level 1")
	wrapped2 := Wrap(wrapped1, "level 2")
	wrapped3 := Wrap(wrapped2, "level 3")

	if !Is(wrapped3, base) {
		t.Error("wrapped error should match base error")
	}

	// Test the error message contains all contexts
	msg := wrapped3.Error()
	if len(msg) == 0 {
		t.Error("wrapped error should have a message")
	}
}

// Example of how to use the errors package
func ExampleWrap() {
	err := fmt.Errorf("connection failed")
	wrapped := Wrap(err, "failed to connect to database")
	fmt.Println(wrapped)
	// Output: failed to connect to database: connection failed
}

func ExampleNewValidationError() {
	err := NewValidationError("email", "must be a valid email address")
	fmt.Println(err)
	// Output: validation error on field "email": must be a valid email address
}

func ExampleMultiError() {
	me := NewMultiError()
	me.Add(errors.New("error 1"))
	me.Add(errors.New("error 2"))

	if me.HasErrors() {
		fmt.Println("Multiple errors occurred:", me.Error())
	}
	// Output: Multiple errors occurred: 2 errors occurred:
	//   1. error 1
	//   2. error 2
}
