// Package unifier provides the Unifier Pipeline orchestration.
package unifier

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/kombifyio/techstack/pkg/core"
)

var safeStackIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

const (
	// RequirementsSpecFilename is the default filename for persisted requirements.
	RequirementsSpecFilename = "requirements-spec.yaml"

	// UnifiedSpecFilename is the default filename for persisted unified specs.
	UnifiedSpecFilename = "unified-spec.yaml"

	// IntentSpecFilename is the default filename for the original intent.
	IntentSpecFilename = "kombination.yaml"

	// StackSpecFilename is the canonical StackKits CLI spec handoff.
	StackSpecFilename = "stack-spec.yaml"

	unifierAPIVersion = "techstack/v1"
)

// SpecPersister handles saving and loading of spec files to disk.
// It maintains a directory structure for each stack:
//
//	~/.techstack/stacks/<stack-id>/
//	├── kombination.yaml          # Original intent (copy, immutable)
//	├── requirements-spec.yaml    # Phase 2 output
//	├── unified-spec.yaml         # Phase 4 output
//	└── tofu/                     # IaC files
type SpecPersister struct {
	BaseDir string // Stack directory path
}

// NewSpecPersister creates a persister for a specific stack.
// It creates the directory structure if it doesn't exist.
func NewSpecPersister(stackID string) (*SpecPersister, error) {
	if err := validateSpecPersisterStackID(stackID); err != nil {
		return nil, err
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home directory: %w", err)
	}

	baseDir := filepath.Join(homeDir, ".techstack", "stacks", stackID)
	if err := os.MkdirAll(baseDir, 0750); err != nil {
		return nil, fmt.Errorf("create stack directory: %w", err)
	}

	// Create tofu subdirectory
	tofuDir := filepath.Join(baseDir, "tofu")
	if err := os.MkdirAll(tofuDir, 0750); err != nil {
		return nil, fmt.Errorf("create tofu directory: %w", err)
	}

	return &SpecPersister{BaseDir: baseDir}, nil
}

// NewSpecPersisterWithPath creates a persister with a custom base path.
// Useful for testing or non-standard deployments.
func NewSpecPersisterWithPath(baseDir string) (*SpecPersister, error) {
	if baseDir == "" {
		return nil, fmt.Errorf("base directory is required")
	}
	if info, err := os.Lstat(baseDir); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("base directory must not be a symlink")
	}

	if err := os.MkdirAll(baseDir, 0750); err != nil {
		return nil, fmt.Errorf("create base directory: %w", err)
	}

	return &SpecPersister{BaseDir: baseDir}, nil
}

// SaveIntentBytes persists an immutable copy of the user's kombination.yaml.
//
// IMPORTANT:
// - This writes the bytes EXACTLY as provided.
// - No marshaling, no normalization, no header injection.
// - This is what we later return on export to guarantee byte-for-byte preservation.
func (p *SpecPersister) SaveIntentBytes(data []byte) (string, string, error) {
	if len(data) == 0 {
		return "", "", fmt.Errorf("intent data is empty")
	}

	path, err := p.writeManagedFile(IntentSpecFilename, data)
	if err != nil {
		return "", "", fmt.Errorf("write intent file: %w", err)
	}

	return path, ComputeDataHash(data), nil
}

// SaveStackSpecBytes persists the canonical StackKits CLI spec handoff.
func (p *SpecPersister) SaveStackSpecBytes(data []byte) (string, string, error) {
	if len(data) == 0 {
		return "", "", fmt.Errorf("stack spec data is empty")
	}

	path, err := p.writeManagedFile(StackSpecFilename, data)
	if err != nil {
		return "", "", fmt.Errorf("write stack spec file: %w", err)
	}

	return path, ComputeDataHash(data), nil
}

// SaveIntentFile copies an existing kombination.yaml into the stack directory
// without changing its bytes.
func (p *SpecPersister) SaveIntentFile(sourcePath string) (string, string, error) {
	if sourcePath == "" {
		return "", "", fmt.Errorf("source path is required")
	}

	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", "", fmt.Errorf("read intent file: %w", err)
	}

	return p.SaveIntentBytes(data)
}

// LoadIntentBytes loads the persisted kombination.yaml bytes.
func (p *SpecPersister) LoadIntentBytes() ([]byte, error) {
	path := filepath.Join(p.BaseDir, IntentSpecFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read intent file: %w", err)
	}
	return data, nil
}

