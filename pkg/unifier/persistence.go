// Package unifier provides the Unifier Pipeline orchestration.
package unifier

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/kombifyio/techstack/pkg/core"
)

var safeStackIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var stackSpecSnapshotPattern = regexp.MustCompile(`^stack-spec\.snapshot-[a-f0-9]{64}\.v2\.json$`)

const (
	// RequirementsSpecFilename is the default filename for persisted requirements.
	RequirementsSpecFilename = "requirements-spec.yaml"

	// UnifiedSpecFilename is the default filename for persisted unified specs.
	UnifiedSpecFilename = "unified-spec.yaml"

	// IntentSpecFilename is the default filename for the original intent.
	IntentSpecFilename = "kombination.yaml"

	// StackSpecFilename is the canonical StackKits CLI spec handoff.
	StackSpecFilename = "stack-spec.yaml"

	// BoundStackSpecFilename is the pinned renderer's canonical node handoff.
	BoundStackSpecFilename = "stack-spec.address-bound.v2.json"

	unifierAPIVersion = "techstack/v1"
)

// SpecPersister handles saving and loading of spec files to disk.
// It maintains a directory structure for each stack:
//
//	~/.techstack/stacks/<stack-id>/
//	├── kombination.yaml          # Original intent (copy, immutable)
//	├── requirements-spec.yaml    # Phase 2 output
//	├── unified-spec.yaml         # Phase 4 output
//	└── stack-spec.yaml           # StackKits CLI handoff
type SpecPersister struct {
	BaseDir string // Stack directory path

	// mirror keeps the durable copy of the managed files. The local directory
	// is a working copy: an instance without a persistent disk restores it
	// from the mirror, and a managed write is reported as persisted only after
	// it reached the mirror.
	mirror SpecFileMirror
}

// ErrSpecMirrorPending reports that the durable custody does not exist yet
// (the binary runs before its schema). The local directory stays the only copy.
var ErrSpecMirrorPending = errors.New("durable spec file custody is not rolled out yet")

// SpecFileMirror is the durable custody of one stack's managed spec files,
// keyed by the managed filename.
type SpecFileMirror interface {
	PutSpecFile(filename string, data []byte) error
	LoadSpecFiles() (map[string][]byte, error)
}

// AttachMirror binds the durable custody and reconciles the working copy
// with it. A managed file the mirror holds and the directory lacks (a fresh
// instance) is restored. A managed file the directory holds is the newer copy
// whenever it differs from the mirror or the mirror lacks it: it was written on
// this instance (some writers, like the renderer's address binding, write the
// working copy directly) or on a disk that predates the mirror, so it is handed
// to the mirror.
func (p *SpecPersister) AttachMirror(mirror SpecFileMirror) error {
	if p == nil || mirror == nil {
		return nil
	}
	files, err := mirror.LoadSpecFiles()
	if err != nil {
		return fmt.Errorf("load durable spec files: %w", err)
	}
	if entries, readErr := os.ReadDir(p.BaseDir); readErr == nil {
		for _, entry := range entries {
			name := entry.Name()
			if !entry.Type().IsRegular() || !IsManagedSpecFilename(name) {
				continue
			}
			data, fileErr := os.ReadFile(filepath.Join(p.BaseDir, name)) // #nosec G304 -- managed filename under BaseDir.
			if fileErr != nil {
				return fmt.Errorf("read local spec file %s: %w", name, fileErr)
			}
			if held, ok := files[name]; ok && bytes.Equal(held, data) {
				continue
			}
			if putErr := mirror.PutSpecFile(name, data); putErr != nil {
				return fmt.Errorf("adopt local spec file %s: %w", name, putErr)
			}
			files[name] = data
		}
	}
	for filename, data := range files {
		path, pathErr := p.managedPath(filename)
		if pathErr != nil {
			return fmt.Errorf("restore durable spec file: %w", pathErr)
		}
		if _, statErr := os.Lstat(path); statErr == nil {
			continue
		}
		// #nosec G703 -- path is restricted by managedPath to fixed spec filenames under BaseDir.
		if writeErr := os.WriteFile(path, data, 0600); writeErr != nil {
			return fmt.Errorf("restore durable spec file %s: %w", filename, writeErr)
		}
	}
	p.mirror = mirror
	return nil
}

func (p *SpecPersister) mirrorManagedFile(filename string, data []byte) error {
	if p.mirror == nil {
		return nil
	}
	if err := p.mirror.PutSpecFile(filename, data); err != nil {
		return fmt.Errorf("persist durable spec file %s: %w", filename, err)
	}
	return nil
}

