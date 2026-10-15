// Package unifier consumes published StackKits display metadata. StackSpec
// authoring and validation belong to the pinned StackKits CLI.
package unifier

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kombifyio/techstack/pkg/validator"
)

// StackKitLoader reads release metadata without loading StackKits CUE sources.
type StackKitLoader struct {
	metadataDir   string
	metadataError error
}

type StackKitInfo struct {
	Name        string                      `json:"name"`
	DisplayName string                      `json:"displayName"`
	Version     string                      `json:"version"`
	Description string                      `json:"description"`
	Author      string                      `json:"author,omitempty"`
	License     string                      `json:"license,omitempty"`
	Maturity    string                      `json:"maturity,omitempty"`
	Status      string                      `json:"status,omitempty"`
	Tags        []string                    `json:"tags"`
	Deprecated  bool                        `json:"deprecated"`
	Features    []string                    `json:"features,omitempty"`
	SupportedOS []string                    `json:"supportedOS,omitempty"`
	Services    *StackKitServicesInfo       `json:"services,omitempty"`
	Modes       map[string]StackKitModeInfo `json:"modes,omitempty"`
}

type StackKitServicesInfo struct {
	Required    []string `json:"required,omitempty"`
	Recommended []string `json:"recommended,omitempty"`
	Available   []string `json:"available,omitempty"`
}

type StackKitModeInfo struct {
	Description    string   `json:"description,omitempty"`
	TemplateDir    string   `json:"templateDir,omitempty"`
	Engine         string   `json:"engine,omitempty"`
	Requires       []string `json:"requires,omitempty"`
	RecommendedFor []string `json:"recommendedFor,omitempty"`
}

// NewStackKitLoader uses the pinned metadata by default. An explicit metadata
// authority retains its error for catalog and input-validation consumers.
func NewStackKitLoader(stackKitsDir ...string) (*StackKitLoader, error) {
	directory, err := stackKitMetadataAuthority(stackKitsDir...)
	return &StackKitLoader{metadataDir: directory, metadataError: err}, nil
}

func NewStackKitLoaderWithDir(directory string) (*StackKitLoader, error) {
	root, err := secureExistingDir(directory)
	if err != nil {
		return nil, err
	}
	return &StackKitLoader{metadataDir: root}, nil
}

func (l *StackKitLoader) ListAvailableKits() []string {
	kits, err := l.ListKitInfo()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(kits))
	for _, kit := range kits {
		names = append(names, kit.Name)
	}
	return names
}

func validateStackKitName(name string) error {
	return validator.ValidateStackKitName(name)
}

func secureExistingDir(directory string) (string, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return "", fmt.Errorf("directory path is required")
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", fmt.Errorf("resolve directory %q: %w", directory, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat directory %q: %w", resolved, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", resolved)
	}
	return filepath.Clean(resolved), nil
}
