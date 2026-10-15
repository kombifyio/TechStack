package stackkitrelease

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReleasedApplicationMaturityRetainsRuntimeAcceptanceDenial(t *testing.T) {
	const maturity = "experimental"
	application := UseCase{
		ID: "application", DefaultAlternative: "engine",
		ComputeTiers: map[string]UseCaseComputeTier{"standard": {Included: false, Reason: "runtime acceptance pending"}},
		Components:   []UseCaseComponent{{ID: "engine", Role: "primary", Kind: "application"}},
		Alternatives: []UseCaseAlternative{{ID: "engine", Modules: []UseCaseModule{{
			ComputeProfileFacts: map[string]UseCaseProfileFacts{"standard": {Maturity: maturity, Executable: true}},
		}}}},
	}
	if application.ApplicationMaturity("engine") != maturity {
		t.Fatal("executable application maturity must survive a denied runtime tier")
	}
	payload := CreationCatalogPayload(UseCaseCatalog{UseCases: []UseCase{application}}, map[string]string{}, true, nil)
	entry := payload["use_cases"].([]map[string]any)[0]
	if entry["maturity"] != maturity || entry["deliverable"] != false {
		t.Fatal("presentation must retain maturity without turning runtime denial into delivery admission")
	}
	if entry["compute_tiers"].(map[string]UseCaseComputeTier)["standard"] != application.ComputeTiers["standard"] {
		t.Fatal("presentation changed the runtime acceptance verdicts")
	}
	facts := application.Alternatives[0].Modules[0].ComputeProfileFacts["standard"]
	facts.Executable = false
	application.Alternatives[0].Modules[0].ComputeProfileFacts["standard"] = facts
	if application.ApplicationMaturity("engine") != "" {
		t.Fatal("a non-executable application must not become available")
	}
}

func TestPinnedUseCaseCatalogOmitsMediaOnLow(t *testing.T) {
	t.Setenv(UseCaseCatalogEnv, "")
	t.Setenv("STACKKITS_REPO", filepath.Join(t.TempDir(), "missing-checkout"))
	t.Setenv("TECHSTACK_STACKKITS_DIR", "")
	t.Setenv("STACKKITS_PATH", "")

	catalog, err := ResolveUseCaseCatalog()
	if err != nil {
		t.Fatal(err)
	}
	media, ok := catalog.FindUseCase("media")
	if !ok {
		t.Fatal("pinned catalog missing media use case")
	}
	low, ok := media.ComputeTiers["low"]
	if !ok || low.Included {
		t.Fatalf("media low = %#v, want omitted on the pinned catalog", low)
	}
	photos, ok := catalog.FindUseCase("photos")
	if !ok {
		t.Fatal("pinned catalog missing photos use case")
	}
	photosLow, ok := photos.ComputeTiers["low"]
	if !ok || !photosLow.Included {
		t.Fatalf("photos low = %#v, want included on the pinned catalog", photosLow)
	}
	if catalog.Release.Tag == "" || catalog.Release.Version == "" {
		t.Fatalf("pinned catalog missing release identity: %#v", catalog.Release)
	}
}

func TestDecodeUseCaseCatalogRejectsUnknownSchema(t *testing.T) {
	_, err := DecodeUseCaseCatalog([]byte(`{"schemaVersion":"not-a-catalog","catalog":{"useCases":[]}}`))
	if err == nil {
		t.Fatal("expected unknown schema to fail closed")
	}
}

func TestResolveUseCaseCatalogPrefersPublishedPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	payload := []byte(`{
		"schemaVersion": "stackkits-use-case-catalog/v1",
		"release": {"tag": "v0.0.1", "version": "0.0.1"},
		"catalog": {"useCases": [{"id": "media", "title": "Media", "description": "test", "computeTiers": {"low": {"included": true}}}]}
	}`)
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(UseCaseCatalogEnv, path)
	catalog, err := ResolveUseCaseCatalog()
	if err != nil {
		t.Fatal(err)
	}
	media, ok := catalog.FindUseCase("media")
	if !ok {
		t.Fatal("published catalog missing media")
	}
	if !media.ComputeTiers["low"].Included {
		t.Fatal("published catalog should win over the embedded pin")
	}
}
