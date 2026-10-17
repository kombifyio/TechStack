package specv2

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

func TestCatalogDeliveryRejectsMixedRelease(t *testing.T) {
	release := stackkitrelease.UseCaseCatalogRelease{
		Tag: "v0.53.8", Version: "0.53.8",
		SourceSHA:       "bcfa561ea9becb844036d4e1e6264fe77d5d25f9",
		PublicSourceSHA: "f49db60bdfd6409b51382bb2731635c2a7340420",
	}
	manifest := map[string]any{
		"schemaVersion": "stackkits-compatibility/v1", "release": release,
		"compatibility": map[string]any{"applicationDelivery": []map[string]string{{
			"useCaseRef": "files", "workloadRef": "files", "adapterRef": "standalone-compose", "status": "beta",
		}}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "compatibility.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(CompatibilityManifestEnv, path)
	delivery, err := CatalogDeliveryForRelease(release)
	if err != nil || !delivery.Known || delivery.Goals["files"] != "files" {
		t.Fatalf("matching release lost its delivery admission: %+v, %v", delivery, err)
	}
	for _, change := range []struct {
		name string
		edit func(*stackkitrelease.UseCaseCatalogRelease)
	}{
		{"version", func(r *stackkitrelease.UseCaseCatalogRelease) { r.Tag, r.Version = "v0.53.6", "0.53.6" }},
		{"tag", func(r *stackkitrelease.UseCaseCatalogRelease) { r.Tag = "v0.53.6" }},
		{"source", func(r *stackkitrelease.UseCaseCatalogRelease) {
			r.SourceSHA = "83de297b12007e54af4428ec5dab3f5eb958af58"
		}},
		{"public source", func(r *stackkitrelease.UseCaseCatalogRelease) {
			r.PublicSourceSHA = "c76cc82c23ef9262b7040f2cbf88fcf0db500103"
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			other := release
			change.edit(&other)
			if delivery, err := CatalogDeliveryForRelease(other); err == nil || delivery.Known {
				t.Fatalf("another release inherited delivery admission: %+v, %v", delivery, err)
			}
		})
	}
}
