// Package stackkits provides StackKit registry and loading functionality.
// It supports loading StackKits from embedded, local, and remote sources.
package stackkits

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	slashpath "path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	// ErrStackKitNotFound is returned when a StackKit cannot be found.
	ErrStackKitNotFound = errors.New("stackkit not found")
	// ErrInvalidStackKit is returned when a StackKit has invalid structure.
	ErrInvalidStackKit = errors.New("invalid stackkit")
	// ErrRegistryUnavailable is returned when a remote registry is unreachable.
	ErrRegistryUnavailable = errors.New("registry unavailable")
)

// StackKitMeta contains metadata about a StackKit.
type StackKitMeta struct {
	Name        string            `json:"name" yaml:"name"`
	Version     string            `json:"version" yaml:"version"`
	Description string            `json:"description" yaml:"description"`
	Author      string            `json:"author,omitempty" yaml:"author,omitempty"`
	License     string            `json:"license,omitempty" yaml:"license,omitempty"`
	Tags        []string          `json:"tags,omitempty" yaml:"tags,omitempty"`
	Repository  string            `json:"repository,omitempty" yaml:"repository,omitempty"`
	Mode        StackKitMode      `json:"mode" yaml:"mode"`
	Features    []string          `json:"features,omitempty" yaml:"features,omitempty"`
	Requires    StackKitRequires  `json:"requires,omitempty" yaml:"requires,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// stackkitV1YAML represents the stackkit/v1 YAML format (structured with apiVersion/kind/metadata).
// TD-17-2: Bridge between the file format and the registry's StackKitMeta.
type stackkitV1YAML struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name              string   `yaml:"name"`
		Version           string   `yaml:"version"`
		Description       string   `yaml:"description"`
		Author            string   `yaml:"author"`
		License           string   `yaml:"license"`
		Tags              []string `yaml:"tags"`
		DeprecatedAliases []string `yaml:"deprecated_aliases"`
	} `yaml:"metadata"`
	SupportedOS  []string                     `yaml:"supportedOS"`
	Requirements map[string]map[string]int    `yaml:"requirements"`
	Modes        map[string]stackkitV1Mode    `yaml:"modes"`
	AutoSelect   map[string]interface{}       `yaml:"auto_select"`
	Features     map[string]map[string]string `yaml:"features"`
}

type stackkitV1Mode struct {
	Description    string   `yaml:"description"`
	TemplateDir    string   `yaml:"templateDir"`
	Engine         string   `yaml:"engine"`
	Requires       []string `yaml:"requires"`
	RecommendedFor []string `yaml:"recommended_for"`
}

// parseStackKitYAML parses a stackkit.yaml file, supporting both flat and v1 formats.
// TD-17-2: Auto-detects format by checking for apiVersion field.
func parseStackKitYAML(data []byte) (*StackKitMeta, error) {
	// First, try to detect format via apiVersion
	var probe struct {
		APIVersion string `yaml:"apiVersion"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}

	if probe.APIVersion == "stackkit/v1" {
		return parseStackKitV1(data)
	}

	// Fallback: flat format (legacy/embedded)
	var meta StackKitMeta
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("invalid stackkit metadata: %w", err)
	}
	return &meta, nil
}

// ParseStackKitMetaYAML parses stackkit metadata from either flat or stackkit/v1 YAML.
func ParseStackKitMetaYAML(data []byte) (*StackKitMeta, error) {
	return parseStackKitYAML(data)
}

