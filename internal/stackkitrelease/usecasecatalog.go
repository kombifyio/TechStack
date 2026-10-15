package stackkitrelease

// The StackKits use-case catalog published beside the exact release artifact.
// StackKits owns the CUE authority (foundation/use_case_catalog.cue); this
// reads its release projection and nothing else. Techstack must never grow a
// second, hand-written map of what a use case is built from.

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
	// Realization is set on alternative components: "install" when the
	// pinned release can install it, "recorded" when it only keeps the choice.
	Realization string `json:"realization,omitempty"`
	// Maturity is the release's maturity of the application's own
	// Architecture v2 alternative (see ApplicationMaturity); empty when the
	// release has no executable alternative for it.
	Maturity string `json:"maturity,omitempty"`
}

// UseCaseProfileFacts is StackKits' declared fact set of one module compute
// profile. Sizing remains release-owned, including the distinction between a
// supported host floor, the workload's reservation and recommended headroom.
type UseCaseProfileFacts struct {
	Maturity    string                               `json:"maturity,omitempty"`
	Executable  bool                                 `json:"executable"`
	Realization string                               `json:"realization,omitempty"`
	HostFloor   *UseCaseHostFloor                    `json:"hostFloor,omitempty"`
	Reservation *UseCaseResources                    `json:"reservation,omitempty"`
	Recommended *UseCaseResources                    `json:"recommended,omitempty"`
	Provenance  map[string]UseCaseResourceProvenance `json:"provenance,omitempty"`
}

// UseCaseModule is one module an Architecture v2 alternative is built from.
type UseCaseModule struct {
	ID                      string                             `json:"id"`
	ComputeProfiles         []string                           `json:"computeProfiles,omitempty"`
	ComputeProfileFacts     map[string]UseCaseProfileFacts     `json:"computeProfileFacts,omitempty"`
	AcceleratorProfileFacts map[string]UseCaseAcceleratorFacts `json:"acceleratorProfileFacts,omitempty"`
}

// UseCaseAlternative is one Architecture v2 alternative of a workload.
type UseCaseAlternative struct {
	ID      string          `json:"id"`
	Modules []UseCaseModule `json:"modules,omitempty"`
}

// UseCaseAddOn is an optional application workload of a use case. Its ID is
// the workload ID an installing setting names in workloadRef.
type UseCaseAddOn struct {
	ID                 string               `json:"id"`
	DefaultAlternative string               `json:"defaultAlternative"`
	Alternatives       []UseCaseAlternative `json:"alternatives,omitempty"`
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
	// Realization overrides the setting's realization for this option, so a
	// service choice can mix installing and recorded alternatives.
	Realization string `json:"realization,omitempty"`
	// MinMemoryGB is the server memory this option needs to run well.
	MinMemoryGB int `json:"minMemoryGb,omitempty"`
	// NeedsAccelerator marks an option that needs a GPU in practice.
	NeedsAccelerator bool `json:"needsAccelerator,omitempty"`
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
	// WorkloadRef names the application workload an installing toggle adds.
	WorkloadRef string `json:"workloadRef,omitempty"`
}

// UseCaseCapabilityOption is one realization of a capability module: the
// default, or its single alternative. Realization is "install" when the pinned
// release can install it and "recorded" when it only keeps the choice.
type UseCaseCapabilityOption struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Note            string   `json:"note,omitempty"`
	Realization     string   `json:"realization"`
	Pending         string   `json:"pending,omitempty"`
	WorkloadRef     string   `json:"workloadRef,omitempty"`
	AlternativeRef  string   `json:"alternativeRef,omitempty"`
	AlternativeRefs []string `json:"alternativeRefs,omitempty"`
}

// UseCaseCapability is one independently selectable capability module of a
// use case (StackKits #UseCaseCapability). "off" is a legal selection unless
// the capability is Required.
type UseCaseCapability struct {
	ID                  string                   `json:"id"`
	Name                string                   `json:"name"`
	Help                string                   `json:"help,omitempty"`
	Required            bool                     `json:"required"`
	EnabledByDefault    bool                     `json:"enabledByDefault"`
	Requires            []string                 `json:"requires,omitempty"`
	RequiresAccelerator bool                     `json:"requiresAccelerator"`
	Default             UseCaseCapabilityOption  `json:"default"`
	Alternative         *UseCaseCapabilityOption `json:"alternative,omitempty"`
}

