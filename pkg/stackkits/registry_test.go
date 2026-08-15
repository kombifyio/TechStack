package stackkits

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseNameVersion(t *testing.T) {
	tests := []struct {
		input       string
		wantName    string
		wantVersion string
	}{
		{"basement-kit", "basement-kit", ""},
		{"basement-kit@1.0.0", "basement-kit", "1.0.0"},
		{"modern-kit@2.3.4", "modern-kit", "2.3.4"},
		{"kit@v1", "kit", "v1"},
		{"simple", "simple", ""},
	}

	for _, tc := range tests {
		name, version := parseNameVersion(tc.input)
		if name != tc.wantName || version != tc.wantVersion {
			t.Errorf("parseNameVersion(%q) = (%q, %q), want (%q, %q)",
				tc.input, name, version, tc.wantName, tc.wantVersion)
		}
	}
}

func TestRegistryRegister(t *testing.T) {
	reg := NewRegistry(DefaultConfig())

	kit := &StackKit{
		Meta: StackKitMeta{
			Name:        "test-kit",
			Version:     "1.0.0",
			Description: "Test StackKit",
		},
		Source: StackKitSource{
			Type:     "test",
			Location: "memory",
		},
		LoadedAt: time.Now(),
	}

	if err := reg.Register(kit); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	// Should be retrievable
	ctx := context.Background()
	got, err := reg.Get(ctx, "test-kit@1.0.0")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if got.Meta.Name != kit.Meta.Name {
		t.Errorf("Get().Meta.Name = %q, want %q", got.Meta.Name, kit.Meta.Name)
	}
}

func TestRegistryRegisterInvalid(t *testing.T) {
	reg := NewRegistry(DefaultConfig())

	// Nil kit
	if err := reg.Register(nil); err != ErrInvalidStackKit {
		t.Errorf("Register(nil) error = %v, want %v", err, ErrInvalidStackKit)
	}

	// Empty name
	if err := reg.Register(&StackKit{}); err != ErrInvalidStackKit {
		t.Errorf("Register(empty) error = %v, want %v", err, ErrInvalidStackKit)
	}
}

func TestRegistryList(t *testing.T) {
	reg := NewRegistry(DefaultConfig())

	kits := []*StackKit{
		{Meta: StackKitMeta{Name: "kit-a", Version: "1.0.0"}},
		{Meta: StackKitMeta{Name: "kit-b", Version: "2.0.0"}},
	}

	for _, kit := range kits {
		if err := reg.Register(kit); err != nil {
			t.Fatalf("Register() error = %v", err)
		}
	}

	list := reg.List()
	if len(list) != 2 {
		t.Errorf("List() returned %d items, want 2", len(list))
	}
}

func TestLoadLocal(t *testing.T) {
	// Create temp directory structure
	tmpDir := t.TempDir()
	kitDir := filepath.Join(tmpDir, "test-kit")
	if err := os.MkdirAll(kitDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create stackkit.yaml
	metaYAML := `name: test-kit
version: "1.0.0"
description: Test StackKit for unit tests
mode:
  simple: true
  advanced: false
`
	if err := os.WriteFile(filepath.Join(kitDir, "stackkit.yaml"), []byte(metaYAML), 0644); err != nil {
		t.Fatal(err)
	}

	// Create stackfile.cue
	cueSrc := `package testkit

#TestConfig: {
    name: string
    enabled: bool | *true
}
`
	if err := os.WriteFile(filepath.Join(kitDir, "stackfile.cue"), []byte(cueSrc), 0644); err != nil {
		t.Fatal(err)
	}

	// Create templates directory
	templatesDir := filepath.Join(kitDir, "templates", "simple")
	if err := os.MkdirAll(templatesDir, 0755); err != nil {
		t.Fatal(err)
	}

	mainTF := `# Test main.tf
provider "docker" {}
`
	if err := os.WriteFile(filepath.Join(templatesDir, "main.tf"), []byte(mainTF), 0644); err != nil {
		t.Fatal(err)
	}

	// Create registry with local source
	cfg := RegistryConfig{
		Sources: []RegistrySource{
			{Type: "local", Path: tmpDir, Priority: 0},
		},
		CacheDir:    filepath.Join(tmpDir, "cache"),
		CacheTTL:    time.Hour,
		HTTPTimeout: 10 * time.Second,
	}
	reg := NewRegistry(cfg)

	// Load the kit
	ctx := context.Background()
	kit, err := reg.Get(ctx, "test-kit")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	// Verify metadata
	if kit.Meta.Name != "test-kit" {
		t.Errorf("Meta.Name = %q, want %q", kit.Meta.Name, "test-kit")
	}
	if kit.Meta.Version != "1.0.0" {
		t.Errorf("Meta.Version = %q, want %q", kit.Meta.Version, "1.0.0")
	}

	// Verify CUE schema loaded
	if kit.CUESchema == "" {
		t.Error("CUESchema is empty")
	}

	// Verify templates loaded
	if _, ok := kit.Templates["simple/main.tf"]; !ok {
		t.Error("Template simple/main.tf not loaded")
	}

	// Verify source info
	if kit.Source.Type != "local" {
		t.Errorf("Source.Type = %q, want %q", kit.Source.Type, "local")
	}
}

func TestLoadLocalNotFound(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := RegistryConfig{
		Sources: []RegistrySource{
			{Type: "local", Path: tmpDir, Priority: 0},
		},
		CacheDir:    filepath.Join(tmpDir, "cache"),
		CacheTTL:    time.Hour,
		HTTPTimeout: 10 * time.Second,
	}
	reg := NewRegistry(cfg)

	ctx := context.Background()
	_, err := reg.Get(ctx, "nonexistent-kit")
	if err == nil {
		t.Error("Get() should return error for nonexistent kit")
	}
}

func TestRefreshCache(t *testing.T) {
	reg := NewRegistry(DefaultConfig())

	kit := &StackKit{
		Meta: StackKitMeta{Name: "cached-kit", Version: "1.0.0"},
	}
	if err := reg.Register(kit); err != nil {
		t.Fatal(err)
	}

	// Verify it's cached
	list := reg.List()
	if len(list) != 1 {
		t.Fatalf("List() = %d items, want 1", len(list))
	}

	// Clear cache
	reg.RefreshCache()

	// Should be empty now
	list = reg.List()
	if len(list) != 0 {
		t.Errorf("List() after RefreshCache() = %d items, want 0", len(list))
	}
}

func TestAddSource(t *testing.T) {
	reg := NewRegistry(DefaultConfig())
	initialLen := len(reg.sources)

	reg.AddSource(RegistrySource{
		Type:     "github",
		URL:      "github.com/test/stackkits",
		Priority: 100,
	})

	if len(reg.sources) != initialLen+1 {
		t.Errorf("AddSource() didn't add source, len = %d, want %d", len(reg.sources), initialLen+1)
	}
}
