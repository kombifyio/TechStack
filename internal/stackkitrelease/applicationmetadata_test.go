package stackkitrelease

import (
	"encoding/json"
	"testing"
)

// SDK display links are pinned facts: altering them cannot create a catalog
// result under the original presentation digest.
func TestApplicationMetadataRejectsChangedPinnedLinks(t *testing.T) {
	original, err := DecodeApplicationMetadata(pinnedKitMetadataJSON)
	if err != nil {
		t.Fatal(err)
	}
	var document pinnedMetadataDocument
	if err := json.Unmarshal(pinnedKitMetadataJSON, &document); err != nil {
		t.Fatal(err)
	}
	changed := append(original.Applications, ApplicationPresentation{
		ID: "forged-sdk-presentation", GuideURL: "https://example.invalid/modeled-link",
	})
	bytes, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	document.ApplicationMetadata.JSON = string(bytes)
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeApplicationMetadata(data); err == nil {
		t.Fatal("changed SDK display links were accepted under the original digest")
	}
}
