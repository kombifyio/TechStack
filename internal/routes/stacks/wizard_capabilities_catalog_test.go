package stacks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/specv2"
)

const capabilityCatalogFixture = `{
  "schemaVersion": "stackkits-use-case-catalog/v1",
  "release": {"tag": "v0.0.0", "version": "0.0.0"},
  "catalog": {"useCases": [{
    "id": "ai", "title": "AI", "description": "AI",
    "capabilities": [
      {"id": "inference", "name": "Inference", "required": true, "enabledByDefault": true,
       "default": {"id": "ollama", "name": "Ollama", "realization": "install"},
       "alternative": {"id": "vllm", "name": "vLLM", "realization": "install"}},
      {"id": "chat", "name": "Chat", "required": false, "enabledByDefault": true,
       "default": {"id": "open-webui", "name": "Open WebUI", "realization": "install"},
       "alternative": {"id": "librechat", "name": "LibreChat", "realization": "recorded"}}
    ]
  }]}
}`

// Capability boundary: only options the catalog advertises as installable, or
// "off" for an optional capability, pass before a Wizard run creates state.
func TestWizardUseCaseCapabilitiesFollowCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, []byte(capabilityCatalogFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(stackkitrelease.UseCaseCatalogEnv, path)

	for name, test := range map[string]struct {
		selection map[string]string
		wantErr   bool
	}{
		"default and alternative": {map[string]string{"inference": "vllm", "chat": "open-webui"}, false},
		"optional off":            {map[string]string{"chat": "off"}, false},
		"unknown option":          {map[string]string{"inference": "nope"}, true},
		"unknown capability":      {map[string]string{"nope": "off"}, true},
		"required off":            {map[string]string{"inference": "off"}, true},
		"recorded option":         {map[string]string{"chat": "librechat"}, true},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateUseCaseCapabilitiesAgainstCatalog(specv2.WizardIntent{
				Goals:               []string{"ai"},
				UseCaseCapabilities: map[string]map[string]string{"ai": test.selection},
			})
			if (err != nil) != test.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}
