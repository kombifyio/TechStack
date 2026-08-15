package unifier

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDefaultStackKitsDirPrefersExternalEnv(t *testing.T) {
	t.Setenv("STACKKITS_REPO", "")
	t.Setenv("STACKKITS_PATH", "")
	external := t.TempDir()
	t.Setenv("TECHSTACK_STACKKITS_DIR", external)

	if got := DefaultStackKitsDir(); got != filepath.Clean(external) {
		t.Fatalf("DefaultStackKitsDir() = %q, want %q", got, filepath.Clean(external))
	}
}

func TestCanonicalStackKitName(t *testing.T) {
	tests := map[string]string{
		"base-kit":     StackKitBasement,
		"basement-kit": StackKitBasement,
		"basement":     StackKitBasement,
		"cloud-kit":    StackKitCloud,
		"cloud":        StackKitCloud,
	}

	for input, want := range tests {
		if got := CanonicalStackKitName(input); got != want {
			t.Fatalf("CanonicalStackKitName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDiscoverAvailableStackKits(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"base-kit", "basement-kit", "cloud-kit", "modern-homelab", "notes"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "base-kit", "stackkit.yaml"), []byte("metadata:\n  name: base-kit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "basement-kit", "stackkit.yaml"), []byte("metadata:\n  name: basement-kit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cloud-kit", "stackkit.yaml"), []byte("metadata:\n  name: cloud-kit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "modern-homelab", "stackkit.yaml"), []byte("metadata:\n  name: modern-homelab\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := DiscoverAvailableStackKits(root)
	want := []string{"basement-kit", "cloud-kit", "modern-homelab"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DiscoverAvailableStackKits() = %#v, want %#v", got, want)
	}
}

func TestDiscoverAvailableStackKitsSkipsSymlinkMetadata(t *testing.T) {
	root := t.TempDir()
	kitDir := filepath.Join(root, "basement-kit")
	if err := os.MkdirAll(kitDir, 0o750); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "external-stackkit.yaml")
	if err := os.WriteFile(outside, []byte("metadata:\n  name: basement-kit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(kitDir, "stackkit.yaml")); err != nil {
		t.Skipf("symlinks unavailable in this environment: %v", err)
	}

	if got := DiscoverAvailableStackKits(root); len(got) != 0 {
		t.Fatalf("DiscoverAvailableStackKits() = %#v, want no symlink-backed kits", got)
	}
}
