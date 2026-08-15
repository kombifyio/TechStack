// Package stackkits - Embedded StackKit loading support.
// This file provides embedded StackKits compiled into the binary using //go:embed.
package stackkits

import (
	"embed"
	"fmt"
	"path"
	"strings"
	"time"
)

// embeddedFS holds the embedded StackKits filesystem.
// Note: The actual embed directive points to the StackKits directory.
// For production use, the StackKits repository should be vendored or embedded
// during the build process. This placeholder enables the infrastructure.
//
//go:embed embedded/*
var embeddedFS embed.FS

// EmbeddedStackKits is a list of StackKit names available in the embedded filesystem.
// This list is used as a fallback when the embedded filesystem doesn't have entries.
var EmbeddedStackKits = []string{
	"basement-kit",
}

// loadEmbeddedStackKit loads a StackKit from the embedded filesystem.
func loadEmbeddedStackKit(name, version string) (*StackKit, error) {
	name = normalizeEmbeddedStackKitName(name)
	// Build path to the stackkit
	kitPath := path.Join("embedded", name)

	// Check if the embedded directory exists
	entries, err := embeddedFS.ReadDir(kitPath)
	if err != nil {
		if name == "basement-kit" {
			legacyPath := path.Join("embedded", "base-kit")
			if legacyEntries, legacyErr := embeddedFS.ReadDir(legacyPath); legacyErr == nil {
				kitPath = legacyPath
				entries = legacyEntries
				err = nil
			}
		}
	}
	if err != nil {
		// Fallback: return a minimal StackKit for supported kits if embedded assets are absent.
		if name == "basement-kit" || name == "cloud-kit" {
			return createFallbackStackKit(name, version), nil
		}
		return nil, fmt.Errorf("%w: %s (not embedded)", ErrStackKitNotFound, name)
	}

	// Check for stackkit.yaml
	metaPath := path.Join(kitPath, "stackkit.yaml")
	metaData, err := embeddedFS.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %s (missing stackkit.yaml)", ErrStackKitNotFound, name)
	}

	// TD-17-2: Auto-detect flat vs stackkit/v1 format
	meta, err := parseStackKitYAML(metaData)
	if err != nil {
		return nil, fmt.Errorf("invalid stackkit.yaml: %w", err)
	}
	meta.Name = normalizeEmbeddedStackKitName(meta.Name)

	kit := &StackKit{
		Meta: *meta,
		Source: StackKitSource{
			Type:     "embedded",
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

	// Load CUE schema if present
	schemaPath := path.Join(kitPath, "stackfile.cue")
	if data, err := embeddedFS.ReadFile(schemaPath); err == nil {
		kit.CUESchema = string(data)
	}

	// Load templates from templates/ directory
	templatesPath := path.Join(kitPath, "templates")
	if templateEntries, err := embeddedFS.ReadDir(templatesPath); err == nil {
		for _, entry := range templateEntries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".tmpl") {
				tmplPath := path.Join(templatesPath, entry.Name())
				if data, err := embeddedFS.ReadFile(tmplPath); err == nil {
					kit.Templates[entry.Name()] = string(data)
				}
			}
		}
	}

	// Load variants from variants/ directory
	variantsPath := path.Join(kitPath, "variants")
	if variantEntries, err := embeddedFS.ReadDir(variantsPath); err == nil {
		for _, entry := range variantEntries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".yaml") {
				variantPath := path.Join(variantsPath, entry.Name())
				if data, err := embeddedFS.ReadFile(variantPath); err == nil {
					variantName := strings.TrimSuffix(entry.Name(), ".yaml")
					kit.Variants.OS[variantName] = string(data)
				}
			}
		}
	}

	// Count entries loaded (for debugging)
	_ = entries

	return kit, nil
}

func normalizeEmbeddedStackKitName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "base-kit", "basement", "basementkit":
		return "basement-kit"
	case "cloud", "cloudkit":
		return "cloud-kit"
	default:
		return name
	}
}

// createFallbackStackKit creates a minimal StackKit when embedded files aren't available.
// This ensures the system can function even without embedded StackKits.
func createFallbackStackKit(name, version string) *StackKit {
	return &StackKit{
		Meta: StackKitMeta{
			Name:        name,
			Version:     "1.0.0",
			Description: "Fallback StackKit (embedded files not available)",
			Mode: StackKitMode{
				Simple:   true,
				Advanced: true,
			},
			Tags: []string{"homelab", "fallback"},
		},
		Source: StackKitSource{
			Type:     "embedded-fallback",
			Location: name,
			Version:  version,
		},
		Templates: make(map[string]string),
		Variants: StackKitVariants{
			OS:      make(map[string]string),
			Compute: make(map[string]string),
		},
		LoadedAt: time.Now(),
	}
}

// ListEmbeddedStackKits returns the list of available embedded StackKits.
func ListEmbeddedStackKits() []string {
	// Try to read from the embedded filesystem first
	entries, err := embeddedFS.ReadDir("embedded")
	if err != nil {
		// Fallback to the known list
		return EmbeddedStackKits
	}

	var kits []string
	seen := make(map[string]struct{})
	for _, entry := range entries {
		if entry.IsDir() {
			// Check if it has a stackkit.yaml
			metaPath := path.Join("embedded", entry.Name(), "stackkit.yaml")
			if _, err := embeddedFS.ReadFile(metaPath); err == nil {
				kitName := normalizeEmbeddedStackKitName(entry.Name())
				if _, ok := seen[kitName]; !ok {
					seen[kitName] = struct{}{}
					kits = append(kits, kitName)
				}
			}
		}
	}

	if len(kits) == 0 {
		return EmbeddedStackKits
	}

	return kits
}

// HasEmbeddedStackKit checks if a StackKit is available in the embedded filesystem.
func HasEmbeddedStackKit(name string) bool {
	name = normalizeEmbeddedStackKitName(name)
	// Check if directory exists
	kitPath := path.Join("embedded", name, "stackkit.yaml")
	if _, err := embeddedFS.ReadFile(kitPath); err == nil {
		return true
	}
	if name == "basement-kit" {
		legacyPath := path.Join("embedded", "base-kit", "stackkit.yaml")
		if _, err := embeddedFS.ReadFile(legacyPath); err == nil {
			return true
		}
	}

	// Check fallback list
	for _, kit := range EmbeddedStackKits {
		if normalizeEmbeddedStackKitName(kit) == name {
			return true
		}
	}

	return false
}
