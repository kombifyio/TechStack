package specv2

import (
	"slices"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

const (
	privateAIUseCase     = "ai"
	privateAIAlternative = "private-ai"
)

// privateAIWorkloadSettings are the ai use-case settings the Private AI
// workload reads at Apply: the model size and the model packs Ollama pulls.
// The kombify-connector setting stays with the node's own setup command,
// which holds the connector token.
var privateAIWorkloadSettings = []string{"model-size", "model-pack-general", "model-pack-coding", "model-pack-vision"}

// proposePrivateAISettings writes the operator's model choices, or the
// release defaults, onto a newly authored Private AI workload. Only settings
// the pinned release installs are written, so a release that merely records
// them never sees a setting its StackSpec contract does not allow.
func proposePrivateAISettings(intent WizardIntent, catalog stackkitrelease.UseCaseCatalog, authored *GoalAuthoring) {
	entry, ok := authored.Workloads[privateAIUseCase].(map[string]any)
	if !ok || entry["alternative"] != privateAIAlternative {
		return
	}
	values := intent.UseCaseSettings[privateAIUseCase]
	// Models run on the node only for local inference; the Wizard hides the
	// packs otherwise.
	if location, set := values["inference-location"]; set && location != "local" {
		return
	}
	useCase, ok := catalog.FindUseCase(privateAIUseCase)
	if !ok {
		return
	}
	settings := map[string]any{}
	if authoredSettings, ok := entry["settings"].(map[string]any); ok {
		for key, value := range authoredSettings {
			settings[key] = value
		}
	}
	for _, setting := range useCase.Settings {
		if setting.Realization != "install" || !slices.Contains(privateAIWorkloadSettings, setting.ID) {
			continue
		}
		value, set := values[setting.ID]
		if !set {
			value = setting.Default
		}
		if value != nil {
			settings[setting.ID] = value
		}
	}
	if len(settings) == 0 {
		return
	}
	proposed := make(map[string]any, len(entry)+1)
	for key, value := range entry {
		proposed[key] = value
	}
	proposed["settings"] = settings
	authored.Workloads[privateAIUseCase] = proposed
}
