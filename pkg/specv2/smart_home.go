package specv2

import (
	"fmt"
	"github.com/kombifyio/techstack/internal/substrate"
)

func hasSmartHomeSelection(intent WizardIntent) bool {
	s := intent.UseCaseSettings["smart-home"]
	return s["operating-form"] != nil || s["instance-origin"] != nil
}

// proposeSmartHomeSelection changes only a newly authored proposal. Admission
// and the pinned CLI still validate the selected release contract afterwards.
func proposeSmartHomeSelection(seed map[string]any, intent WizardIntent, authored *GoalAuthoring) error {
	if !hasSmartHomeSelection(intent) {
		return nil
	}
	s := intent.UseCaseSettings["smart-home"]
	mode, origin := "container", "new"
	if value, exists := s["operating-form"]; exists {
		var ok bool
		mode, ok = value.(string)
		if !ok {
			return fmt.Errorf("invalid Smart Home operating form")
		}
	}
	if value, exists := s["instance-origin"]; exists {
		var ok bool
		origin, ok = value.(string)
		if !ok {
			return fmt.Errorf("invalid Smart Home origin")
		}
	}
	if (mode != "container" && mode != "haos") || (origin != "new" && origin != "existing") {
		return fmt.Errorf("unsupported Smart Home installation selection")
	}
	alternative, module := "home-assistant", ""
	if origin == "existing" {
		alternative, module = "home-assistant-existing", "stackkits-home-assistant-existing-runtime"
	} else if mode == "haos" {
		alternative, module = "home-assistant-haos", "stackkits-home-assistant-haos-runtime"
	}
	owned, _ := seed["workloads"].(map[string]any)
	if existing, exists := owned["smart-home"]; exists {
		entry, ok := existing.(map[string]any)
		if !ok || entry["alternative"] != alternative {
			return fmt.Errorf("existing Smart Home workload requires an explicit migration, not replacement during creation")
		}
		return nil
	}
	if _, supported := authored.Workloads["smart-home"]; !supported {
		return fmt.Errorf("pinned release does not author the selected Smart Home workload")
	}
	if module == "" {
		return nil
	}
	// Appliance bindings must not inherit Compose transport, secret references,
	// or container module configuration from the initial Core proposal.
	authored.Workloads["smart-home"] = map[string]any{"alternative": alternative}
	if authored.ModuleProfiles == nil {
		authored.ModuleProfiles = map[string]map[string]any{}
	}
	authored.ModuleProfiles["smart-home"] = map[string]any{module: map[string]any{"computeProfile": "standard"}}
	return nil
}

// SmartHomeContext is advisory input, never resource inventory or authorization.
// Provisioning rechecks all resources, locality and hardware independently.
type SmartHomeContext struct {
	Existing                    bool `json:"existing"`
	ProxmoxAvailable            bool `json:"proxmox_available"`
	LANReachable                bool `json:"lan_reachable"`
	CPU                         int  `json:"cpu"`
	MemoryMiB                   int  `json:"memory_mib"`
	DiskGiB                     int  `json:"disk_gib"`
	NeedsApps                   bool `json:"needs_apps"`
	NeedsRadio                  bool `json:"needs_radio"`
	ExclusiveRadio              bool `json:"exclusive_radio"`
	PrefersApplianceMaintenance bool `json:"prefers_appliance_maintenance"`
}

type SmartHomeRecommendation struct {
	OperatingForm   string              `json:"operating_form,omitempty"`
	ManagementScope string              `json:"management_scope"`
	Reasons         []string            `json:"reasons"`
	Requirements    []string            `json:"requirements"`
	Capabilities    map[string][]string `json:"capabilities"`
}

func RecommendSmartHome(context SmartHomeContext, settings map[string]any) SmartHomeRecommendation {
	r := SmartHomeRecommendation{ManagementScope: "managed", Reasons: []string{}, Requirements: []string{}, Capabilities: map[string][]string{
		"container": {"home-assistant-core", "operator-managed-host"},
		"haos":      {"home-assistant-core", "supervisor", "apps", "appliance-maintenance"},
	}}
	if context.Existing || settings["instance-origin"] == "existing" {
		r.ManagementScope = "observed"
		r.Reasons = append(r.Reasons, "preserve-existing-installation")
		r.Requirements = append(r.Requirements, "detect-installed-form-and-api-capabilities", "separate-management-grant")
		return r
	}
	capacity := false
	for _, profile := range substrate.Profiles() {
		if profile.ID == "haos" {
			capacity = context.CPU >= profile.MinCPU && context.MemoryMiB >= profile.MinMemoryMiB && context.DiskGiB >= profile.MinDiskGiB
		}
	}
	ready := context.ProxmoxAvailable && context.LANReachable && capacity
	r.OperatingForm = "container"
	if ready {
		r.OperatingForm = "haos"
		r.Reasons = append(r.Reasons, "dedicated-appliance-target-available")
	}
	if selected, ok := settings["operating-form"].(string); ok && (selected == "container" || selected == "haos") {
		r.OperatingForm = selected
		r.Reasons = append(r.Reasons, "explicit-user-selection")
	}
	if context.NeedsApps && r.OperatingForm != "haos" {
		r.Requirements = append(r.Requirements, "apps-require-haos")
	}
	if context.PrefersApplianceMaintenance {
		r.Reasons = append(r.Reasons, "haos-provides-appliance-maintenance")
	}
	if r.OperatingForm == "haos" && !ready {
		r.Requirements = append(r.Requirements, "verified-proxmox-lan-and-guest-capacity")
	}
	if !context.LANReachable {
		r.Requirements = append(r.Requirements, "verify-local-device-reachability")
	}
	if context.NeedsRadio && !context.ExclusiveRadio {
		r.Requirements = append(r.Requirements, "identify-and-exclusively-authorize-radio")
	}
	return r
}
