// Package unifier provides the StackKit loader for loading CUE schemas from the StackKits checkout.
package unifier

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	cueerrors "cuelang.org/go/cue/errors"
	"cuelang.org/go/cue/parser"

	stackkitcatalog "github.com/kombifyio/techstack/pkg/stackkits"
	"github.com/kombifyio/techstack/pkg/validator"
)

// validateStackKitName validates a stackkit name using the shared validator.
func validateStackKitName(name string) error {
	return validator.ValidateStackKitName(name)
}

// StackKitLoader handles loading and caching of StackKit CUE schemas.
type StackKitLoader struct {
	ctx        *cue.Context
	baseKit    cue.Value
	baseSource string // Raw CUE source for base kit (for concatenation with specific kits)
	kits       map[string]cue.Value
	kitsDir    string // External stackkits directory (optional)
	mu         sync.RWMutex
	baseLoaded bool
	lastError  error // Last error for debugging
}

// StackKitInfo contains metadata about a loaded StackKit.
type StackKitInfo struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"displayName"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	Author      string   `json:"author,omitempty"`
	License     string   `json:"license,omitempty"`
	Tags        []string `json:"tags"`
	Deprecated  bool     `json:"deprecated"`
	Features    []string `json:"features,omitempty"`
	SupportedOS []string `json:"supportedOS,omitempty"`
	Services    struct {
		Required    []string `json:"required,omitempty"`
		Recommended []string `json:"recommended,omitempty"`
		Available   []string `json:"available,omitempty"`
	} `json:"services,omitempty"`
	Modes map[string]StackKitModeInfo `json:"modes,omitempty"`
}

type StackKitModeInfo struct {
	Description    string   `json:"description,omitempty"`
	TemplateDir    string   `json:"templateDir,omitempty"`
	Engine         string   `json:"engine,omitempty"`
	Requires       []string `json:"requires,omitempty"`
	RecommendedFor []string `json:"recommendedFor,omitempty"`
}

// NewStackKitLoader creates a new StackKitLoader.
// It attempts to find the stackkits directory relative to the working directory.
// An optional stackKitsDir parameter can be passed from config; if empty, the
// TECHSTACK_STACKKITS_DIR environment variable is checked as a fallback.
func NewStackKitLoader(stackKitsDir ...string) (*StackKitLoader, error) {
	ctx := cuecontext.New()

	loader := &StackKitLoader{
		ctx:  ctx,
		kits: make(map[string]cue.Value),
	}

	// Highest priority: explicit directory from config parameter.
	if len(stackKitsDir) > 0 && strings.TrimSpace(stackKitsDir[0]) != "" {
		if dir, err := secureExistingDir(stackKitsDir[0]); err == nil {
			loader.kitsDir = dir
		} else {
			loader.lastError = err
		}
	}

	// Second priority: external StackKits checkout resolution.
	if loader.kitsDir == "" {
		if dir, err := secureExistingDir(DefaultStackKitsDir()); err == nil {
			loader.kitsDir = dir
		}
	}

	// Last resort: discover the legacy in-repo fallback based on source location.
	if loader.kitsDir == "" {
		if _, file, _, ok := runtime.Caller(0); ok {
			unifierDir := filepath.Dir(file)
			candidate := filepath.Clean(filepath.Join(unifierDir, "..", "stackkits"))
			if dir, err := secureExistingDir(candidate); err == nil {
				loader.kitsDir = dir
			}
		}
	}

	// Try to load base kit from known locations
	if err := loader.tryLoadBaseKit(); err != nil {
		// Base kit loading failed, but loader is still usable
		// with reduced functionality
		loader.baseLoaded = false
	} else {
		loader.baseLoaded = true
	}

	return loader, nil
}

func resolveKitDirName(name string) string {
	return CanonicalStackKitName(name)
}

func resolveKitDirNames(name string) []string {
	dirName := resolveKitDirName(name)
	if dirName == StackKitBasement {
		return []string{StackKitBasement, StackKitLegacyBaseSlug}
	}
	return []string{dirName}
}