// AcceptsSelection reports whether value is a legal selection for the
// capability and, when not, why. "off" is refused for a required capability
// and a recorded option is refused because the release cannot install it.
func (capability UseCaseCapability) AcceptsSelection(value string) error {
	if value == "off" {
		if capability.Required {
			return fmt.Errorf("capability %q is required and cannot be turned off", capability.ID)
		}
		return nil
	}
	options := []UseCaseCapabilityOption{capability.Default}
	if capability.Alternative != nil {
		options = append(options, *capability.Alternative)
	}
	for _, option := range options {
		if option.ID != value {
			continue
		}
		if option.Realization == "recorded" {
			return fmt.Errorf("capability %q option %q is only recorded by this release and cannot be installed", capability.ID, value)
		}
		return nil
	}
	return fmt.Errorf("capability %q declares no option %q", capability.ID, value)
}

// UseCaseBundle groups capability modules of one use case under one
// customer-facing goal (StackKits #UseCaseBundle). It is catalog grouping
// only: turning a bundle on selects its Defaults through the same capability
// selection, so authoring never sees a bundle. A module several bundles need
// appears in each and installs once.
type UseCaseBundle struct {
	ID                    string   `json:"id"`
	Name                  string   `json:"name"`
	Help                  string   `json:"help,omitempty"`
	Examples              []string `json:"examples,omitempty"`
	Capabilities          []string `json:"capabilities"`
	Defaults              []string `json:"defaults"`
	Required              bool     `json:"required"`
	EnabledByDefault      bool     `json:"enabledByDefault"`
	Advanced              bool     `json:"advanced"`
	RecommendsAccelerator bool     `json:"recommendsAccelerator"`
	RequiresSandbox       bool     `json:"requiresSandbox"`
}

