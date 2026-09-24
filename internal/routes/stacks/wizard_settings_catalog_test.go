package stacks

import (
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/specv2"
)

// Configuration boundary: every service/profile choice advertised from the
// pinned catalog is accepted, while values the catalog did not advertise fail
// closed before a Wizard run creates state.
func TestWizardUseCaseSettingsFollowResolvedCatalogChoices(t *testing.T) {
	t.Setenv(stackkitrelease.UseCaseCatalogEnv, "")
	catalog, err := stackkitrelease.ResolveUseCaseCatalog()
	if err != nil {
		t.Fatal(err)
	}
	files, ok := catalog.FindUseCase("files")
	if !ok {
		t.Fatal("pinned catalog has no files use case")
	}
	backend, ok := files.FindSetting("backend")
	if !ok || len(backend.Options) < 2 {
		t.Fatalf("files has no selectable catalog backend: %#v", backend)
	}
	profile, ok := files.FindSetting("profile")
	if !ok || len(profile.Options) == 0 {
		t.Fatalf("files has no included compute profile: %#v", profile)
	}

	intent := specv2.WizardIntent{
		Goals: []string{"files"},
		UseCaseSettings: map[string]map[string]any{
			"files": {
				"backend": backend.Options[1].ID,
				"profile": profile.Options[0].ID,
			},
		},
	}
	if err := validateUseCaseSettingsAgainstCatalog(intent); err != nil {
		t.Fatalf("advertised catalog choices were rejected: %v", err)
	}

	for name, settings := range map[string]map[string]any{
		"unknown backend": {"backend": "not-in-the-catalog"},
		"unknown profile": {"profile": "not-in-the-catalog"},
		"unknown setting": {"not-in-the-catalog": true},
	} {
		t.Run(name, func(t *testing.T) {
			intent.UseCaseSettings["files"] = settings
			if err := validateUseCaseSettingsAgainstCatalog(intent); err == nil {
				t.Fatal("unadvertised choice was accepted")
			}
		})
	}
}