// NewStackKitLoaderWithDir creates a loader with a specific stackkits directory.
func NewStackKitLoaderWithDir(externalDir string) (*StackKitLoader, error) {
	ctx := cuecontext.New()
	kitsDir, err := secureExistingDir(externalDir)
	if err != nil {
		return nil, err
	}

	loader := &StackKitLoader{
		ctx:     ctx,
		kits:    make(map[string]cue.Value),
		kitsDir: kitsDir,
	}

	// Try to load base kit from the provided directory.
	baseDir, err := existingDirUnderRoot(kitsDir, "base")
	if err != nil {
		loader.lastError = err
		return loader, nil
	}
	kit, err := loader.loadBaseKitFromDir(baseDir)
	if err == nil {
		loader.baseKit = kit
		loader.baseLoaded = true
	} else {
		loader.lastError = err
	}

	return loader, nil
}

// tryLoadBaseKit attempts to find and load the base stackkit from common locations.
func (l *StackKitLoader) tryLoadBaseKit() error {
	// Locations to check for base stackkit
	locations := []string{
		"pkg/stackkits/base",
		"../pkg/stackkits/base",
		"../../pkg/stackkits/base",
	}

	// Also check if kitsDir is set
	if l.kitsDir != "" {
		if baseDir, err := existingDirUnderRoot(l.kitsDir, "base"); err == nil {
			locations = append([]string{baseDir}, locations...)
		} else {
			l.lastError = err
		}
	}

	for _, loc := range locations {
		if kit, err := l.loadBaseKitFromDir(loc); err == nil {
			l.baseKit = kit
			return nil
		}
	}

	return fmt.Errorf("base stackkit not found in any known location")
}

// loadBaseKitFromDir loads the base CUE files from a directory.
// This is separate from loadKitFromDir because base files don't need
// to be unified with another base - they ARE the base.
// All files are concatenated and compiled together to resolve cross-references.
func (l *StackKitLoader) loadBaseKitFromDir(dir string) (cue.Value, error) {
	dir, err := secureExistingContentDir(dir)
	if err != nil {
		return cue.Value{}, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return cue.Value{}, fmt.Errorf("failed to read base directory %s: %w", dir, err)
	}

	// Collect all CUE content from all files
	var allContent strings.Builder

	// Track imports we need to preserve
	seenImports := make(map[string]bool)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".cue") {
			continue
		}

		content, err := readRegularFileUnderRoot(dir, entry.Name())
		if err != nil {
			return cue.Value{}, fmt.Errorf("failed to read base file %s: %w", entry.Name(), err)
		}

		// Extract imports and content separately
		imports, body, dropped := extractImportsAndBody(content)
		if unresolvable := unresolvableDroppedImports(dropped); len(unresolvable) > 0 {
			return cue.Value{}, fmt.Errorf(
				"base file %s imports %s, which this loader cannot inline; "+
					"add it to cueStdLibPackages or stop importing it",
				entry.Name(), strings.Join(unresolvable, ", "))
		}
		for _, imp := range imports {
			seenImports[imp] = true
		}
		allContent.WriteString(body)
		allContent.WriteString("\n")
	}

	if allContent.Len() == 0 {
		return cue.Value{}, fmt.Errorf("no .cue files found in %s", dir)
	}

	// Build final content with deduplicated imports at the top
	var finalContent strings.Builder
	writeImportBlock(&finalContent, seenImports)
	finalContent.WriteString(allContent.String())

	// Store the source for later concatenation with specific kits
	l.baseSource = finalContent.String()

	// The base is deliberately NOT evaluated here. Every kit load concatenates
	// this same source with the kit and compiles the pair, so evaluating it
	// standalone produces a second copy of the whole base schema graph that
	// nothing reads -- and a cue.Context retains everything compiled in it, so
	// that copy lives as long as the loader. Measured on the real checkout it
	// cost ~190MB and pushed the generate phase peak from 398MB to 591MB, which
	// is what put the rollout into the OOM killer on a 512Mi instance.
	//
	// Syntax is still checked eagerly, because a broken base should fail at load
	// time rather than inside the first rollout. Parsing is cheap and, unlike
	// compilation, keeps nothing in the context.
	if _, parseErr := parser.ParseFile("base/combined.cue", l.baseSource); parseErr != nil {
		return cue.Value{}, fmt.Errorf("failed to parse base schemas: %s", cueerrors.Details(parseErr, nil))
	}
	v := cue.Value{}

	return v, nil
}

