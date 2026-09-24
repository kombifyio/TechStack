package unifier

import (
	"sort"
	"strings"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

// UseCaseFit is the StackKits package compute-tier surface Unifier reads.
type UseCaseFit struct {
	Included   bool
	ModuleSlug string
	Reason     string
	Residency  string
	Baseline   string
	Burst      string
}

func OmittedOnTier(selected []string, fits map[string]map[string]UseCaseFit, tier string) []string {
	tier = strings.TrimSpace(tier)
	if tier == "" {
		tier = "standard"
	}
	var omitted []string
	seen := map[string]bool{}
	for _, id := range selected {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		fit, ok := fits[id][tier]
		if ok && !fit.Included {
			omitted = append(omitted, id)
		}
	}
	sort.Strings(omitted)
	return omitted
}

func CountAlwaysOnActive(selected []string, fits map[string]map[string]UseCaseFit, tier string) int {
	tier = strings.TrimSpace(tier)
	if tier == "" {
		tier = "standard"
	}
	count := 0
	seen := map[string]bool{}
	for _, id := range selected {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		fit, ok := fits[id][tier]
		if ok && fit.Included && fit.Residency == "always-on" && fit.Baseline == "active-resident" {
			count++
		}
	}
	return count
}

func selectedUseCaseRefs(metadata map[string]string, serviceNames []string) []string {
	var refs []string
	if raw := strings.TrimSpace(metadata["use_cases"]); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			if id := strings.TrimSpace(part); id != "" {
				refs = append(refs, id)
			}
		}
	}
	for _, name := range serviceNames {
		if id := serviceUseCaseRef(name); id != "" {
			refs = append(refs, id)
		}
	}
	return refs
}

func serviceUseCaseRef(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "photos", "immich":
		return "photos"
	case "media", "jellyfin":
		return "media"
	case "files", "cloudreve":
		return "files"
	case "vault", "vaultwarden":
		return "vault"
	default:
		return ""
	}
}

func loadUseCaseFits() map[string]map[string]UseCaseFit {
	catalog, err := stackkitrelease.ResolveUseCaseCatalog()
	if err != nil {
		return nil
	}
	fits := map[string]map[string]UseCaseFit{}
	for _, useCase := range catalog.UseCases {
		id := strings.TrimSpace(useCase.ID)
		if id == "" || len(useCase.ComputeTiers) == 0 {
			continue
		}
		tierFits := map[string]UseCaseFit{}
		for _, tier := range []string{"low", "standard", "high"} {
			verdict, ok := useCase.ComputeTiers[tier]
			if !ok {
				continue
			}
			fit := UseCaseFit{
				Included:   verdict.Included,
				ModuleSlug: verdict.ModuleSlug,
				Reason:     verdict.Reason,
			}
			if verdict.Load != nil {
				fit.Residency = verdict.Load.Residency
				fit.Baseline = verdict.Load.Baseline
				fit.Burst = verdict.Load.Burst
			}
			tierFits[tier] = fit
		}
		if len(tierFits) > 0 {
			fits[id] = tierFits
		}
	}
	return fits
}
