package stackkitrelease

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/guardrelease"
)

const previewUseCaseCatalogFile = "stackkits-preview-use-case-catalog-v1.json"

// The signed Guard bundle is the custody boundary for this reader. Its exact
// index digest is independently pinned in the signed candidate provenance;
// the producer verified each payload attestation before signing that bundle.
func decodeCandidateUseCaseCatalog(raw, indexRaw []byte, pin Pin) (UseCaseCatalog, error) {
	refuse := func() (UseCaseCatalog, error) {
		return UseCaseCatalog{}, fmt.Errorf("candidate catalog differs from its admitted source/index")
	}
	if pin.Candidate == nil || len(raw) == 0 || len(raw) > maxUseCaseCatalog || len(indexRaw) == 0 || len(indexRaw) > 1<<20 {
		return refuse()
	}
	digest := func(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
	if digest(indexRaw) != pin.Candidate.IndexSHA256 {
		return refuse()
	}
	var index struct {
		Assets []struct {
			Name   string `json:"name"`
			SHA256 string `json:"sha256"`
		} `json:"assets"`
	}
	if json.Unmarshal(indexRaw, &index) != nil {
		return refuse()
	}
	count := 0
	for _, asset := range index.Assets {
		if asset.Name == previewUseCaseCatalogFile {
			count++
			if asset.SHA256 != digest(raw) {
				return refuse()
			}
		}
	}
	if count != 1 {
		return refuse()
	}
	var document struct {
		SchemaVersion   string                                `json:"schemaVersion"`
		DeclaredVersion string                                `json:"declaredVersion"`
		Source          guardrelease.PreviewCandidateSource   `json:"source"`
		Producer        guardrelease.PreviewCandidateProducer `json:"producer"`
		Catalog         json.RawMessage                       `json:"catalog"`
	}
	if json.Unmarshal(raw, &document) != nil || document.SchemaVersion != "stackkits.preview-use-case-catalog/v1" || document.DeclaredVersion != strings.TrimPrefix(pin.Version, "v") || document.Source != pin.Candidate.Source || document.Producer != pin.Candidate.Producer {
		return refuse()
	}
	// Project through the same canonical component/settings decoder. No public
	// Release/tag/URL is invented for these private candidate facts.
	var projected useCaseCatalogDocument
	if json.Unmarshal(document.Catalog, &projected.Catalog) != nil {
		return refuse()
	}
	result, err := projectUseCaseCatalog(projected)
	if err != nil {
		return UseCaseCatalog{}, err
	}
	candidate := *pin.Candidate
	result.Candidate = &candidate
	// Descriptive version equality is still required by existing authoring
	// dispatch. Tag, public-source identity and release URL remain absent.
	result.Release.Version = pin.Version
	return result, nil
}

func (release Release) CandidateUseCaseCatalog() (UseCaseCatalog, error) {
	if release.receipt.Candidate == nil || release.useCaseCatalog == nil {
		return UseCaseCatalog{}, fmt.Errorf("release has no admitted candidate use-case catalog")
	}
	// Detach returned collections from the immutable admitted release.
	raw, err := json.Marshal(release.useCaseCatalog)
	if err != nil {
		return UseCaseCatalog{}, err
	}
	var result UseCaseCatalog
	err = json.Unmarshal(raw, &result)
	return result, err
}
