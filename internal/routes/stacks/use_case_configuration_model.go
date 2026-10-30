package stacks

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

type configurationProfileView struct {
	Current string   `json:"current,omitempty"`
	Options []string `json:"options"`
}
type configurationSettingView struct {
	Key     string `json:"key"`
	Type    string `json:"type"`
	Label   string `json:"label"`
	Current any    `json:"current,omitempty"`
	Options []any  `json:"options,omitempty"`
}
type configurationSecretView struct {
	Key     string   `json:"key"`
	Current string   `json:"current,omitempty"`
	Options []string `json:"options"`
}
type configurationWorkloadView struct {
	WorkloadRef string `json:"workload_ref"`
	Alternative string `json:"alternative"`
	ModuleRef   string `json:"module_ref"`
	Profiles    struct {
		Compute     configurationProfileView `json:"compute"`
		Storage     configurationProfileView `json:"storage"`
		Accelerator configurationProfileView `json:"accelerator"`
	} `json:"profiles"`
	Settings        []configurationSettingView `json:"settings"`
	SecretRefs      []configurationSecretView  `json:"secret_refs"`
	RemovalEligible bool                       `json:"removal_eligible"`
	Eligible        bool                       `json:"eligible"`
	ReasonCode      string                     `json:"reason_code,omitempty"`
}
type configurationUseCaseView struct {
	ID        string                      `json:"id"`
	Name      string                      `json:"name"`
	Workloads []configurationWorkloadView `json:"workloads"`
}
type useCaseConfigurationView struct {
	RetainedRemovalAvailable bool                       `json:"retained_removal_available"`
	KitDeploymentID          string                     `json:"kit_deployment_id"`
	ExpectedSpecHash         string                     `json:"expected_spec_hash,omitempty"`
	ReleaseVersion           string                     `json:"release_version"`
	UseCases                 []configurationUseCaseView `json:"use_cases"`
}

type configuredCatalogWorkload struct {
	useCaseID, title, workload string
	alternative                stackkitrelease.UseCaseAlternative
}

// Membership is a released native workloadRef/addOn ID and selected native
// alternative. Component names and Wizard settings never become authoring keys.
func configuredCatalogWorkloads(catalog stackkitrelease.UseCaseCatalog, spec map[string]any) []configuredCatalogWorkload {
	selected, _ := spec["workloads"].(map[string]any)
	var result []configuredCatalogWorkload
	for _, uc := range catalog.UseCases {
		candidates := []struct {
			id           string
			alternatives []stackkitrelease.UseCaseAlternative
		}{{uc.WorkloadRef, uc.Alternatives}}
		for _, addon := range uc.AddOns {
			candidates = append(candidates, struct {
				id           string
				alternatives []stackkitrelease.UseCaseAlternative
			}{addon.ID, addon.Alternatives})
		}
		for _, candidate := range candidates {
			if candidate.id == "" {
				continue
			}
			entry, ok := selected[candidate.id].(map[string]any)
			if !ok {
				continue
			}
			alternativeID, _ := entry["alternative"].(string)
			for _, alternative := range candidate.alternatives {
				if alternative.ID == alternativeID {
					result = append(result, configuredCatalogWorkload{uc.ID, uc.Title, candidate.id, alternative})
				}
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].useCaseID == result[j].useCaseID {
			return result[i].workload < result[j].workload
		}
		return result[i].useCaseID < result[j].useCaseID
	})
	return result
}