// parseStackKitV1 parses the stackkit/v1 structured format into StackKitMeta.
func parseStackKitV1(data []byte) (*StackKitMeta, error) {
	var v1 stackkitV1YAML
	if err := yaml.Unmarshal(data, &v1); err != nil {
		return nil, fmt.Errorf("invalid stackkit/v1 format: %w", err)
	}

	meta := &StackKitMeta{
		Name:        v1.Metadata.Name,
		Version:     v1.Metadata.Version,
		Description: v1.Metadata.Description,
		Author:      v1.Metadata.Author,
		License:     v1.Metadata.License,
		Tags:        v1.Metadata.Tags,
	}

	// Map modes
	_, hasSimple := v1.Modes["simple"]
	_, hasAdvanced := v1.Modes["advanced"]
	meta.Mode = StackKitMode{
		Simple:   hasSimple,
		Advanced: hasAdvanced,
	}

	// Map requirements from minimum
	if reqs, ok := v1.Requirements["minimum"]; ok {
		meta.Requires = StackKitRequires{
			MinCPU:     reqs["cpu"],
			MinRAMGB:   reqs["memory"],
			MinStorage: reqs["disk"],
		}
	}
	meta.Requires.OSSupported = v1.SupportedOS

	// Map features
	for name := range v1.Features {
		meta.Features = append(meta.Features, name)
	}

	return meta, nil
}

// StackKitMode defines the deployment modes supported by a StackKit.
type StackKitMode struct {
	Simple   bool `json:"simple" yaml:"simple"`
	Advanced bool `json:"advanced" yaml:"advanced"`
}

// StackKitRequires defines the requirements for a StackKit.
type StackKitRequires struct {
	MinCPU      int      `json:"minCpu,omitempty" yaml:"minCpu,omitempty"`
	MinRAMGB    int      `json:"minRamGB,omitempty" yaml:"minRamGB,omitempty"`
	MinStorage  int      `json:"minStorageGB,omitempty" yaml:"minStorageGB,omitempty"`
	OSSupported []string `json:"osSupported,omitempty" yaml:"osSupported,omitempty"`
	Features    []string `json:"features,omitempty" yaml:"features,omitempty"`
}

// StackKit represents a loaded StackKit with all its resources.
type StackKit struct {
	Meta      StackKitMeta      `json:"meta"`
	Source    StackKitSource    `json:"source"`
	CUESchema string            `json:"cueSchema,omitempty"`
	Templates map[string]string `json:"templates,omitempty"`
	Variants  StackKitVariants  `json:"variants,omitempty"`
	LoadedAt  time.Time         `json:"loadedAt"`
}

// StackKitSource describes where a StackKit was loaded from.
type StackKitSource struct {
	Type     string `json:"type"` // "embedded", "local", "remote"
	Location string `json:"location"`
	Version  string `json:"version,omitempty"`
}

// StackKitVariants contains OS and compute variants.
type StackKitVariants struct {
	OS      map[string]string `json:"os,omitempty"`      // OS variant CUE files
	Compute map[string]string `json:"compute,omitempty"` // Compute tier CUE files
}

// Registry manages StackKit discovery and loading.
type Registry struct {
	mu         sync.RWMutex
	stackkits  map[string]*StackKit
	sources    []RegistrySource
	httpClient *http.Client
	cacheDir   string
	cacheTTL   time.Duration
}

// RegistrySource defines a source for StackKits.
type RegistrySource struct {
	Type     string `json:"type"` // "embedded", "local", "github", "http"
	URL      string `json:"url,omitempty"`
	Path     string `json:"path,omitempty"`
	Priority int    `json:"priority"` // Lower = higher priority
}

// RegistryConfig holds configuration for the Registry.
type RegistryConfig struct {
	Sources      []RegistrySource
	CacheDir     string
	CacheTTL     time.Duration
	HTTPTimeout  time.Duration
	EnableRemote bool
	GitHubToken  string // Optional: for private repos
}

// DefaultConfig returns a Registry configuration with sensible defaults.
func DefaultConfig() RegistryConfig {
	return RegistryConfig{
		Sources: []RegistrySource{
			{Type: "embedded", Priority: 0},
			{Type: "local", Path: "./stackkits", Priority: 10},
		},
		CacheDir:     filepath.Join(os.TempDir(), "techstack", "stackkits-cache"),
		CacheTTL:     24 * time.Hour,
		HTTPTimeout:  30 * time.Second,
		EnableRemote: true,
	}
}

