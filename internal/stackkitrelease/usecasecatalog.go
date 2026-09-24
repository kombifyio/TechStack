package stackkitrelease

// The StackKits use-case catalog published beside the exact release artifact.
// StackKits owns the CUE authority (foundation/use_case_catalog.cue); this
// reads its release projection and nothing else. Techstack must never grow a
// second, hand-written map of what a use case is built from - the existing
// goalServiceMap is a payload service-flag map and is not that.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed pinned-use-case-catalog.json
var pinnedUseCaseCatalogJSON []byte

// UseCaseCatalogEnv points at the use-case catalog published beside the exact
// StackKits release artifact, alongside the compatibility manifest.
const UseCaseCatalogEnv = "TECHSTACK_STACKKIT_USE_CASE_CATALOG"

const (
	useCaseCatalogSchema = "stackkits-use-case-catalog/v1"
	maxUseCaseCatalog    = 1 << 20
	useCaseCatalogTTL    = 5 * time.Minute
)

// UseCaseComponent is one product a use case is built from. The role vocabulary
// is StackKits' (#UseCaseCatalogComponent.role) and is passed through verbatim;
// Techstack does not reinterpret or collapse it.
type UseCaseComponent struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
	Kind string `json:"kind"`
}

// UseCaseLoad is StackKits' declared residency/baseline/burst for one tier.
type UseCaseLoad struct {
	Residency string `json:"residency,omitempty"`
	Baseline  string `json:"baseline,omitempty"`
	Burst     string `json:"burst,omitempty"`
}

// UseCaseComputeTier is StackKits' verdict on one compute tier for one use
// case: whether the pinned release includes it there, and its own reason when
// it does not. The reason is engineering prose, not customer copy - surfaces
// show it at the technical depth level, not on the compact card.
type UseCaseComputeTier struct {
	Included   bool         `json:"included"`
	Reason     string       `json:"reason,omitempty"`
	ModuleSlug string       `json:"moduleSlug,omitempty"`
	Load       *UseCaseLoad `json:"load,omitempty"`
}

// UseCaseSettingOption is one choice of a choice-kind setting.
type UseCaseSettingOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Note string `json:"note,omitempty"`
}

// UseCaseSetting is a decision an operator makes about a use case before it
// is installed. StackKits' #UseCaseSetting declarations are decoded verbatim;
// ConfigurationSettings can add recorded preferences derived from other
// release-catalog facts. Default is a string for choice and text settings and
// a bool for toggles.
// Realization says whether the pinned release applies the decision at install
// ("install") or only records it against the homelab for a later release
// ("recorded"); a surface shows a recorded setting as such and never hides it.
type UseCaseSetting struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	Kind        string                 `json:"kind"`
	Group       string                 `json:"group"`
	Depth       string                 `json:"depth"`
	Help        string                 `json:"help,omitempty"`
	Options     []UseCaseSettingOption `json:"options,omitempty"`
	Default     any                    `json:"default"`
	Placeholder string                 `json:"placeholder,omitempty"`
	Realization string                 `json:"realization"`
}

// UseCase is one catalog entry. The ID is the use-case slug the Wizard already
// speaks, so no translation table is needed on either side.
type UseCase struct {
	ID           string                        `json:"id"`
	Title        string                        `json:"title"`
	Description  string                        `json:"description"`
	Components   []UseCaseComponent            `json:"components"`
	ComputeTiers map[string]UseCaseComputeTier `json:"computeTiers,omitempty"`
	Settings     []UseCaseSetting              `json:"settings,omitempty"`
	// Docs is the path of the use case's guide on docs.kombify.io, when the
	// catalog names one. Empty means "no guide yet", never "guess one".
	Docs string `json:"docs,omitempty"`
}

// AcceptsSettingValue reports whether value is a legal value for setting:
// one of the declared option ids for a choice, a bool for a toggle, a string
// for text. The intent contract is closed, so anything else is rejected.
func (setting UseCaseSetting) AcceptsSettingValue(value any) bool {
	switch setting.Kind {
	case "choice":
		id, ok := value.(string)
		if !ok {
			return false
		}
		for _, option := range setting.Options {
			if option.ID == id {
				return true
			}
		}
		return false
	case "toggle":
		_, ok := value.(bool)
		return ok
	case "text":
		text, ok := value.(string)
		return ok && len(text) <= 253
	default:
		return false
	}
}

// FindUseCase returns the catalog entry for a use-case slug.
func (catalog UseCaseCatalog) FindUseCase(id string) (UseCase, bool) {
	for _, useCase := range catalog.UseCases {
		if useCase.ID == id {
			return useCase, true
		}
	}
	return UseCase{}, false
}

// FindSetting returns a configuration setting of a use case.
func (useCase UseCase) FindSetting(id string) (UseCaseSetting, bool) {
	for _, setting := range useCase.ConfigurationSettings() {
		if setting.ID == id {
			return setting, true
		}
	}
	return UseCaseSetting{}, false
}

