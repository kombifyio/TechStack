package stackkitrelease

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
)

// AgentSurfaceCatalog retains the producer's declared non-secret surface facts.
// Techstack neither renders node client configurations nor grants availability.
type AgentSurfaceCatalog struct {
	Source   SDKMetadataSource
	Surfaces map[string]json.RawMessage
}

var pinnedAgentSurfaces = sync.OnceValues(func() (AgentSurfaceCatalog, error) {
	return DecodeAgentSurfaces(pinnedKitMetadataJSON)
})

func DecodeAgentSurfaces(data []byte) (AgentSurfaceCatalog, error) {
	document, err := decodePinnedMetadata(data)
	if err != nil {
		return AgentSurfaceCatalog{}, err
	}
	result := AgentSurfaceCatalog{Source: document.Source}
	if document.AgentSurfaces == nil {
		return result, nil
	}
	entry := document.AgentSurfaces
	digest := sha256.Sum256([]byte(entry.JSON))
	if entry.Path != "internal/agentsurface/surfaces.json" || hex.EncodeToString(digest[:]) != entry.SHA256 {
		return AgentSurfaceCatalog{}, fmt.Errorf("pinned agent-surface source or digest mismatch")
	}
	var catalog struct {
		SchemaVersion string                     `json:"schemaVersion"`
		Surfaces      map[string]json.RawMessage `json:"surfaces"`
	}
	if err := json.Unmarshal([]byte(entry.JSON), &catalog); err != nil {
		return AgentSurfaceCatalog{}, fmt.Errorf("decode published agent surfaces: %w", err)
	}
	if catalog.SchemaVersion != "stackkit.agent-surface/v1" || catalog.Surfaces == nil {
		return AgentSurfaceCatalog{}, fmt.Errorf("unsupported published agent-surface catalog")
	}
	for ref, surface := range catalog.Surfaces {
		var identity struct {
			Ref string `json:"ref"`
		}
		if err := json.Unmarshal(surface, &identity); err != nil || ref == "" || identity.Ref != ref {
			return AgentSurfaceCatalog{}, fmt.Errorf("published agent-surface identity differs from its catalog key")
		}
	}
	result.Surfaces = catalog.Surfaces
	return result, nil
}