func writeImportBlock(builder *strings.Builder, imports map[string]bool) {
	if len(imports) == 0 {
		return
	}

	specs := make([]string, 0, len(imports))
	for imp := range imports {
		specs = append(specs, imp)
	}
	sort.Strings(specs)

	builder.WriteString("import (\n")
	for _, imp := range specs {
		builder.WriteString("\t")
		builder.WriteString(imp)
		builder.WriteString("\n")
	}
	builder.WriteString(")\n\n")
}

// cueStdLibPackages is the complete CUE standard library. It must stay
// complete: an import that is not listed here is dropped from the combined
// buffer, and the kit then fails to compile with a bare "reference X not
// found" pointing at a synthetic line number, which says nothing about the
// missing import. That is exactly how a missing "struct" entry cost a live
// managed-runtime rollout on 2026-07-26 — StackKits' base/architecture_v2.cue
// imports "struct" and uses struct.MinFields(1), so every rollout failed with
// four unexplained reference errors.
//
// Source: https://pkg.go.dev/cuelang.org/go/pkg
var cueStdLibPackages = map[string]bool{
	"crypto/ed25519": true, "crypto/hmac": true, "crypto/md5": true,
	"crypto/sha1": true, "crypto/sha256": true, "crypto/sha512": true,
	"encoding/base64": true, "encoding/csv": true, "encoding/hex": true,
	"encoding/json": true, "encoding/yaml": true,
	"html": true, "list": true, "math": true, "math/bits": true,
	"net": true, "path": true, "regexp": true, "strconv": true,
	"strings": true, "struct": true,
	"text/tabwriter": true, "text/template": true,
	"time": true, "tool": true, "tool/cli": true, "tool/exec": true,
	"tool/file": true, "tool/http": true, "tool/os": true,
	"uuid": true,
}

// importSpecPattern captures the quoted path of a CUE import spec, with or
// without a local alias.
var importSpecPattern = regexp.MustCompile(`"([^"]+)"`)

// extractImportsAndBody separates import statements from the rest of the CUE
// content. It returns the standard-library import specs to re-emit, the body
// without package/import declarations, and every import path it deliberately
// dropped so the caller can refuse to compile a buffer whose references it
// silently removed.
func extractImportsAndBody(content []byte) (imports []string, body string, dropped []string) {
	lines := strings.Split(string(content), "\n")
	var bodyLines []string
	inImportBlock := false

	classify := func(spec string) {
		match := importSpecPattern.FindStringSubmatch(spec)
		if match == nil {
			return
		}
		if cueStdLibPackages[match[1]] {
			imports = append(imports, spec)
			return
		}
		dropped = append(dropped, match[1])
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Skip package declarations
		if strings.HasPrefix(trimmed, "package ") {
			continue
		}

		// Handle import blocks
		if strings.HasPrefix(trimmed, "import (") {
			inImportBlock = true
			continue
		}
		if inImportBlock {
			if trimmed == ")" {
				inImportBlock = false
				continue
			}
			if trimmed != "" && !strings.HasPrefix(trimmed, "//") {
				classify(trimmed)
			}
			continue
		}

		// Handle single-line imports
		if strings.HasPrefix(trimmed, "import ") {
			if spec := strings.TrimSpace(strings.TrimPrefix(trimmed, "import")); spec != "" {
				classify(spec)
			}
			continue
		}

		bodyLines = append(bodyLines, line)
	}

	return imports, strings.Join(bodyLines, "\n"), dropped
}

// inlinedKitModulePrefixes are the module imports a kit legitimately declares
// and this loader legitimately drops, because the referenced package is
// concatenated into the same buffer rather than resolved through a module.
// Three org spellings are in circulation across the in-repo fork and the
// pinned external checkout; all of them resolve to the same inlined base.
var inlinedKitModulePrefixes = []string{
	"github.com/kombifyio/stackkits/",
	"github.com/kombihq/stackkits/",
	"github.com/kombifyio/stackkits/",
	"base/",
}

