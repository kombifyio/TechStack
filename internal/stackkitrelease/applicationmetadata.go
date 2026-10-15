package stackkitrelease

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// ApplicationPresentation carries only the SDK's declared presentation facts.
// ID is the owning registry's tool_name, never its URL-facing service key.
type ApplicationPresentation struct {
	ID       string              `json:"id"`
	GuideURL string              `json:"guide_url,omitempty"`
	Clients  []ApplicationClient `json:"clients,omitempty"`
	Vendor   *ApplicationVendor  `json:"vendor,omitempty"`
}

type ApplicationClient struct {
	Platform string `json:"platform"`
	Kind     string `json:"kind"`
	URL      string `json:"url"`
}

type ApplicationVendor struct {
	Name    string                   `json:"name,omitempty"`
	Website string                   `json:"website,omitempty"`
	Docs    string                   `json:"docs,omitempty"`
	License string                   `json:"license,omitempty"`
	Support []ApplicationSupportLink `json:"support,omitempty"`
}

type ApplicationSupportLink struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
}

type ApplicationMetadataCatalog struct {
	Source       SDKMetadataSource
	Applications []ApplicationPresentation
}

func PinnedApplicationMetadata() (ApplicationMetadataCatalog, error) {
	return DecodeApplicationMetadata(pinnedKitMetadataJSON)
}

// DecodeApplicationMetadata reads the generated, checksum-bound SDK projection.
// Earlier pin projections omit this optional presentation section entirely.
func DecodeApplicationMetadata(data []byte) (ApplicationMetadataCatalog, error) {
	document, err := decodePinnedMetadata(data)
	if err != nil {
		return ApplicationMetadataCatalog{}, err
	}
	result := ApplicationMetadataCatalog{Source: document.Source}
	presentation := document.ApplicationMetadata
	if presentation == nil {
		return result, nil
	}
	sourceDigest, err := hex.DecodeString(presentation.SourceSHA256)
	if err != nil || len(sourceDigest) != sha256.Size || presentation.Path != "internal/registry/data/registry_snapshot.json" {
		return result, fmt.Errorf("application metadata has invalid SDK source provenance")
	}
	digest := sha256.Sum256([]byte(presentation.JSON))
	if hex.EncodeToString(digest[:]) != presentation.SHA256 {
		return result, fmt.Errorf("application metadata digest mismatch")
	}
	if err := json.Unmarshal([]byte(presentation.JSON), &result.Applications); err != nil {
		return result, fmt.Errorf("decode application metadata: %w", err)
	}
	seen := map[string]bool{}
	for _, entry := range result.Applications {
		if entry.ID == "" || seen[entry.ID] {
			return result, fmt.Errorf("application metadata has empty or duplicate tool identity")
		}
		seen[entry.ID] = true
	}
	return result, nil
}