// NewRegistry creates a new StackKit registry.
func NewRegistry(cfg RegistryConfig) *Registry {
	return &Registry{
		stackkits: make(map[string]*StackKit),
		sources:   cfg.Sources,
		httpClient: &http.Client{
			Timeout: cfg.HTTPTimeout,
		},
		cacheDir: cfg.CacheDir,
		cacheTTL: cfg.CacheTTL,
	}
}

// Register adds a StackKit to the registry.
func (r *Registry) Register(kit *StackKit) error {
	if kit == nil || kit.Meta.Name == "" {
		return ErrInvalidStackKit
	}
	kit.Meta.Name = normalizeRegistryStackKitName(kit.Meta.Name)

	r.mu.Lock()
	defer r.mu.Unlock()

	key := kit.Meta.Name
	if kit.Meta.Version != "" {
		key = fmt.Sprintf("%s@%s", kit.Meta.Name, kit.Meta.Version)
	}

	r.stackkits[key] = kit
	return nil
}

// Get retrieves a StackKit by name (optionally with version).
// Format: "name" or "name@version"
func (r *Registry) Get(ctx context.Context, nameVersion string) (*StackKit, error) {
	r.mu.RLock()
	if kit, ok := r.stackkits[nameVersion]; ok {
		r.mu.RUnlock()
		return kit, nil
	}
	r.mu.RUnlock()

	// Try loading from sources
	return r.loadFromSources(ctx, nameVersion)
}

// List returns all registered StackKits.
func (r *Registry) List() []*StackKitMeta {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*StackKitMeta, 0, len(r.stackkits))
	for _, kit := range r.stackkits {
		meta := kit.Meta
		result = append(result, &meta)
	}
	return result
}

// loadFromSources attempts to load a StackKit from configured sources.
func (r *Registry) loadFromSources(ctx context.Context, nameVersion string) (*StackKit, error) {
	name, version := parseNameVersion(nameVersion)

	for _, source := range r.sources {
		kit, err := r.loadFromSource(ctx, source, name, version)
		if err == nil && kit != nil {
			// Cache the loaded StackKit
			if err := r.Register(kit); err != nil {
				return nil, fmt.Errorf("failed to cache stackkit: %w", err)
			}
			return kit, nil
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrStackKitNotFound, nameVersion)
}

// loadFromSource loads a StackKit from a specific source.
func (r *Registry) loadFromSource(ctx context.Context, source RegistrySource, name, version string) (*StackKit, error) {
	switch source.Type {
	case "embedded":
		return r.loadEmbedded(name, version)
	case "local":
		return r.loadLocal(source.Path, name, version)
	case "github":
		return r.loadGitHub(ctx, source.URL, name, version)
	case "http":
		return r.loadHTTP(ctx, source.URL, name, version)
	default:
		return nil, fmt.Errorf("unknown source type: %s", source.Type)
	}
}

// loadEmbedded loads from embedded StackKits (compiled into binary).
// SK-L: Implements embedded StackKit loading with fallback support.
func (r *Registry) loadEmbedded(name, version string) (*StackKit, error) {
	return loadEmbeddedStackKit(name, version)
}

// loadLocal loads a StackKit from a local directory.
func (r *Registry) loadLocal(basePath, name, version string) (*StackKit, error) {
	kitPath := filepath.Join(basePath, name)
	if version != "" {
		kitPath = filepath.Join(basePath, name, version)
	}

	// Check for stackkit.yaml
	metaPath := filepath.Join(kitPath, "stackkit.yaml")
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrStackKitNotFound, name)
	}

	// TD-17-2: Auto-detect flat vs stackkit/v1 format
	meta, err := parseStackKitYAML(metaData)
	if err != nil {
		return nil, fmt.Errorf("invalid stackkit.yaml: %w", err)
	}

	kit := &StackKit{
		Meta: *meta,
		Source: StackKitSource{
			Type:     "local",
			Location: kitPath,
			Version:  version,
		},
		Templates: make(map[string]string),
		Variants: StackKitVariants{
			OS:      make(map[string]string),
			Compute: make(map[string]string),
		},
		LoadedAt: time.Now(),
	}

	// Load CUE schema
	schemaPath := filepath.Join(kitPath, "stackfile.cue")
	if data, err := os.ReadFile(schemaPath); err == nil {
		kit.CUESchema = string(data)
	}

	// Load templates
	if err := r.loadTemplates(kit, kitPath); err != nil {
		return nil, fmt.Errorf("failed to load templates: %w", err)
	}

	// Load variants
	if err := r.loadVariants(kit, kitPath); err != nil {
		return nil, fmt.Errorf("failed to load variants: %w", err)
	}

	return kit, nil
}

