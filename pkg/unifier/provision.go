// Package unifier provides the Unifier Engine implementation.
// This file contains infrastructure provisioning methods.
package unifier

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/tofu"
)

// Provision creates or updates infrastructure based on a unified spec.
// This delegates to the OpenTofu runner.
func (e *Engine) Provision(unified *core.UnifiedSpec) error {
	if unified == nil {
		return fmt.Errorf("unified spec is nil")
	}

	stackID := unified.Name
	if stackID == "" {
		return fmt.Errorf("stack name is required")
	}

	// Get or create stack directory
	stackDir := e.getStackDir(stackID)
	if err := os.MkdirAll(stackDir, 0755); err != nil {
		return fmt.Errorf("failed to create stack directory: %w", err)
	}

	e.logger.Info("provisioning stack",
		"stack_id", stackID,
		"dir", stackDir,
		"nodes", len(unified.Nodes),
		"services", len(unified.Services))

	// Initialize OpenTofu
	if err := e.runner.Init(stackDir); err != nil {
		return fmt.Errorf("tofu init failed: %w", err)
	}

	// Apply the configuration
	ctx := context.Background()
	result, err := e.runner.ApplyWithContext(ctx, stackDir)
	if err != nil {
		return fmt.Errorf("tofu apply failed: %w", err)
	}

	e.logger.Info("provision complete",
		"stack_id", stackID,
		"success", result.Success,
		"duration", result.Duration)

	return nil
}

// Destroy tears down infrastructure for a stack.
func (e *Engine) Destroy(stackID string) error {
	if stackID == "" {
		return fmt.Errorf("stack ID is required")
	}

	stackDir := e.getStackDir(stackID)

	// Check if stack directory exists
	if _, err := os.Stat(stackDir); os.IsNotExist(err) {
		return fmt.Errorf("stack not found: %s", stackID)
	}

	e.logger.Info("destroying stack",
		"stack_id", stackID,
		"dir", stackDir)

	// Destroy the infrastructure
	ctx := context.Background()
	if err := e.runner.DestroyWithContext(ctx, stackDir); err != nil {
		return fmt.Errorf("tofu destroy failed: %w", err)
	}

	e.logger.Info("destroy complete", "stack_id", stackID)

	return nil
}

// Status returns the current status of a stack.
func (e *Engine) Status(stackID string) (*core.StackStatus, error) {
	if stackID == "" {
		return nil, fmt.Errorf("stack ID is required")
	}

	stackDir := e.getStackDir(stackID)

	// Check if stack directory exists
	if _, err := os.Stat(stackDir); os.IsNotExist(err) {
		return &core.StackStatus{
			ID:     stackID,
			State:  "not_found",
			Errors: []string{fmt.Sprintf("stack %s does not exist", stackID)},
		}, nil
	}

	// Get state summary from OpenTofu
	state, err := e.runner.State(stackDir)
	if err != nil {
		return &core.StackStatus{
			ID:     stackID,
			State:  "error",
			Errors: []string{fmt.Sprintf("failed to read state: %v", err)},
		}, nil
	}

	// Determine state based on resources
	status := &core.StackStatus{
		ID:          stackID,
		State:       "running",
		LastUpdated: state.LastModified.Format("2006-01-02T15:04:05Z07:00"),
	}

	if state.Resources == 0 {
		status.State = "empty"
		status.Errors = []string{"no resources provisioned"}
	}

	return status, nil
}

// getStackDir returns the directory path for a stack.
func (e *Engine) getStackDir(stackID string) string {
	return filepath.Join(e.dataDir, stackID)
}

// SetRunner allows injecting a custom OpenTofu runner (useful for testing).
func (e *Engine) SetRunner(runner tofu.ExtendedRunner) {
	e.runner = runner
}

// SetDataDir sets the base directory for stack data.
func (e *Engine) SetDataDir(dir string) {
	e.dataDir = dir
}
