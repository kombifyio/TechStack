package appcatalog

import (
	"log/slog"
	"os"
	"strings"
	"sync"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

var pinnedApplicationCatalog = sync.OnceValues(stackkitrelease.PinnedUseCaseCatalog)
var pinnedApplicationMetadata = sync.OnceValues(func() (stackkitrelease.ApplicationMetadataCatalog, error) {
	metadata, err := stackkitrelease.PinnedApplicationMetadata()
	if err != nil {
		slog.Error("published SDK application metadata is invalid", "error", err)
	}
	return metadata, err
})

var pinnedApplications = sync.OnceValue(func() map[string]Entry {
	catalog, err := pinnedApplicationCatalog()
	if err != nil {
		return nil
	}
	return applicationsFromRelease(catalog)
})

// Runtime recognition retains image/name hints for custom installations. The
// exact released application ID joins presentation facts from the same catalog
// the wizard reads; recognition never grants installation availability.
func releasedCatalogEntry(id string) *Entry {
	applications := pinnedApplications()
	if strings.TrimSpace(os.Getenv(stackkitrelease.UseCaseCatalogEnv)) != "" {
		catalog, _, err := stackkitrelease.UseCaseCatalogFromEnv()
		if err != nil {
			return nil
		}
		for _, useCase := range catalog.UseCases {
			for _, component := range useCase.Components {
				if component.Kind == "application" && component.ID == id {
					entry := &Entry{ID: id, DisplayName: component.Name, Category: useCase.ID, IconURL: IconURL(id)}
					attachReleasedApplicationPresentation(entry)
					return entry
				}
			}
		}
		return nil
	}
	entry, ok := applications[id]
	if !ok {
		return nil
	}
	attachReleasedApplicationPresentation(&entry)
	return &entry
}

// Display links follow the same public source as the use-case catalog. A
// development catalog override cannot borrow another pin's client/vendor data.
func attachReleasedApplicationPresentation(entry *Entry) {
	entry.GuideURL = ""
	entry.Clients = nil
	entry.Vendor = nil
	metadata, err := pinnedApplicationMetadata()
	if err != nil {
		return
	}
	catalog, err := pinnedApplicationCatalog()
	if strings.TrimSpace(os.Getenv(stackkitrelease.UseCaseCatalogEnv)) != "" {
		catalog, _, err = stackkitrelease.UseCaseCatalogFromEnv()
	}
	if err != nil || !metadata.Source.MatchesCatalog(catalog.Release) {
		return
	}
	for _, presentation := range metadata.Applications {
		if presentation.ID != entry.ID {
			continue
		}
		entry.GuideURL = presentation.GuideURL
		entry.Clients = append([]stackkitrelease.ApplicationClient(nil), presentation.Clients...)
		if presentation.Vendor != nil {
			vendor := *presentation.Vendor
			vendor.Support = append([]stackkitrelease.ApplicationSupportLink(nil), presentation.Vendor.Support...)
			entry.Vendor = &vendor
		}
		return
	}
}

func applicationsFromRelease(catalog stackkitrelease.UseCaseCatalog) map[string]Entry {
	applications := map[string]Entry{}
	for _, useCase := range catalog.UseCases {
		for _, component := range useCase.Components {
			if component.Kind != "application" {
				continue
			}
			if _, present := applications[component.ID]; present {
				continue
			}
			applications[component.ID] = Entry{
				ID: component.ID, DisplayName: component.Name,
				Category: useCase.ID, IconURL: IconURL(component.ID),
			}
		}
	}
	return applications
}
