// Package unifier provides the Unifier Engine implementation.
// This file contains StackKit API methods for the Engine.
package unifier

import (
	"fmt"

	"github.com/kombifyio/techstack/pkg/core"
)

// ========== StackKit API Methods ==========

// ListStackKits returns all available StackKit names.
func (e *Engine) ListStackKits() []string {
	if e.kitLoader == nil {
		return []string{StackKitBasement, StackKitCloud}
	}
	return e.kitLoader.ListAvailableKits()
}

// GetStackKitInfo returns metadata for a specific StackKit.
func (e *Engine) GetStackKitInfo(name string) (*StackKitInfo, error) {
	if e.kitLoader == nil {
		return nil, fmt.Errorf("StackKit loader not initialized")
	}
	return e.kitLoader.GetKitInfo(name)
}

// ValidateWithStackKit validates a spec against both base schema and StackKit.
func (e *Engine) ValidateWithStackKit(spec *core.KombinationSpec) (*core.ValidationResult, error) {
	// First do base validation
	result, err := e.Validate(spec)
	if err != nil {
		return nil, err
	}
	if !result.Valid {
		return result, nil
	}

	// Then validate against StackKit if available
	if e.kitLoader != nil && spec.Kit != "" {
		specValue := e.ctx.Encode(spec)
		if specValue.Err() != nil {
			return nil, fmt.Errorf("failed to encode spec: %w", specValue.Err())
		}

		if kitErr := e.kitLoader.ValidateAgainstKit(spec.Kit, specValue); kitErr != nil {
			return &core.ValidationResult{
				Valid: false,
				Errors: []core.ValidationError{
					{
						Path:    "kit",
						Code:    "stackkit_validation_error",
						Message: kitErr.Error(),
					},
				},
			}, nil
		}
	}

	return result, nil
}

// GetStackKitLoader returns the StackKit loader (for advanced use).
func (e *Engine) GetStackKitLoader() *StackKitLoader {
	return e.kitLoader
}
