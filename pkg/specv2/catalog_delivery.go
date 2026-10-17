package specv2

import (
	"fmt"
	"os"
	"strings"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

// CatalogDelivery is the compatibility authority's verdict for one catalog.
// Unknown compatibility makes no delivery claim.
type CatalogDelivery struct {
	Goals    map[string]string
	Statuses map[string]string
	Known    bool
}

// CatalogDeliveryForRelease reads both delivery and status from one manifest,
// bound to the catalog's release. The Wizard and its public preview must not
// offer a catalog using another release's delivery verdicts.
func CatalogDeliveryForRelease(release stackkitrelease.UseCaseCatalogRelease) (CatalogDelivery, error) {
	path := strings.TrimSpace(os.Getenv(CompatibilityManifestEnv))
	if path == "" {
		return CatalogDelivery{}, nil
	}
	manifest, err := readCompatibilityManifest(path)
	if err != nil {
		return CatalogDelivery{}, err
	}
	version := strings.TrimPrefix(strings.TrimSpace(release.Version), "v")
	if version == "" || release.Tag != "v"+version {
		return CatalogDelivery{}, fmt.Errorf("specv2: StackKits catalog has inconsistent release identity")
	}
	goals, _, err := goalMetadata(manifest, version)
	if err != nil {
		return CatalogDelivery{}, err
	}
	if (release.SourceSHA != "" && release.SourceSHA != manifest.Release.SourceSHA) ||
		(release.PublicSourceSHA != "" && release.PublicSourceSHA != manifest.Release.PublicSourceSHA) {
		return CatalogDelivery{}, fmt.Errorf("specv2: StackKits catalog and compatibility manifest have different source revisions")
	}
	return CatalogDelivery{Goals: goals, Statuses: goalDeliveryStatuses(manifest), Known: true}, nil
}