// ConfigurationSettings projects the release catalog facts that Techstack can
// safely record as Wizard preferences. StackKits' declared settings remain
// authoritative and win on an id collision. Service choices come only from
// primary/alternative components, and compute profiles only from included
// tiers; both are recorded preferences until StackKits declares how to apply
// them during installation.
func (useCase UseCase) ConfigurationSettings() []UseCaseSetting {
	settings := append([]UseCaseSetting(nil), useCase.Settings...)
	declared := make(map[string]struct{}, len(settings))
	for _, setting := range settings {
		declared[setting.ID] = struct{}{}
	}

	if _, ok := declared["backend"]; !ok {
		options := make([]UseCaseSettingOption, 0, len(useCase.Components))
		defaultID := ""
		for _, component := range useCase.Components {
			if component.Role != "primary" && component.Role != "alternative" {
				continue
			}
			options = append(options, UseCaseSettingOption{ID: component.ID, Name: component.Name})
			if defaultID == "" || component.Role == "primary" {
				defaultID = component.ID
			}
		}
		if len(options) > 0 {
			settings = append(settings, UseCaseSetting{
				ID:          "backend",
				Name:        "Service",
				Kind:        "choice",
				Group:       "backend",
				Depth:       "summary",
				Help:        "Preferred service from this StackKits release.",
				Options:     options,
				Default:     defaultID,
				Realization: "recorded",
			})
		}
	}

	if _, ok := declared["profile"]; !ok {
		const standardTier = "standard"
		tierNames := map[string]string{"low": "Low", standardTier: "Standard", "high": "High"}
		options := make([]UseCaseSettingOption, 0, len(tierNames))
		defaultID := ""
		for _, tier := range []string{"low", standardTier, "high"} {
			verdict, exists := useCase.ComputeTiers[tier]
			if !exists || !verdict.Included {
				continue
			}
			options = append(options, UseCaseSettingOption{ID: tier, Name: tierNames[tier]})
			if defaultID == "" || tier == standardTier {
				defaultID = tier
			}
		}
		if len(options) > 0 {
			settings = append(settings, UseCaseSetting{
				ID:          "profile",
				Name:        "Compute profile",
				Kind:        "choice",
				Group:       "profile",
				Depth:       "summary",
				Help:        "Preferred compute tier included by this StackKits release.",
				Options:     options,
				Default:     defaultID,
				Realization: "recorded",
			})
		}
	}

	return settings
}

// UseCaseCatalogRelease carries the provenance of the projection so a caller
// can show, and a reader can check, which release these components came from.
type UseCaseCatalogRelease struct {
	Tag     string `json:"tag"`
	Version string `json:"version"`
}

// UseCaseCatalog is the decoded release projection.
type UseCaseCatalog struct {
	Release  UseCaseCatalogRelease `json:"release"`
	UseCases []UseCase             `json:"useCases"`
}

type useCaseCatalogDocument struct {
	SchemaVersion string                `json:"schemaVersion"`
	Release       UseCaseCatalogRelease `json:"release"`
	Catalog       struct {
		UseCases []UseCase `json:"useCases"`
	} `json:"catalog"`
}

var useCaseCatalogCache struct {
	sync.Mutex
	loadedAt time.Time
	path     string
	catalog  UseCaseCatalog
	err      error
}

// UseCaseCatalogFromEnv reads the configured catalog, memoized briefly so a
// page of cards does not re-read the file per request. A deployment without
// the catalog configured is not an error to the caller: it returns false so
// the surface can render without a component strip rather than fail.
func UseCaseCatalogFromEnv() (UseCaseCatalog, bool, error) {
	path := strings.TrimSpace(os.Getenv(UseCaseCatalogEnv))
	if path == "" {
		return UseCaseCatalog{}, false, nil
	}

	useCaseCatalogCache.Lock()
	defer useCaseCatalogCache.Unlock()
	fresh := useCaseCatalogCache.path == path &&
		!useCaseCatalogCache.loadedAt.IsZero() &&
		time.Since(useCaseCatalogCache.loadedAt) < useCaseCatalogTTL
	if fresh {
		if useCaseCatalogCache.err != nil {
			return UseCaseCatalog{}, true, useCaseCatalogCache.err
		}
		return useCaseCatalogCache.catalog, true, nil
	}

	catalog, err := ReadUseCaseCatalog(path)
	useCaseCatalogCache.path = path
	useCaseCatalogCache.loadedAt = time.Now()
	useCaseCatalogCache.catalog = catalog
	useCaseCatalogCache.err = err
	if err != nil {
		return UseCaseCatalog{}, true, err
	}
	return catalog, true, nil
}

// PinnedUseCaseCatalog returns the catalog generated from the StackKits
// module pin (currently v0.24.83). It does not read a source checkout.
func PinnedUseCaseCatalog() (UseCaseCatalog, error) {
	return DecodeUseCaseCatalog(pinnedUseCaseCatalogJSON)
}