// unresolvableDroppedImports returns the dropped imports that are neither CUE
// standard library nor an inlined kit module — the ones whose removal would
// leave dangling references in the combined buffer.
func unresolvableDroppedImports(dropped []string) []string {
	var unresolvable []string
	for _, path := range dropped {
		inlined := false
		for _, prefix := range inlinedKitModulePrefixes {
			if strings.HasPrefix(path, prefix) {
				inlined = true
				break
			}
		}
		if !inlined {
			unresolvable = append(unresolvable, path)
		}
	}
	return unresolvable
}

// LoadKit loads a specific StackKit by name.
// First checks cache, then known locations.
func (l *StackKitLoader) LoadKit(name string) (cue.Value, error) {
	if err := validateStackKitName(name); err != nil {
		return cue.Value{}, err
	}

	l.mu.RLock()
	if kit, ok := l.kits[name]; ok {
		l.mu.RUnlock()
		return kit, nil
	}
	l.mu.RUnlock()

	l.mu.Lock()
	defer l.mu.Unlock()

	// Double-check after acquiring write lock
	if kit, ok := l.kits[name]; ok {
		return kit, nil
	}

	// Try to load from filesystem
	kit, err := l.loadKitByName(name)
	if err == nil {
		l.kits[name] = kit
		return kit, nil
	}

	// Try external directory if configured
	if l.kitsDir != "" {
		kit, err = l.loadExternalKit(name)
		if err == nil {
			l.kits[name] = kit
			return kit, nil
		}
	}

	return cue.Value{}, fmt.Errorf("StackKit '%s' not found: %w", name, err)
}

// loadKitByName attempts to load a kit from known filesystem locations.
func (l *StackKitLoader) loadKitByName(name string) (cue.Value, error) {
	if err := validateStackKitName(name); err != nil {
		return cue.Value{}, err
	}

	// Locations to check
	locations := make([]string, 0, 6)
	for _, dirName := range resolveKitDirNames(name) {
		locations = append(locations,
			filepath.Join("pkg", "stackkits", dirName),
			filepath.Join("..", "pkg", "stackkits", dirName),
			filepath.Join("..", "..", "pkg", "stackkits", dirName),
		)
	}

	for _, loc := range locations {
		if kit, err := l.loadKitFromDir(loc); err == nil {
			return kit, nil
		}
	}

	return cue.Value{}, fmt.Errorf("kit directory not found for '%s'", name)
}

// loadExternalKit loads a kit from the external directory.
func (l *StackKitLoader) loadExternalKit(name string) (cue.Value, error) {
	if err := validateStackKitName(name); err != nil {
		return cue.Value{}, err
	}
	kitDir, err := l.stackKitDir(name)
	if err != nil {
		return cue.Value{}, err
	}
	return l.loadKitFromDir(kitDir)
}

