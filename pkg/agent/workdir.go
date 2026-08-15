package agent

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var windowsDrivePathPattern = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

func resolveAgentWorkDir(basePath, commandID, requested string) (string, error) {
	base := strings.TrimSpace(basePath)
	if base == "" {
		return "", fmt.Errorf("base path is required")
	}

	workDir := strings.TrimSpace(requested)
	if workDir == "" {
		workDir = strings.TrimSpace(commandID)
		if workDir == "" {
			return "", fmt.Errorf("command id is required when working directory is empty")
		}
	}

	if windowsDrivePathPattern.MatchString(workDir) && !filepath.IsAbs(workDir) {
		return "", fmt.Errorf("windows absolute paths are not valid on this platform: %q", workDir)
	}

	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("cannot resolve base path %q: %w", basePath, err)
	}

	candidate := filepath.Clean(workDir)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(baseAbs, candidate)
	}

	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("cannot resolve working directory %q: %w", requested, err)
	}

	rel, err := filepath.Rel(baseAbs, candidateAbs)
	if err != nil {
		return "", fmt.Errorf("working directory %q is not within base path %q", requested, basePath)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("working directory %q escapes base path %q", requested, basePath)
	}

	return candidateAbs, nil
}