// IsManagedSpecFilename reports whether filename is one of the managed spec
// files a stack directory holds durably.
func IsManagedSpecFilename(filename string) bool {
	switch filename {
	case IntentSpecFilename, StackSpecFilename, BoundStackSpecFilename, RequirementsSpecFilename, UnifiedSpecFilename:
		return true
	}
	return stackSpecSnapshotPattern.MatchString(filename)
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

// SaveStackSpecSnapshotBytes keeps a dispatch's native document separate from
// the mutable preparation handoff. The content-derived name cannot replace a
// different job's document, and uses the same confined path authority.
func (p *SpecPersister) SaveStackSpecSnapshotBytes(data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("stack spec snapshot is empty")
	}
	sum := sha256.Sum256(data)
	return p.saveImmutableManagedBytes("stack-spec.snapshot-"+hex.EncodeToString(sum[:])+".v2.json", data)
}

// SaveBoundStackSpecSnapshotBytes preserves an already validated and registered
// zone document under the renderer's fixed node basename without rebinding it.
// Existing differing bytes are never overwritten, including dispatch replay.
func (p *SpecPersister) SaveBoundStackSpecSnapshotBytes(data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("bound stack spec snapshot is empty")
	}
	return p.saveImmutableManagedBytes(BoundStackSpecFilename, data)
}

func (p *SpecPersister) saveImmutableManagedBytes(filename string, data []byte) (string, error) {
	path, err := p.managedPath(filename)
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(existing, data) {
			return "", fmt.Errorf("existing native snapshot does not match its immutable document")
		}
		if mirrorErr := p.mirrorManagedFile(filename, data); mirrorErr != nil {
			return "", mirrorErr
		}
		return path, nil
	}
	if err != nil {
		return "", fmt.Errorf("create native snapshot: %w", err)
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		return "", fmt.Errorf("persist native snapshot: %w", errors.Join(writeErr, closeErr))
	}
	if mirrorErr := p.mirrorManagedFile(filename, data); mirrorErr != nil {
		return "", mirrorErr
	}
	return path, nil
}

// SaveStackSpecSnapshotForDispatch isolates a retry's derived preparation
// artifacts from the stack's earlier bound intent, plan and bundle. The
// immutable snapshot keeps the same managed filename contract; the pinned
// renderer owns every output beneath this dispatch's workspace.
func (p *SpecPersister) SaveStackSpecSnapshotForDispatch(data []byte, dispatchID string) (string, error) {
	if strings.TrimSpace(dispatchID) == "" {
		return "", fmt.Errorf("snapshot preparation requires an exact dispatch identity")
	}
	anchor, err := p.managedPath(StackSpecFilename)
	if err != nil {
		return "", err
	}
	base := filepath.Dir(anchor)
	resolvedBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", fmt.Errorf("resolve snapshot preparation root: %w", err)
	}
	sum := sha256.Sum256([]byte(dispatchID))
	namespace := "preparation-" + hex.EncodeToString(sum[:])
	child := filepath.Join(base, namespace)
	rel, err := filepath.Rel(base, child)
	if err != nil || rel != namespace || filepath.IsAbs(rel) {
		return "", fmt.Errorf("snapshot preparation escapes its managed root")
	}
	preparation, err := NewSpecPersisterWithPath(child)
	if err != nil {
		return "", err
	}
	resolvedChild, err := filepath.EvalSymlinks(child)
	if err != nil || resolvedChild != filepath.Join(resolvedBase, namespace) {
		return "", fmt.Errorf("snapshot preparation does not resolve inside its managed root")
	}
	return preparation.SaveStackSpecSnapshotBytes(data)
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

// GetStackSpecPath returns the full path to the persisted StackKits stack-spec.yaml handoff.
func (p *SpecPersister) GetStackSpecPath() string {
	return filepath.Join(p.BaseDir, StackSpecFilename)
}

// GetRequirementsSpecPath returns the full path to the requirements spec file.
func (p *SpecPersister) GetRequirementsSpecPath() string {
	return filepath.Join(p.BaseDir, RequirementsSpecFilename)
}

// GetTofuDir returns the historical workspace path still used as a deploy
// work-dir name. Callers must create it if they write there; persist no
// longer materializes an OpenTofu tree.
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
	if err := p.mirrorManagedFile(filename, data); err != nil {
		return "", err
	}
	return path, nil
}

func (p *SpecPersister) managedPath(filename string) (string, error) {
	if !IsManagedSpecFilename(filename) {
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