// loadKitFromDir loads all .cue files from a directory and compiles them with base.
// Base source and kit files are concatenated and compiled together to resolve cross-references.
func (l *StackKitLoader) loadKitFromDir(dir string) (cue.Value, error) {
	dir, err := secureExistingContentDir(dir)
	if err != nil {
		return cue.Value{}, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return cue.Value{}, fmt.Errorf("failed to read kit directory %s: %w", dir, err)
	}

	// Collect all CUE content from all kit files
	var kitContent strings.Builder
	seenImports := make(map[string]bool)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".cue") {
			continue
		}

		content, err := readRegularFileUnderRoot(dir, entry.Name())
		if err != nil {
			return cue.Value{}, fmt.Errorf("failed to read kit file %s: %w", entry.Name(), err)
		}

		// Extract imports and content, replacing base.# references
		imports, body, dropped := extractImportsAndBody(content)
		if unresolvable := unresolvableDroppedImports(dropped); len(unresolvable) > 0 {
			return cue.Value{}, fmt.Errorf(
				"kit file %s imports %s, which this loader cannot inline; "+
					"add it to cueStdLibPackages or stop importing it",
				entry.Name(), strings.Join(unresolvable, ", "))
		}
		for _, imp := range imports {
			seenImports[imp] = true
		}
		// Replace base.#Type with #Type since we're combining with base source
		body = strings.ReplaceAll(body, "base.#", "#")
		kitContent.WriteString(body)
		kitContent.WriteString("\n")
	}

	if kitContent.Len() == 0 {
		return cue.Value{}, fmt.Errorf("no .cue files found in %s", dir)
	}

	// l.baseSource was already validated when it was built, so any dropped
	// import here would be one this loader itself emitted.
	baseImports, baseBody, _ := extractImportsAndBody([]byte(l.baseSource))
	mergedImports := make(map[string]bool, len(baseImports)+len(seenImports))
	for _, imp := range baseImports {
		mergedImports[imp] = true
	}
	for imp := range seenImports {
		mergedImports[imp] = true
	}

	// Build combined content: base source + kit source with all stdlib imports.
	var finalContent strings.Builder
	writeImportBlock(&finalContent, mergedImports)
	finalContent.WriteString(baseBody)
	finalContent.WriteString("\n\n// --- Kit-specific definitions ---\n\n")

	// Add kit content
	finalContent.WriteString(kitContent.String())

	// Compile everything together, one compile at a time process-wide.
	compileBudget.Lock()
	combined := l.ctx.CompileBytes([]byte(finalContent.String()), cue.Filename(dir+"/combined.cue"))
	compileBudget.Unlock()
	if combined.Err() != nil {
		if os.Getenv("TECHSTACK_CUE_DUMP") == "1" {
			repoRoot := ""
			if l.kitsDir != "" {
				// l.kitsDir typically points to <repo>/pkg/stackkits
				repoRoot = filepath.Dir(filepath.Dir(l.kitsDir))
			} else if wd, wdErr := os.Getwd(); wdErr == nil {
				repoRoot = wd
			}
			if repoRoot != "" {
				dumpDir := filepath.Join(repoRoot, "tmp")
				if mkErr := os.MkdirAll(dumpDir, 0o750); mkErr == nil {
					dumpFile := filepath.Join(dumpDir, fmt.Sprintf("techstack-kit-%d.cue", os.Getpid()))
					if writeErr := os.WriteFile(dumpFile, []byte(finalContent.String()), 0o600); writeErr == nil {
						return cue.Value{}, fmt.Errorf("failed to compile kit (dumped to %s): %s", dumpFile, cueerrors.Details(combined.Err(), nil))
					}
				}
			}
		}
		return cue.Value{}, fmt.Errorf("failed to compile kit: %s", cueerrors.Details(combined.Err(), nil))
	}

	return combined, nil
}

// GetKitInfo extracts metadata from a loaded StackKit.
func (l *StackKitLoader) GetKitInfo(name string) (*StackKitInfo, error) {
	kit, err := l.LoadKit(name)
	if err != nil {
		return nil, err
	}
	canonicalName := CanonicalStackKitName(name)

	// Try to extract metadata from the kit's #HomelabStarterKit or similar
	// For now, return basic info
	info := &StackKitInfo{
		Name: canonicalName,
	}

	// Try to lookup metadata path
	metaPath := cue.ParsePath("metadata")
	meta := kit.LookupPath(metaPath)
	if meta.Exists() {
		if v := meta.LookupPath(cue.ParsePath("name")); v.Exists() {
			info.Name, _ = v.String()
		}
		if v := meta.LookupPath(cue.ParsePath("displayName")); v.Exists() {
			info.DisplayName, _ = v.String()
		}
		if v := meta.LookupPath(cue.ParsePath("version")); v.Exists() {
			info.Version, _ = v.String()
		}
		if v := meta.LookupPath(cue.ParsePath("description")); v.Exists() {
			info.Description, _ = v.String()
		}
		if v := meta.LookupPath(cue.ParsePath("deprecated")); v.Exists() {
			info.Deprecated, _ = v.Bool()
		}
		if v := meta.LookupPath(cue.ParsePath("author")); v.Exists() {
			info.Author, _ = v.String()
		}
		if v := meta.LookupPath(cue.ParsePath("license")); v.Exists() {
			info.License, _ = v.String()
		}
		if tags := meta.LookupPath(cue.ParsePath("tags")); tags.Exists() {
			info.Tags = cueStringList(tags)
		}
	}

	services, err := l.readStackKitServiceCatalog(name)
	if err == nil {
		info.Services = services
	}

	if fileMeta, err := l.readStackKitYAMLMetadata(name); err == nil && fileMeta != nil {
		if fileMeta.Name != "" {
			info.Name = fileMeta.Name
		}
		if fileMeta.Description != "" {
			info.Description = fileMeta.Description
		}
		if fileMeta.Version != "" {
			info.Version = fileMeta.Version
		}
		if fileMeta.Author != "" {
			info.Author = fileMeta.Author
		}
		if fileMeta.License != "" {
			info.License = fileMeta.License
		}
		if len(fileMeta.Tags) > 0 {
			info.Tags = append([]string(nil), fileMeta.Tags...)
		}
		if len(fileMeta.Features) > 0 {
			info.Features = append([]string(nil), fileMeta.Features...)
		}
		if len(fileMeta.Requires.OSSupported) > 0 {
			info.SupportedOS = append([]string(nil), fileMeta.Requires.OSSupported...)
		}
		info.Modes = convertStackKitModes(fileMeta)
	}

	info.Name = CanonicalStackKitName(info.Name)
	if info.Name == "" {
		info.Name = canonicalName
	}
	if info.DisplayName == "" || legacyBaseKitDisplayName(info.DisplayName) {
		info.DisplayName = productStackKitDisplayName(info.Name)
	}

	return info, nil
}