// ResolveUseCaseCatalog prefers an image-published catalog path when set,
// otherwise the embedded pin. Unifier compute-tier decisions use this so a
// missing STACKKITS_REPO checkout cannot silently omit catalog facts.
func ResolveUseCaseCatalog() (UseCaseCatalog, error) {
	if strings.TrimSpace(os.Getenv(UseCaseCatalogEnv)) != "" {
		catalog, _, err := UseCaseCatalogFromEnv()
		return catalog, err
	}
	return PinnedUseCaseCatalog()
}

// ReadUseCaseCatalog decodes the catalog at path. Bounded regular-file read
// with a schema check, the same shape as the compatibility manifest reader:
// this file arrives from the image build, so it is trusted input read
// defensively rather than parsed optimistically.
func ReadUseCaseCatalog(path string) (UseCaseCatalog, error) {
	if strings.TrimSpace(path) == "" {
		return UseCaseCatalog{}, fmt.Errorf("stackkitrelease: %s is required to read the use-case catalog", UseCaseCatalogEnv)
	}
	catalogPath := filepath.Clean(path)
	info, err := os.Lstat(catalogPath) // #nosec G304 -- image configuration selects the immutable catalog.
	if err != nil {
		return UseCaseCatalog{}, fmt.Errorf("stackkitrelease: inspect pinned StackKits use-case catalog: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maxUseCaseCatalog {
		return UseCaseCatalog{}, fmt.Errorf("stackkitrelease: pinned StackKits use-case catalog must be a bounded regular non-symlink file")
	}
	file, err := os.Open(catalogPath) // #nosec G304 -- path passed the bounded regular-file check above.
	if err != nil {
		return UseCaseCatalog{}, fmt.Errorf("stackkitrelease: open pinned StackKits use-case catalog: %w", err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, info.Size()+1))
	if err != nil {
		return UseCaseCatalog{}, fmt.Errorf("stackkitrelease: read pinned StackKits use-case catalog: %w", err)
	}
	if int64(len(data)) != info.Size() {
		return UseCaseCatalog{}, fmt.Errorf("stackkitrelease: pinned StackKits use-case catalog changed while it was read")
	}

	catalog, err := DecodeUseCaseCatalog(data)
	if err != nil {
		return UseCaseCatalog{}, err
	}
	return catalog, nil
}

// DecodeUseCaseCatalog parses a stackkits-use-case-catalog/v1 document from
// bytes. This is the Unifier's public catalog entry; it never loads CUE.
func DecodeUseCaseCatalog(data []byte) (UseCaseCatalog, error) {
	var document useCaseCatalogDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return UseCaseCatalog{}, fmt.Errorf("stackkitrelease: decode pinned StackKits use-case catalog: %w", err)
	}
	if document.SchemaVersion != useCaseCatalogSchema {
		return UseCaseCatalog{}, fmt.Errorf("stackkitrelease: unsupported StackKits use-case catalog schema %q", document.SchemaVersion)
	}

	catalog := UseCaseCatalog{Release: document.Release}
	for _, useCase := range document.Catalog.UseCases {
		id := strings.TrimSpace(useCase.ID)
		if id == "" {
			continue
		}
		entry := UseCase{
			ID:          id,
			Title:       strings.TrimSpace(useCase.Title),
			Description: strings.TrimSpace(useCase.Description),
		}
		for tier, verdict := range useCase.ComputeTiers {
			tier = strings.TrimSpace(tier)
			if tier == "" {
				continue
			}
			if entry.ComputeTiers == nil {
				entry.ComputeTiers = map[string]UseCaseComputeTier{}
			}
			copied := UseCaseComputeTier{
				Included:   verdict.Included,
				Reason:     strings.TrimSpace(verdict.Reason),
				ModuleSlug: strings.TrimSpace(verdict.ModuleSlug),
			}
			if verdict.Load != nil {
				copied.Load = &UseCaseLoad{
					Residency: strings.TrimSpace(verdict.Load.Residency),
					Baseline:  strings.TrimSpace(verdict.Load.Baseline),
					Burst:     strings.TrimSpace(verdict.Load.Burst),
				}
			}
			entry.ComputeTiers[tier] = copied
		}
		for _, component := range useCase.Components {
			componentID := strings.TrimSpace(component.ID)
			if componentID == "" {
				continue
			}
			entry.Components = append(entry.Components, UseCaseComponent{
				ID:   componentID,
				Name: strings.TrimSpace(component.Name),
				Role: strings.TrimSpace(component.Role),
				Kind: strings.TrimSpace(component.Kind),
			})
		}
		for _, setting := range useCase.Settings {
			if strings.TrimSpace(setting.ID) == "" {
				continue
			}
			entry.Settings = append(entry.Settings, setting)
		}
		entry.Docs = strings.TrimSpace(useCase.Docs)
		catalog.UseCases = append(catalog.UseCases, entry)
	}
	return catalog, nil
}