// IntentExists checks if a persisted kombination.yaml exists for this stack.
func (p *SpecPersister) IntentExists() bool {
	path := filepath.Join(p.BaseDir, IntentSpecFilename)
	_, err := os.Stat(path)
	return err == nil
}

// StackSpecExists checks if a persisted StackKits stack-spec.yaml exists.
func (p *SpecPersister) StackSpecExists() bool {
	path := filepath.Join(p.BaseDir, StackSpecFilename)
	_, err := os.Stat(path)
	return err == nil
}

// SaveRequirementsSpec persists the RequirementsSpec to YAML.
// It automatically sets the metadata if not already present.
func (p *SpecPersister) SaveRequirementsSpec(spec *core.RequirementsSpec, intentPath string) (string, error) {
	if spec == nil {
		return "", fmt.Errorf("requirements spec is nil")
	}

	// Set metadata if not present
	if spec.Metadata.GeneratedAt.IsZero() {
		spec.Metadata.GeneratedAt = time.Now().UTC()
	}
	if spec.Metadata.SourceFile == "" && intentPath != "" {
		spec.Metadata.SourceFile = intentPath
	}
	if spec.Metadata.SourceHash == "" && intentPath != "" {
		hash, err := ComputeFileHash(intentPath)
		if err != nil {
			return "", fmt.Errorf("compute intent hash: %w", err)
		}
		spec.Metadata.SourceHash = hash
	}

	// Set API version and kind
	if spec.APIVersion == "" {
		spec.APIVersion = unifierAPIVersion
	}
	if spec.Kind == "" {
		spec.Kind = "RequirementsSpec"
	}

	path, err := p.saveYAML(spec, RequirementsSpecFilename)
	if err != nil {
		return "", fmt.Errorf("save requirements spec: %w", err)
	}

	return path, nil
}

// SaveUnifiedSpec persists the UnifiedSpec to YAML.
// It automatically computes the hash chain from the requirements spec.
func (p *SpecPersister) SaveUnifiedSpec(spec *core.UnifiedSpec, requirementsPath string) (string, error) {
	if spec == nil {
		return "", fmt.Errorf("unified spec is nil")
	}

	// Set metadata if not present
	if spec.PipelineInfo.GeneratedAt.IsZero() {
		spec.PipelineInfo.GeneratedAt = time.Now().UTC()
	}
	if spec.PipelineInfo.SourceRequirementsFile == "" && requirementsPath != "" {
		spec.PipelineInfo.SourceRequirementsFile = requirementsPath
	}
	if spec.PipelineInfo.SourceRequirementsHash == "" && requirementsPath != "" {
		hash, err := ComputeFileHash(requirementsPath)
		if err != nil {
			return "", fmt.Errorf("compute requirements hash: %w", err)
		}
		spec.PipelineInfo.SourceRequirementsHash = hash
	}

	// Set API version and kind
	if spec.APIVersion == "" {
		spec.APIVersion = unifierAPIVersion
	}
	if spec.Kind == "" {
		spec.Kind = "UnifiedSpec"
	}

	path, err := p.saveYAML(spec, UnifiedSpecFilename)
	if err != nil {
		return "", fmt.Errorf("save unified spec: %w", err)
	}

	return path, nil
}

// LoadRequirementsSpec loads a persisted RequirementsSpec from disk.
func (p *SpecPersister) LoadRequirementsSpec() (*core.RequirementsSpec, error) {
	path := filepath.Join(p.BaseDir, RequirementsSpecFilename)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read requirements spec: %w", err)
	}

	var spec core.RequirementsSpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parse requirements spec: %w", err)
	}

	return &spec, nil
}

// LoadUnifiedSpec loads a persisted UnifiedSpec from disk.
func (p *SpecPersister) LoadUnifiedSpec() (*core.UnifiedSpec, error) {
	path := filepath.Join(p.BaseDir, UnifiedSpecFilename)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read unified spec: %w", err)
	}

	var spec core.UnifiedSpec
	if err := yaml.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parse unified spec: %w", err)
	}

	return &spec, nil
}

// RequirementsSpecExists checks if a requirements spec file exists.
func (p *SpecPersister) RequirementsSpecExists() bool {
	path := filepath.Join(p.BaseDir, RequirementsSpecFilename)
	_, err := os.Stat(path)
	return err == nil
}

