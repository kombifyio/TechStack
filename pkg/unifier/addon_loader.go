// Package unifier provides the Add-On Schema Loader for dynamic CUE schema loading.
package unifier

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	slashpath "path"
	"path/filepath"
	"strings"
	"sync"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
)

// AddonSchemaLoader loads and caches CUE add-on schemas.
type AddonSchemaLoader struct {
	ctx     *cue.Context
	schemas map[string]cue.Value
	mu      sync.RWMutex
	logger  *slog.Logger
	loaded  bool
}

// LoadedAddon represents a loaded add-on schema with metadata.
type LoadedAddon struct {
	Name        string
	DisplayName string
	Description string
	Version     string
	Priority    int
	Tags        []string
	CUEValue    cue.Value
}

// NewAddonSchemaLoader creates a new schema loader.
func NewAddonSchemaLoader() *AddonSchemaLoader {
	return &AddonSchemaLoader{
		ctx:     cuecontext.New(),
		schemas: make(map[string]cue.Value),
		logger:  slog.Default(),
	}
}

// LoadEmbedded loads all add-on schemas from the default stackkits/addons directory.
// This looks for addons relative to the working directory or in common locations.
func (l *AddonSchemaLoader) LoadEmbedded() error {
	// Try common paths
	paths := []string{}
	if stackkitsDir := DefaultStackKitsDir(); stackkitsDir != "" {
		paths = append(paths, filepath.Join(stackkitsDir, "addons"))
	}
	paths = append(paths,
		"addons",
		"pkg/stackkits/addons",
		"./pkg/stackkits/addons",
		"../stackkits/addons",
		"../../pkg/stackkits/addons",
	)

	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return l.LoadFromPath(path)
		}
	}

	l.logger.Warn("Could not find embedded addons directory")
	l.loaded = true
	return nil
}

// LoadFromPath loads add-on schemas from a filesystem path.
func (l *AddonSchemaLoader) LoadFromPath(addonsPath string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.loaded {
		return nil // Already loaded
	}

	addonsRoot, err := os.OpenRoot(addonsPath)
	if err != nil {
		return fmt.Errorf("failed to open addons root: %w", err)
	}
	defer func() {
		_ = addonsRoot.Close()
	}()

	// Load base schema first
	const basePath = "addon.cue"
	baseContent, err := addonsRoot.ReadFile(basePath)
	var baseValue cue.Value
	if err == nil {
		baseValue = l.ctx.CompileBytes(baseContent, cue.Filename("addon.cue"))
		if baseValue.Err() != nil {
			l.logger.Warn("Error compiling base schema", "error", baseValue.Err())
		}
	}

	// Walk the directory
	err = fs.WalkDir(addonsRoot.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			l.logger.Warn("WalkDir error", "path", path, "error", err)
			return err
		}

		l.logger.Debug("Visiting path", "path", path, "isDir", d.IsDir())

		if d.IsDir() || !strings.HasSuffix(path, ".cue") {
			return nil
		}

		if path == basePath {
			l.logger.Debug("Skipping base schema", "path", path)
			return nil // Skip base
		}

		// External StackKits add-ons live in <addon>/addon.cue. Ignore helper
		// files inside addon subfolders and only keep the folder's main addon.cue.
		isRootFile := slashpath.Dir(path) == "."
		if !isRootFile && slashpath.Base(path) != "addon.cue" {
			return nil
		}

		content, readErr := addonsRoot.ReadFile(path)
		if readErr != nil {
			l.logger.Warn("Could not read file", "path", path, "error", readErr)
			return nil
		}

		l.logger.Debug("Compiling CUE file", "path", path, "size", len(content))

		// Compile the addon directly - base schema is used for validation only
		val := l.ctx.CompileBytes(content, cue.Filename(slashpath.Base(path)))
		if val.Err() != nil {
			l.logger.Warn("Error compiling add-on", "path", path, "error", val.Err())
			return nil
		}

		// Optionally validate against base schema if it exists
		if baseValue.Exists() && baseValue.Err() == nil {
			unified := baseValue.Unify(val)
			if unified.Err() != nil {
				l.logger.Debug("Add-on does not conform to base schema",
					"path", path,
					"error", unified.Err(),
				)
				// Still use the original val, just warn
			}
		}

		if val.Err() != nil {
			l.logger.Warn("Error loading schema", "path", path, "error", val.Err())
			return nil
		}

		name := strings.TrimSuffix(slashpath.Base(path), ".cue")
		if slashpath.Base(path) == "addon.cue" {
			name = slashpath.Base(slashpath.Dir(path))
		}
		l.schemas[name] = val
		l.logger.Debug("Loaded add-on schema", "name", name, "path", path)
		return nil
	})

	l.loaded = true
	if err != nil {
		return fmt.Errorf("failed to walk addons path: %w", err)
	}

	l.logger.Info("Loaded add-on schemas", "count", len(l.schemas), "path", addonsPath)
	return nil
}

