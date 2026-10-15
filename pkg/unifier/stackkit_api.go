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
		return nil
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

// ListStackKitInfo returns the complete metadata catalog or its authority
// error. Callers must never replace a broken catalog with fabricated rows.
func (e *Engine) ListStackKitInfo() ([]*StackKitInfo, error) {
	if e.kitLoader == nil {
		return nil, fmt.Errorf("StackKit loader not initialized")
	}
	return e.kitLoader.ListKitInfo()
}

// ValidateWithStackKit retains the input-validation interface. Canonical
// StackSpecs are separately authored and validated by the pinned CLI.
func (e *Engine) ValidateWithStackKit(spec *core.KombinationSpec) (*core.ValidationResult, error) {
	return e.Validate(spec)
}

// GetStackKitLoader returns the released metadata reader.
func (e *Engine) GetStackKitLoader() *StackKitLoader {
	return e.kitLoader
}
