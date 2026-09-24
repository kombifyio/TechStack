package unifier

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kombifyio/techstack/pkg/core"
)

// Status reports only whether a local stack directory exists.
func (e *Engine) Status(stackID string) (*core.StackStatus, error) {
	if stackID == "" {
		return nil, fmt.Errorf("stack ID is required")
	}
	stackDir := e.getStackDir(stackID)
	if _, err := os.Stat(stackDir); os.IsNotExist(err) {
		return &core.StackStatus{
			ID:     stackID,
			State:  "not_found",
			Errors: []string{fmt.Sprintf("stack %s does not exist", stackID)},
		}, nil
	}
	return &core.StackStatus{ID: stackID, State: "present"}, nil
}

func (e *Engine) getStackDir(stackID string) string {
	return filepath.Join(e.dataDir, stackID)
}

// SetDataDir sets the base directory for stack data.
func (e *Engine) SetDataDir(dir string) {
	e.dataDir = dir
}