// Get returns a loaded add-on schema by name.
func (l *AddonSchemaLoader) Get(name string) (cue.Value, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	val, ok := l.schemas[name]
	return val, ok
}

// GetAll returns all loaded add-on schemas.
func (l *AddonSchemaLoader) GetAll() map[string]cue.Value {
	l.mu.RLock()
	defer l.mu.RUnlock()

	// Return a copy
	result := make(map[string]cue.Value, len(l.schemas))
	for k, v := range l.schemas {
		result[k] = v
	}
	return result
}

// GetLoadedAddon returns a LoadedAddon with extracted metadata.
func (l *AddonSchemaLoader) GetLoadedAddon(name string) (*LoadedAddon, error) {
	val, ok := l.Get(name)
	if !ok {
		return nil, fmt.Errorf("add-on not found: %s", name)
	}

	addon := &LoadedAddon{
		Name:     name,
		CUEValue: val,
	}

	for _, path := range []string{"metadata", "#Config._addon", "_addon"} {
		if meta := val.LookupPath(cue.ParsePath(path)); meta.Exists() {
			populateLoadedAddonFromMeta(addon, meta)
			break
		}
	}

	if addon.DisplayName == "" {
		// Fallback for older inline add-ons that keep metadata one level deeper.
		iter, _ := val.Fields()
		for iter.Next() {
			field := iter.Value()
			for _, path := range []string{"metadata", "_addon"} {
				if meta := field.LookupPath(cue.ParsePath(path)); meta.Exists() {
					populateLoadedAddonFromMeta(addon, meta)
					break
				}
			}
			if addon.DisplayName != "" {
				break
			}
		}
	}

	// Fallback defaults
	if addon.DisplayName == "" {
		addon.DisplayName = name
	}
	if addon.Version == "" {
		addon.Version = "1.0.0"
	}

	return addon, nil
}

func populateLoadedAddonFromMeta(addon *LoadedAddon, meta cue.Value) {
	if dn, err := meta.LookupPath(cue.ParsePath("displayName")).String(); err == nil {
		addon.DisplayName = dn
	}
	if desc, err := meta.LookupPath(cue.ParsePath("description")).String(); err == nil {
		addon.Description = desc
	}
	if ver, err := meta.LookupPath(cue.ParsePath("version")).String(); err == nil {
		addon.Version = ver
	}
	if prio, err := meta.LookupPath(cue.ParsePath("priority")).Int64(); err == nil {
		addon.Priority = int(prio)
	}
	if tags := meta.LookupPath(cue.ParsePath("tags")); tags.Exists() {
		tagIter, _ := tags.List()
		for tagIter.Next() {
			if t, err := tagIter.Value().String(); err == nil {
				addon.Tags = append(addon.Tags, t)
			}
		}
	}
}

// ListNames returns all loaded add-on names.
func (l *AddonSchemaLoader) ListNames() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()

	names := make([]string, 0, len(l.schemas))
	for name := range l.schemas {
		names = append(names, name)
	}
	return names
}

// MergeAddons merges multiple add-on schemas into a single CUE value.
// This is used to combine detected add-ons with the base StackKit schema.
func (l *AddonSchemaLoader) MergeAddons(addonNames []string) (cue.Value, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if len(addonNames) == 0 {
		return cue.Value{}, nil
	}

	// Start with the first addon
	firstName := addonNames[0]
	result, ok := l.schemas[firstName]
	if !ok {
		return cue.Value{}, fmt.Errorf("add-on not found: %s", firstName)
	}

	// Merge remaining addons
	for i := 1; i < len(addonNames); i++ {
		name := addonNames[i]
		addon, ok := l.schemas[name]
		if !ok {
			l.logger.Warn("Add-on not found, skipping", "name", name)
			continue
		}

		// Unify schemas
		result = result.Unify(addon)
		if result.Err() != nil {
			return cue.Value{}, fmt.Errorf("conflict merging add-on %s: %w", name, result.Err())
		}
	}

	return result, nil
}

// Count returns the number of loaded schemas.
func (l *AddonSchemaLoader) Count() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.schemas)
}
