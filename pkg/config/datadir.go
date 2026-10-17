package config

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	// stateDirName is the neutral directory name for local runtime state
	// (agent CA, agent gRPC TLS material, embedded TSDB, device state).
	stateDirName = "state"
	// legacyStateDirName is the name earlier installs used. Existing
	// directories under it keep being used in place; it is never created.
	legacyStateDirName = "pb_data" // pocketbase-migration-compat
)

// ResolveStateDir returns the runtime state directory below base. An existing
// legacy directory wins so installs keep their data; otherwise the neutral
// directory is returned. Nothing is created, moved or deleted.
func ResolveStateDir(base string) string {
	legacy := filepath.Join(base, legacyStateDirName)
	if info, err := os.Stat(legacy); err == nil && info.IsDir() { //nolint:gosec // base is the operator-configured data root, not request input
		return legacy
	}
	return filepath.Join(base, stateDirName)
}

// StateDirFromEnv resolves the runtime state directory from the environment.
// TECHSTACK_STATE_DIR names the directory exactly; the deprecated alias
// TECHSTACK_PB_DATA_DIR does the same. Otherwise TECHSTACK_DATA_DIR is the base
// ResolveStateDir works on, and without it the base is the working directory.
func StateDirFromEnv() string {
	for _, name := range []string{"TECHSTACK_STATE_DIR", "TECHSTACK_PB_DATA_DIR"} {
		if dir := strings.TrimSpace(os.Getenv(name)); dir != "" {
			return dir
		}
	}
	return ResolveStateDir(strings.TrimSpace(os.Getenv("TECHSTACK_DATA_DIR")))
}
