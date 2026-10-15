package appcatalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

// A repin must change the existing runtime grouping without editing a local
// application list; a missing release application cannot become installation
// availability. Custom discovery still recognizes what the host measured.
func TestRuntimeApplicationFactsFollowReleaseRepin(t *testing.T) {
	catalog, err := stackkitrelease.PinnedUseCaseCatalog()
	if err != nil {
		t.Fatal(err)
	}
	writeCatalog := func(name string) string {
		t.Helper()
		data, err := json.Marshal(map[string]any{
			"schemaVersion": "stackkits-use-case-catalog/v1", "release": catalog.Release,
			"catalog": map[string]any{"useCases": catalog.UseCases},
		})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	t.Setenv(stackkitrelease.UseCaseCatalogEnv, writeCatalog("before.json"))
	newID := "release-added-application"
	subject := Subject{Name: newID, Platform: "docker", ComposeProject: "stackkit-core", ComposeService: newID}
	before := Group(subject)
	component := stackkitrelease.UseCaseComponent{ID: newID, Name: "Released application name", Kind: "application", Role: "alternative"}
	catalog.UseCases[0].Components = append(catalog.UseCases[0].Components, component)
	t.Setenv(stackkitrelease.UseCaseCatalogEnv, writeCatalog("after.json"))
	after := Group(subject)
	if before.DisplayName == after.DisplayName || after.DisplayName != component.Name || after.Key != component.ID {
		t.Fatalf("repin did not change measured application presentation: before=%+v after=%+v", before, after)
	}
	known := Match(Subject{Image: "ghcr.io/immich-app/immich-server:2", Name: "photos"})
	if known == nil || known.DisplayName == known.ID || known.WebPort == 0 {
		t.Fatalf("released names must join existing measured image/port recognition: %+v", known)
	}
	catalog.UseCases[0].Components = catalog.UseCases[0].Components[:len(catalog.UseCases[0].Components)-1]
	t.Setenv(stackkitrelease.UseCaseCatalogEnv, writeCatalog("removed.json"))
	if Lookup(newID) != nil || Group(subject).DisplayName != before.DisplayName {
		t.Fatal("removed release application retained released presentation")
	}
	if Match(Subject{Image: "plexinc/pms-docker:1"}) == nil {
		t.Fatal("unrelated measured application recognition was lost")
	}
}
