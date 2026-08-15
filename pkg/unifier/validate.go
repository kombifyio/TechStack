// Package unifier provides the Unifier Engine implementation.
// This file contains validation methods for the Engine.
package unifier

import (
	"context"
	"fmt"
	"strings"

	"cuelang.org/go/cue"
	cueerrors "cuelang.org/go/cue/errors"
	"github.com/kombifyio/techstack/pkg/core"
)

// Validate checks a KombinationSpec against CUE schemas and policies.
// Returns validation result without executing any infrastructure changes.
// Robustheit: Verwendet Timeout um nie ewig zu blockieren.
func (e *Engine) Validate(spec *core.KombinationSpec) (*core.ValidationResult, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// Robustheit: nil-Spec wird mit leerer Config behandelt.
	// NOTE: This does not imply the spec is valid; required fields may still be missing.
	if spec == nil {
		spec = &core.KombinationSpec{Name: "default"}
	}

	// Convert Go struct to CUE value
	specValue := e.ctx.Encode(spec)
	if specValue.Err() != nil {
		return &core.ValidationResult{
			Valid: false,
			Errors: []core.ValidationError{{
				Path:    "",
				Code:    "encoding_error",
				Message: fmt.Sprintf("failed to encode spec for validation: %v", specValue.Err()),
			}},
		}, nil
	}

	// Get the schema definition
	schemaPath := cue.ParsePath("#KombinationSpec")
	schemaDef := e.schema.LookupPath(schemaPath)
	if schemaDef.Err() != nil {
		return &core.ValidationResult{
			Valid: false,
			Errors: []core.ValidationError{{
				Path:    "",
				Code:    "schema_error",
				Message: fmt.Sprintf("schema lookup failed: %v", schemaDef.Err()),
			}},
		}, nil
	}

	// Robustheit: CUE-Validation mit Timeout (nie blockieren)
	ctx, cancel := context.WithTimeout(context.Background(), DefaultValidationTimeout)
	defer cancel()

	if err := ValidateWithTimeout(ctx, schemaDef, specValue); err != nil {
		if ctx.Err() != nil {
			return &core.ValidationResult{
				Valid: false,
				Errors: []core.ValidationError{{
					Path:    "",
					Code:    "timeout_error",
					Message: "CUE validation timed out",
				}},
			}, nil
		}

		return &core.ValidationResult{
			Valid:  false,
			Errors: extractValidationErrors(err),
		}, nil
	}

	// If StackKit is specified and loader is available, validate against kit.
	// This is strict: invalid StackKit schema must block downstream provisioning.
	if spec.Kit != "" && e.kitLoader != nil {
		if kitErr := e.kitLoader.ValidateAgainstKit(spec.Kit, specValue); kitErr != nil {
			// StackKit validation failed
			return &core.ValidationResult{
				Valid: false,
				Errors: []core.ValidationError{
					{
						Path:    "kit",
						Code:    "stackkit_validation_error",
						Message: fmt.Sprintf("StackKit validation failed: %v", kitErr),
					},
				},
			}, nil
		}
	}

	return &core.ValidationResult{
		Valid:  true,
		Errors: nil,
	}, nil
}

// extractValidationErrors converts CUE errors to ValidationError slice with path extraction.
func extractValidationErrors(err error) []core.ValidationError {
	if err == nil {
		return nil
	}

	var errors []core.ValidationError
	for _, e := range cueerrors.Errors(err) {
		path := strings.Join(e.Path(), ".")
		errors = append(errors, core.ValidationError{
			Path:    path,
			Code:    "validation_error",
			Message: e.Error(),
		})
	}

	if len(errors) == 0 {
		errors = append(errors, core.ValidationError{
			Path:    "",
			Code:    "validation_error",
			Message: err.Error(),
		})
	}

	return errors
}
