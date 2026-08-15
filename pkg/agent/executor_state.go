// Package agent — small executor-state helpers retained after the Phase 7.2
// compose-deployment cleanup. GetRunningProjects / IsProjectRunning have been
// removed because the runningProjects map they tracked is gone with the
// deployCompose / stopCompose handlers; WorkDir / ProjectDir survive as pure
// path-helpers used by diagnostic handlers and tests.
package agent

import "path/filepath"

// WorkDir returns the executor's working directory.
func (e *CommandExecutor) WorkDir() string {
	return e.workDir
}

// ProjectDir returns the directory for a specific project.
func (e *CommandExecutor) ProjectDir(projectName string) string {
	return filepath.Join(e.workDir, projectName)
}