// UnifiedSpecExists checks if a unified spec file exists.
func (p *SpecPersister) UnifiedSpecExists() bool {
	path := filepath.Join(p.BaseDir, UnifiedSpecFilename)
	_, err := os.Stat(path)
	return err == nil
}

// GetIntentPath returns the full path to the persisted kombination.yaml copy.
func (p *SpecPersister) GetIntentPath() string {
	return filepath.Join(p.BaseDir, IntentSpecFilename)
}

// GetStackSpecPath returns the full path to the persisted StackKits stack-spec.yaml handoff.
func (p *SpecPersister) GetStackSpecPath() string {
	return filepath.Join(p.BaseDir, StackSpecFilename)
}

// GetRequirementsSpecPath returns the full path to the requirements spec file.
func (p *SpecPersister) GetRequirementsSpecPath() string {
	return filepath.Join(p.BaseDir, RequirementsSpecFilename)
}

// GetUnifiedSpecPath returns the full path to the unified spec file.
func (p *SpecPersister) GetUnifiedSpecPath() string {
	return filepath.Join(p.BaseDir, UnifiedSpecFilename)
}

// GetTofuDir returns the path to the tofu directory.
func (p *SpecPersister) GetTofuDir() string {
	return filepath.Join(p.BaseDir, "tofu")
}

// saveYAML marshals data to YAML and writes it to a managed file.
func (p *SpecPersister) saveYAML(data interface{}, filename string) (string, error) {
	yamlData, err := yaml.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("marshal YAML: %w", err)
	}

	// Add header comment
	header := []byte("# Generated by kombifyTechstack Unifier\n# DO NOT EDIT - This file is managed automatically\n\n")
	yamlData = append(header, yamlData...)

	return p.writeManagedFile(filename, yamlData)
}

func (p *SpecPersister) writeManagedFile(filename string, data []byte) (string, error) {
	path, err := p.managedPath(filename)
	if err != nil {
		return "", err
	}
	// #nosec G703 -- path is restricted by managedPath to fixed spec filenames under BaseDir.
	if err := os.WriteFile(path, data, 0600); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}

	return path, nil
}

func (p *SpecPersister) managedPath(filename string) (string, error) {
	switch filename {
	case IntentSpecFilename, StackSpecFilename, RequirementsSpecFilename, UnifiedSpecFilename:
	default:
		return "", fmt.Errorf("unsupported managed spec filename %q", filename)
	}
	if p.BaseDir == "" {
		return "", fmt.Errorf("base directory is required")
	}
	baseDir, err := filepath.Abs(p.BaseDir)
	if err != nil {
		return "", fmt.Errorf("resolve base directory: %w", err)
	}
	path := filepath.Join(baseDir, filename)
	rel, err := filepath.Rel(baseDir, path)
	if err != nil {
		return "", fmt.Errorf("resolve managed spec path: %w", err)
	}
	if rel != filename || filepath.IsAbs(rel) {
		return "", fmt.Errorf("managed spec path %q escapes base directory", filename)
	}
	return path, nil
}

func validateSpecPersisterStackID(stackID string) error {
	if stackID == "" {
		return fmt.Errorf("stack ID is required")
	}
	if filepath.IsAbs(stackID) || filepath.Clean(stackID) != stackID || !safeStackIDPattern.MatchString(stackID) {
		return fmt.Errorf("stack ID %q is not a safe canonical identifier", stackID)
	}
	return nil
}

// ComputeFileHash calculates the SHA256 hash of a file.
func ComputeFileHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read file for hash: %w", err)
	}

	return ComputeDataHash(data), nil
}

// ComputeDataHash calculates the SHA256 hash of byte data.
func ComputeDataHash(data []byte) string {
	hash := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(hash[:])
}

// VerifyHashChain validates that the unified spec's hash chain is intact.
// Returns true if the requirements spec hash matches.
func (p *SpecPersister) VerifyHashChain() (bool, error) {
	unified, err := p.LoadUnifiedSpec()
	if err != nil {
		return false, fmt.Errorf("load unified spec: %w", err)
	}

	if unified.PipelineInfo.SourceRequirementsHash == "" {
		return false, fmt.Errorf("unified spec has no requirements hash")
	}

	reqPath := p.GetRequirementsSpecPath()
	currentHash, err := ComputeFileHash(reqPath)
	if err != nil {
		return false, fmt.Errorf("compute requirements hash: %w", err)
	}

	return unified.PipelineInfo.SourceRequirementsHash == currentHash, nil
}