func legacyBaseKitDisplayName(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	return normalized == "base kit" || normalized == StackKitLegacyBaseSlug
}

func productStackKitDisplayName(name string) string {
	switch CanonicalStackKitName(name) {
	case StackKitBasement:
		return "Basement Kit"
	case StackKitCloud:
		return "Cloud Kit"
	default:
		return name
	}
}

func (l *StackKitLoader) readStackKitYAMLMetadata(name string) (*stackkitcatalog.StackKitMeta, error) {
	content, err := l.readStackKitFile(name, "stackkit.yaml")
	if err != nil {
		return nil, err
	}

	meta, err := stackkitcatalog.ParseStackKitMetaYAML(content)
	if err != nil {
		return nil, err
	}

	return meta, nil
}

func (l *StackKitLoader) effectiveKitsDir() string {
	if l.kitsDir != "" {
		return l.kitsDir
	}
	if dir := DefaultStackKitsDir(); dir != "" {
		return dir
	}
	if _, file, _, ok := runtime.Caller(0); ok {
		return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "stackkits"))
	}
	return filepath.Clean(filepath.Join("pkg", "stackkits"))
}

func (l *StackKitLoader) readStackKitServiceCatalog(name string) (struct {
	Required    []string `json:"required,omitempty"`
	Recommended []string `json:"recommended,omitempty"`
	Available   []string `json:"available,omitempty"`
}, error) {
	var services struct {
		Required    []string `json:"required,omitempty"`
		Recommended []string `json:"recommended,omitempty"`
		Available   []string `json:"available,omitempty"`
	}

	content, err := l.readStackKitFile(name, "stackfile.cue")
	if err != nil {
		return services, err
	}

	source := string(content)
	services.Required = extractQuotedArray(source, `_requiredServices:\s*\[(?s)(.*?)\]`)
	services.Recommended = extractQuotedArray(source, `_recommendedServices:\s*\[(?s)(.*?)\]`)
	services.Available = extractQuotedArray(source, `_availableServices:\s*\[(?s)(.*?)\]`)

	return services, nil
}

func (l *StackKitLoader) stackKitDir(name string) (string, error) {
	if err := validateStackKitName(name); err != nil {
		return "", err
	}
	root, err := secureExistingDir(l.effectiveKitsDir())
	if err != nil {
		return "", err
	}
	for _, dirName := range resolveKitDirNames(name) {
		if dir, err := existingDirUnderRoot(root, dirName); err == nil {
			return dir, nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	return "", fmt.Errorf("kit directory %q not found under %s", resolveKitDirName(name), root)
}

func (l *StackKitLoader) readStackKitFile(name, filename string) ([]byte, error) {
	kitDir, err := l.stackKitDir(name)
	if err != nil {
		return nil, err
	}
	return readRegularFileUnderRoot(kitDir, filename)
}

func secureExistingDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("directory path is required")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve directory %q: %w", path, err)
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

func secureExistingContentDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("directory path is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("stat directory %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing symlink directory %s", path)
	}
	return secureExistingDir(path)
}

func safeJoinUnderRoot(root string, elems ...string) (string, error) {
	root, err := secureExistingDir(root)
	if err != nil {
		return "", err
	}
	parts := append([]string{root}, elems...)
	full := filepath.Clean(filepath.Join(parts...))
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return "", fmt.Errorf("resolve relative path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q escapes root %q", full, root)
	}
	return full, nil
}

