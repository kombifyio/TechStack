package stackkitrelease

import (
	"bytes"
	"encoding/json"
	"testing"
)

// A catalog from another public source cannot borrow MCP/API/skill facts from
// this SDK pin, even when its use-case identities and tag are unchanged.
func TestAgentSurfacesFollowExactPublishedCatalog(t *testing.T) {
	catalog, err := PinnedUseCaseCatalog()
	if err != nil {
		t.Fatal(err)
	}
	sdk, err := DecodeAgentSurfaces(pinnedKitMetadataJSON)
	if err != nil {
		t.Fatal(err)
	}
	for _, useCase := range catalog.UseCases {
		if !bytes.Equal(useCase.AgentSurface, sdk.Surfaces[useCase.ID]) {
			t.Fatalf("use-case %s lost its declared SDK surface", useCase.ID)
		}
	}
	var document map[string]any
	if err := json.Unmarshal(pinnedUseCaseCatalogJSON, &document); err != nil {
		t.Fatal(err)
	}
	document["release"].(map[string]any)["publicSourceSha"] = "0000000000000000000000000000000000000000"
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	other, err := DecodeUseCaseCatalog(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, useCase := range other.UseCases {
		if len(useCase.AgentSurface) != 0 {
			t.Fatal("another public source borrowed the pinned SDK surface")
		}
	}
}