// FindCapability returns a capability module of a use case.
func (useCase UseCase) FindCapability(id string) (UseCaseCapability, bool) {
	for _, capability := range useCase.Capabilities {
		if capability.ID == id {
			return capability, true
		}
	}
	return UseCaseCapability{}, false
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
	// Capabilities are the use case's independently selectable modules.
	Capabilities []UseCaseCapability `json:"capabilities,omitempty"`
	// Bundles group the capability modules by customer goal. A module in no
	// bundle stays selectable by id but the guided setup does not offer it.
	Bundles []UseCaseBundle `json:"bundles,omitempty"`
	// Docs is the path of the use case's guide on docs.kombify.io, when the
	// catalog names one. Empty means "no guide yet", never "guess one".
	Docs               string               `json:"docs,omitempty"`
	DefaultAlternative string               `json:"defaultAlternative,omitempty"`
	Alternatives       []UseCaseAlternative `json:"alternatives,omitempty"`
	AddOns             []UseCaseAddOn       `json:"addOns,omitempty"`
	AgentSurface       json.RawMessage      `json:"agentSurface,omitempty"`
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

// maturityRank orders StackKits' module maturity vocabulary from least to
// most mature; an unknown value ranks lowest so it never reads as supported.
var maturityRank = map[string]int{"deprecated": 1, "experimental": 2, "beta": 3, "supported": 4}

// ApplicationMaturity is the maturity the pinned release declares for the
// application whose Architecture v2 alternative has id: the least mature
// executable compute profile of its modules. Runtime acceptance controls tier
// inclusion separately; it does not erase a released application's maturity.
// It is empty when the release has no executable alternative (a recorded choice).
func (useCase UseCase) ApplicationMaturity(id string) string {
	weakest := ""
	for _, alternative := range useCase.Alternatives {
		if alternative.ID != id {
			continue
		}
		for _, module := range alternative.Modules {
			for _, facts := range module.ComputeProfileFacts {
				if !facts.Executable {
					continue
				}
				maturity := strings.TrimSpace(facts.Maturity)
				if maturity == "" {
					continue
				}
				if weakest == "" || maturityRank[maturity] < maturityRank[weakest] {
					weakest = maturity
				}
			}
		}
	}
	return weakest
}

// Maturity is the use case's maturity: that of its default application.
func (useCase UseCase) Maturity() string {
	return useCase.ApplicationMaturity(useCase.DefaultAlternative)
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

// InstallingAlternative returns a non-default alternative the pinned release
// installs: an alternative component marked realization "install" whose
// Architecture v2 alternative is built from exactly one module. Anything else
// stays a recorded preference.
func (useCase UseCase) InstallingAlternative(id string) (UseCaseAlternative, bool) {
	if id == "" || id == useCase.DefaultAlternative {
		return UseCaseAlternative{}, false
	}
	installs := false
	for _, component := range useCase.Components {
		if component.ID == id && component.Role == "alternative" && component.Realization == "install" {
			installs = true
		}
	}
	if !installs {
		return UseCaseAlternative{}, false
	}
	return singleModuleAlternative(useCase.Alternatives, id)
}

// DefaultWorkloadBinding resolves the default alternative and its module for
// a workload the catalog declares, either a use case or an add-on. It is the
// release-owned binding an installing setting's workloadRef selects.
func (catalog UseCaseCatalog) DefaultWorkloadBinding(workloadID string) (UseCaseAlternative, bool) {
	for _, useCase := range catalog.UseCases {
		if useCase.ID == workloadID {
			return singleModuleAlternative(useCase.Alternatives, useCase.DefaultAlternative)
		}
		for _, addOn := range useCase.AddOns {
			if addOn.ID == workloadID {
				return singleModuleAlternative(addOn.Alternatives, addOn.DefaultAlternative)
			}
		}
	}
	return UseCaseAlternative{}, false
}

func singleModuleAlternative(alternatives []UseCaseAlternative, id string) (UseCaseAlternative, bool) {
	for _, alternative := range alternatives {
		if alternative.ID == id && id != "" && len(alternative.Modules) == 1 && alternative.Modules[0].ID != "" {
			return alternative, true
		}
	}
	return UseCaseAlternative{}, false
}

// ConfigurationSettings projects the release catalog facts that Techstack can
// safely record as Wizard preferences. StackKits' declared settings remain
// authoritative and win on an id collision. Service choices come only from
// primary/alternative components; an option installs when the release marks
// its alternative realization "install" and stays recorded otherwise. Compute
// profiles come only from included tiers and remain recorded preferences.
func (useCase UseCase) ConfigurationSettings() []UseCaseSetting {
	settings := append([]UseCaseSetting(nil), useCase.Settings...)
	declared := make(map[string]struct{}, len(settings))
	for _, setting := range settings {
		declared[setting.ID] = struct{}{}
	}

	if _, ok := declared["backend"]; !ok {
		options := make([]UseCaseSettingOption, 0, len(useCase.Components))
		defaultID := ""
		realization := "recorded"
		for _, component := range useCase.Components {
			if component.Role != "primary" && component.Role != "alternative" {
				continue
			}
			option := UseCaseSettingOption{ID: component.ID, Name: component.Name, Realization: "recorded"}
			if component.Role == "primary" {
				option.Realization = "install"
			} else if _, installs := useCase.InstallingAlternative(component.ID); installs {
				// The choice installs once one alternative does; the other
				// options keep their own recorded realization.
				option.Realization, realization = "install", "install"
			}
			options = append(options, option)
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
				Realization: realization,
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
	Tag             string `json:"tag"`
	Version         string `json:"version"`
	SourceSHA       string `json:"sourceSha,omitempty"`
	PublicSourceSHA string `json:"publicSourceSha,omitempty"`
	ReleaseURL      string `json:"releaseUrl,omitempty"`
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

// PinnedUseCaseCatalog returns the embedded copy of the pinned release's
// catalog asset, which the StackKits repin writes beside the Dockerfile pin.
// It does not read a source checkout.
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
				ID:          componentID,
				Name:        strings.TrimSpace(component.Name),
				Role:        strings.TrimSpace(component.Role),
				Kind:        strings.TrimSpace(component.Kind),
				Realization: strings.TrimSpace(component.Realization),
			})
		}
		for _, setting := range useCase.Settings {
			if strings.TrimSpace(setting.ID) == "" {
				continue
			}
			entry.Settings = append(entry.Settings, setting)
		}
		for _, capability := range useCase.Capabilities {
			if strings.TrimSpace(capability.ID) == "" {
				continue
			}
			entry.Capabilities = append(entry.Capabilities, capability)
		}
		for _, bundle := range useCase.Bundles {
			if strings.TrimSpace(bundle.ID) == "" {
				continue
			}
			entry.Bundles = append(entry.Bundles, bundle)
		}
		entry.Docs = strings.TrimSpace(useCase.Docs)
		entry.DefaultAlternative = strings.TrimSpace(useCase.DefaultAlternative)
		entry.Alternatives = useCase.Alternatives
		entry.AddOns = useCase.AddOns
		for index := range entry.Components {
			entry.Components[index].Maturity = entry.ApplicationMaturity(entry.Components[index].ID)
		}
		catalog.UseCases = append(catalog.UseCases, entry)
	}
	agentCatalog, err := pinnedAgentSurfaces()
	if err != nil {
		return UseCaseCatalog{}, err
	}
	if agentCatalog.Source.MatchesCatalog(catalog.Release) {
		for i := range catalog.UseCases {
			catalog.UseCases[i].AgentSurface = append(json.RawMessage(nil), agentCatalog.Surfaces[catalog.UseCases[i].ID]...)
		}
	}
	return catalog, nil
}