// loadTemplates loads OpenTofu templates from a StackKit directory.
func (r *Registry) loadTemplates(kit *StackKit, kitPath string) error {
	templatesDir := filepath.Join(kitPath, "templates")
	templatesRoot, err := os.OpenRoot(templatesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() {
		_ = templatesRoot.Close()
	}()

	// Simple mode
	if data, err := templatesRoot.ReadFile("simple/main.tf"); err == nil {
		kit.Templates["simple/main.tf"] = string(data)
	}

	// Advanced mode - walk the advanced directory (including root-level templates)
	if _, err := templatesRoot.Stat("advanced"); err == nil {
		err := fs.WalkDir(templatesRoot.FS(), "advanced", func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			// TD-17-2: Also load .tpl files (Go templates used by advanced mode)
			if strings.HasSuffix(path, ".tf") || strings.HasSuffix(path, ".tm.hcl") || strings.HasSuffix(path, ".tpl") {
				relPath := slashpath.Clean(path)
				data, err := templatesRoot.ReadFile(relPath)
				if err != nil {
					return err
				}
				kit.Templates[relPath] = string(data)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}

	return nil
}

// loadVariants loads OS and compute variants.
func (r *Registry) loadVariants(kit *StackKit, kitPath string) error {
	variantsDir := filepath.Join(kitPath, "variants")

	// OS variants
	osDir := filepath.Join(variantsDir, "os")
	if files, err := os.ReadDir(osDir); err == nil {
		for _, f := range files {
			if !f.IsDir() && strings.HasSuffix(f.Name(), ".cue") {
				data, err := os.ReadFile(filepath.Join(osDir, f.Name()))
				if err != nil {
					continue
				}
				name := strings.TrimSuffix(f.Name(), ".cue")
				kit.Variants.OS[name] = string(data)
			}
		}
	}

	// Compute variants
	computeDir := filepath.Join(variantsDir, "compute")
	if files, err := os.ReadDir(computeDir); err == nil {
		for _, f := range files {
			if !f.IsDir() && strings.HasSuffix(f.Name(), ".cue") {
				data, err := os.ReadFile(filepath.Join(computeDir, f.Name()))
				if err != nil {
					continue
				}
				name := strings.TrimSuffix(f.Name(), ".cue")
				kit.Variants.Compute[name] = string(data)
			}
		}
	}

	return nil
}

// loadGitHub loads a StackKit from a GitHub repository.
func (r *Registry) loadGitHub(ctx context.Context, repoURL, name, version string) (*StackKit, error) {
	// Parse GitHub URL: owner/repo
	parts := strings.Split(strings.TrimPrefix(repoURL, "github.com/"), "/")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid github repo format: %s", repoURL)
	}
	owner, repo := parts[0], parts[1]

	// Determine ref (tag/branch)
	ref := "main"
	if version != "" {
		ref = "v" + version
	}

	// Build raw content URL
	baseURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", owner, repo, ref, name)

	// Fetch stackkit.yaml
	metaURL := baseURL + "/stackkit.yaml"
	metaData, err := r.fetchURL(ctx, metaURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrStackKitNotFound, name)
	}

	// TD-17-2: Auto-detect flat vs stackkit/v1 format
	meta, err := parseStackKitYAML(metaData)
	if err != nil {
		return nil, fmt.Errorf("invalid stackkit.yaml: %w", err)
	}

	kit := &StackKit{
		Meta: *meta,
		Source: StackKitSource{
			Type:     "github",
			Location: fmt.Sprintf("%s/%s", owner, repo),
			Version:  version,
		},
		Templates: make(map[string]string),
		LoadedAt:  time.Now(),
	}

	// Fetch main CUE schema
	if data, err := r.fetchURL(ctx, baseURL+"/stackfile.cue"); err == nil {
		kit.CUESchema = string(data)
	}

	// Fetch simple template
	if data, err := r.fetchURL(ctx, baseURL+"/templates/simple/main.tf"); err == nil {
		kit.Templates["simple/main.tf"] = string(data)
	}

	return kit, nil
}

// loadHTTP loads a StackKit from an HTTP endpoint.
func (r *Registry) loadHTTP(ctx context.Context, baseURL, name, version string) (*StackKit, error) {
	kitURL := fmt.Sprintf("%s/%s", strings.TrimSuffix(baseURL, "/"), name)
	if version != "" {
		kitURL = fmt.Sprintf("%s/%s/%s", strings.TrimSuffix(baseURL, "/"), name, version)
	}

	// Fetch index/meta
	indexURL := kitURL + "/stackkit.json"
	indexData, err := r.fetchURL(ctx, indexURL)
	if err != nil {
		// Try YAML fallback
		indexURL = kitURL + "/stackkit.yaml"
		indexData, err = r.fetchURL(ctx, indexURL)
		if err != nil {
			return nil, fmt.Errorf("%w: %s", ErrStackKitNotFound, name)
		}
		// TD-17-2: Auto-detect flat vs stackkit/v1 format
		meta, err := parseStackKitYAML(indexData)
		if err != nil {
			return nil, fmt.Errorf("invalid stackkit metadata: %w", err)
		}
		return &StackKit{
			Meta: *meta,
			Source: StackKitSource{
				Type:     "http",
				Location: kitURL,
				Version:  version,
			},
			LoadedAt: time.Now(),
		}, nil
	}

	var meta StackKitMeta
	if err := json.Unmarshal(indexData, &meta); err != nil {
		return nil, fmt.Errorf("invalid stackkit.json: %w", err)
	}

	return &StackKit{
		Meta: meta,
		Source: StackKitSource{
			Type:     "http",
			Location: kitURL,
			Version:  version,
		},
		LoadedAt: time.Now(),
	}, nil
}

// fetchURL performs an HTTP GET request.
func (r *Registry) fetchURL(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrRegistryUnavailable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, url)
	}

	return io.ReadAll(resp.Body)
}