func existingDirUnderRoot(root string, elems ...string) (string, error) {
	path, err := safeJoinUnderRoot(root, elems...)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing symlink directory %s", path)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", path)
	}
	return path, nil
}

func readRegularFileUnderRoot(root, name string) ([]byte, error) {
	if name == "" || filepath.IsAbs(name) || strings.Contains(name, "..") {
		return nil, fmt.Errorf("unsafe file name %q", name)
	}
	path, err := safeJoinUnderRoot(root, name)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing symlink file %s", path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return os.ReadFile(path)
}

func cueStringList(v cue.Value) []string {
	if !v.Exists() {
		return nil
	}
	iter, err := v.List()
	if err != nil {
		return nil
	}

	items := make([]string, 0)
	for iter.Next() {
		item, err := iter.Value().String()
		if err != nil || item == "" {
			continue
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return nil
	}
	return items
}

func convertStackKitModes(meta *stackkitcatalog.StackKitMeta) map[string]StackKitModeInfo {
	modes := make(map[string]StackKitModeInfo)
	if meta == nil {
		return modes
	}
	if meta.Mode.Simple {
		modes["simple"] = StackKitModeInfo{}
	}
	if meta.Mode.Advanced {
		modes["advanced"] = StackKitModeInfo{}
	}
	if len(modes) == 0 {
		return nil
	}
	return modes
}

func extractQuotedArray(source string, pattern string) []string {
	re := regexp.MustCompile(pattern)
	match := re.FindStringSubmatch(source)
	if len(match) < 2 {
		return nil
	}
	itemRe := regexp.MustCompile(`"([^"]+)"`)
	items := itemRe.FindAllStringSubmatch(match[1], -1)
	if len(items) == 0 {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if len(item) < 2 || item[1] == "" {
			continue
		}
		result = append(result, item[1])
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// ListAvailableKits returns a list of all available StackKits.
func (l *StackKitLoader) ListAvailableKits() []string {
	seen := make(map[string]struct{})
	kits := make([]string, 0, len(DefaultKnownStackKits()))
	for _, kit := range append(DiscoverAvailableStackKits(l.effectiveKitsDir()), DefaultKnownStackKits()...) {
		kit = CanonicalStackKitName(kit)
		if kit == "" || !IsSupportedProductStackKit(kit) {
			continue
		}
		if _, ok := seen[kit]; ok {
			continue
		}
		seen[kit] = struct{}{}
		kits = append(kits, kit)
	}
	if len(kits) > 0 {
		return kits
	}
	return DefaultKnownStackKits()
}

// GetBaseKit returns the base StackKit schema, evaluating it on first use.
//
// The base is not evaluated at load time any more (see loadBaseKitFromDir), so
// the cost is paid here, once, and only by callers that actually want the base
// value rather than by every rollout.
func (l *StackKitLoader) GetBaseKit() cue.Value {
	l.mu.RLock()
	if l.baseKit.Exists() || l.baseSource == "" {
		value := l.baseKit
		l.mu.RUnlock()
		return value
	}
	l.mu.RUnlock()

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.baseKit.Exists() {
		return l.baseKit
	}
	compileBudget.Lock()
	value := l.ctx.CompileBytes([]byte(l.baseSource), cue.Filename("base/combined.cue"))
	compileBudget.Unlock()
	if value.Err() != nil {
		return cue.Value{}
	}
	l.baseKit = value
	return l.baseKit
}

// ValidateAgainstKit validates a spec against a specific StackKit.
func (l *StackKitLoader) ValidateAgainstKit(kitName string, specValue cue.Value) error {
	kit, err := l.LoadKit(kitName)
	if err != nil {
		return err
	}

	// Unify the spec with the kit schema
	unified := kit.Unify(specValue)
	if err := unified.Validate(cue.Concrete(false)); err != nil {
		return fmt.Errorf("spec does not conform to StackKit '%s': %w", kitName, err)
	}

	return nil
}

// Context returns the CUE context used by this loader.
func (l *StackKitLoader) Context() *cue.Context {
	return l.ctx
}