func configurationView(catalog stackkitrelease.UseCaseCatalog, stackID string, spec map[string]any, reason string) useCaseConfigurationView {
	view := useCaseConfigurationView{KitDeploymentID: stackID, ReleaseVersion: catalog.Release.Version, UseCases: []configurationUseCaseView{}, RetainedRemovalAvailable: catalog.RetainedRemovalCandidates}
	workloads, _ := spec["workloads"].(map[string]any)
	modules, _ := spec["modules"].(map[string]any)
	for _, selected := range configuredCatalogWorkloads(catalog, spec) {
		entry, _ := workloads[selected.workload].(map[string]any)
		item := configurationWorkloadView{WorkloadRef: selected.workload, Alternative: selected.alternative.ID, Settings: []configurationSettingView{}, SecretRefs: []configurationSecretView{}, ReasonCode: reason}
		item.Profiles.Compute.Options = []string{}
		item.Profiles.Storage.Options = []string{}
		item.Profiles.Accelerator.Options = []string{}
		if len(selected.alternative.Modules) != 1 {
			item.ReasonCode = "native_configuration_module_unavailable"
		} else {
			module := selected.alternative.Modules[0]
			item.ModuleRef = module.ID
			current, _ := modules[module.ID].(map[string]any)
			item.Profiles.Compute.Current, _ = current["computeProfile"].(string)
			item.Profiles.Storage.Current, _ = current["storageProfile"].(string)
			item.Profiles.Accelerator.Current, _ = current["acceleratorProfile"].(string)
			for _, profile := range module.ComputeProfiles {
				if facts, ok := module.ComputeProfileFacts[profile]; ok && facts.Executable {
					item.Profiles.Compute.Options = append(item.Profiles.Compute.Options, profile)
				}
			}
			item.Profiles.Storage.Options = append(item.Profiles.Storage.Options, module.StorageProfiles...)
			item.Profiles.Accelerator.Options = append(item.Profiles.Accelerator.Options, module.AcceleratorProfiles...)
		}
		item.RemovalEligible = reason == "" && len(selected.alternative.Modules) == 1
		if descriptor := selected.alternative.Configuration; descriptor != nil {
			settings, _ := entry["settings"].(map[string]any)
			refs, _ := entry["secretRefs"].(map[string]any)
			for _, setting := range descriptor.Settings {
				if setting.Type != "string" && setting.Type != "number" && setting.Type != "boolean" {
					continue
				}
				item.Settings = append(item.Settings, configurationSettingView{Key: setting.Key, Type: setting.Type, Label: setting.Label, Current: settings[setting.Key], Options: setting.Options})
			}
			for _, key := range descriptor.SecretRefs {
				current, _ := refs[key].(string)
				options := []string{}
				if current != "" {
					options = append(options, current)
				}
				item.SecretRefs = append(item.SecretRefs, configurationSecretView{Key: key, Current: current, Options: options})
			}
		} else if item.ReasonCode == "" {
			item.ReasonCode = "native_configuration_descriptors_unavailable"
		}
		item.Eligible = item.ReasonCode == ""
		if len(view.UseCases) == 0 || view.UseCases[len(view.UseCases)-1].ID != selected.useCaseID {
			view.UseCases = append(view.UseCases, configurationUseCaseView{ID: selected.useCaseID, Name: selected.title, Workloads: []configurationWorkloadView{}})
		}
		group := &view.UseCases[len(view.UseCases)-1]
		group.Workloads = append(group.Workloads, item)
	}
	return view
}

func configurationSelection(view useCaseConfigurationView, workload string) (configurationWorkloadView, bool) {
	for _, uc := range view.UseCases {
		for _, entry := range uc.Workloads {
			if entry.WorkloadRef == workload {
				return entry, true
			}
		}
	}
	return configurationWorkloadView{}, false
}

func validateCatalogConfiguration(configuration stackkitcommand.UseCaseConfiguration, selection configurationWorkloadView) error {
	if !selection.Eligible {
		return fmt.Errorf("%s", selection.ReasonCode)
	}
	for _, profile := range []struct {
		requested string
		options   []string
	}{{configuration.ComputeProfile, selection.Profiles.Compute.Options}, {configuration.StorageProfile, selection.Profiles.Storage.Options}, {configuration.AcceleratorProfile, selection.Profiles.Accelerator.Options}} {
		if profile.requested == "" {
			continue
		}
		found := false
		for _, option := range profile.options {
			found = found || option == profile.requested
		}
		if !found {
			return fmt.Errorf("module profile is not offered by the native catalog")
		}
	}
	for key, raw := range configuration.Settings {
		var value any
		if json.Unmarshal(raw, &value) != nil {
			return fmt.Errorf("invalid public setting")
		}
		found := false
		for _, field := range selection.Settings {
			if field.Key != key {
				continue
			}
			found = true
			valid := false
			switch field.Type {
			case "string":
				_, valid = value.(string)
			case "boolean":
				_, valid = value.(bool)
			case "number":
				_, valid = value.(float64)
			}
			if !valid {
				return fmt.Errorf("public setting differs from the native catalog type")
			}
			if len(field.Options) > 0 {
				offered := false
				for _, option := range field.Options {
					offered = offered || reflect.DeepEqual(value, option)
				}
				if !offered {
					return fmt.Errorf("public setting names no native catalog option")
				}
			}
		}
		if !found {
			return fmt.Errorf("public setting has no native Configure descriptor")
		}
	}
	for key, ref := range configuration.SecretRefs {
		found := false
		for _, field := range selection.SecretRefs {
			if field.Key == key {
				for _, option := range field.Options {
					found = found || option == ref
				}
			}
		}
		if !found {
			return fmt.Errorf("secret input must name existing selected custody reference")
		}
	}
	return nil
}