// parseNameVersion splits "name@version" into name and version.
func parseNameVersion(s string) (name, version string) {
	if idx := strings.Index(s, "@"); idx >= 0 {
		return normalizeRegistryStackKitName(s[:idx]), s[idx+1:]
	}
	return normalizeRegistryStackKitName(s), ""
}

func normalizeRegistryStackKitName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "base-kit", "basement", "basementkit":
		return "basement-kit"
	case "cloud", "cloudkit":
		return "cloud-kit"
	default:
		return name
	}
}

// RefreshCache clears the in-memory cache and reloads from sources.
func (r *Registry) RefreshCache() {
	r.mu.Lock()
	r.stackkits = make(map[string]*StackKit)
	r.mu.Unlock()
}

// AddSource adds a new registry source.
func (r *Registry) AddSource(source RegistrySource) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sources = append(r.sources, source)
}

// DefaultRegistry is the global StackKit registry.
var DefaultRegistry = NewRegistry(DefaultConfig())

// AddGitHubSource is a convenience function to add the official StackKits repo.
func AddGitHubSource(owner, repo string) {
	DefaultRegistry.AddSource(RegistrySource{
		Type:     "github",
		URL:      fmt.Sprintf("github.com/%s/%s", owner, repo),
		Priority: 50,
	})
}
