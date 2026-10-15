package stackkitrelease

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

// Every Techstack copy of the release catalog must be the pinned release's:
// the embedded fallback byte-identical to the asset the image verifies, and
// the Creation preview rendered from that same release. A copy that lags the
// pin is how the Wizard kept offering a v0.24 catalog after many releases.
func TestEmbeddedCatalogsFollowTheImagePin(t *testing.T) {
	dockerfile, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	pinned := func(name string) string {
		match := regexp.MustCompile(`(?m)^ARG ` + name + `="([^"]+)"`).FindSubmatch(dockerfile)
		if match == nil {
			t.Fatalf("Dockerfile has no %s", name)
		}
		return string(match[1])
	}
	sum := sha256.Sum256(pinnedUseCaseCatalogJSON)
	if got, want := hex.EncodeToString(sum[:]), pinned("STACKKIT_RELEASE_USE_CASE_CATALOG_SHA256"); got != want {
		t.Errorf("embedded use-case catalog sha256 %s, image pin %s: run scripts/update-stackkits-pin.mjs", got, want)
	}
	preview, err := os.ReadFile("../../app/static/preview-catalogs/latest.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Release UseCaseCatalogRelease `json:"release"`
	}
	if err := json.Unmarshal(preview, &document); err != nil {
		t.Fatal(err)
	}
	if want := pinned("STACKKIT_RELEASE_TAG"); document.Release.Tag != want {
		t.Errorf("Creation preview shows %s, image pin %s: run scripts/update-stackkits-pin.mjs", document.Release.Tag, want)
	}
}
