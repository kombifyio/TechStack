//nolint:goconst
package stacks

import (
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

//nolint:goconst // Response keys intentionally mirror the stacks API schema.
func stackListItemFromStore(stack controlplane.Stack) map[string]any {
	catalogRef := normalizeStackListKitRef(stringFromMaps("stackkit_catalog_ref", stack.RuntimeSummary, stack.Config))
	desiredWorkloads, workloadSelectionSource := desiredWorkloadSelection(stack.Config)
	e2eScenario := stackSpecMetadataString(stack.Config, "e2e_scenario")
	return map[string]any{
		"id":                              stack.ID,
		"kit_deployment_id":               stack.ID,
		"homelab_id":                      stack.HomelabID,
		"stackkit_id":                     catalogRef,
		"stackkit_instance_id":            stack.StackKitInstanceID,
		"name":                            stack.Name,
		"mode":                            stack.Mode,
		"status":                          stack.Status,
		"runtime_phase":                   stringFromMaps("runtime_phase", stack.RuntimeSummary, stack.Config),
		"server_mode":                     stringFromMaps("server_mode", stack.RuntimeSummary, stack.Config),
		"runtime_lane":                    stringFromMaps("runtime_lane", stack.RuntimeSummary, stack.Config),
		"runtime_offering_id":             stringFromMaps("runtime_offering_id", stack.RuntimeSummary, stack.Config),
		"provider_id":                     stringFromMaps("provider_id", stack.RuntimeSummary, stack.Config),
		"lease_provider":                  stringFromMaps("lease_provider", stack.RuntimeSummary, stack.Config),
		"provider_region":                 stringFromMaps("provider_region", stack.RuntimeSummary, stack.Config),
		"ionos_datacenter":                stringFromMaps("ionos_datacenter", stack.RuntimeSummary, stack.Config),
		"lease_id":                        stringFromMaps("lease_id", stack.RuntimeSummary, stack.Config),
		"simulate_provider_id":            stringFromMaps("simulate_provider_id", stack.RuntimeSummary, stack.Config),
		"simulate_node_lifecycle":         stringFromMaps("simulate_node_lifecycle", stack.RuntimeSummary, stack.Config),
		"desired_state":                   stringFromMaps("desired_state", stack.RuntimeSummary, stack.Config),
		"billing_mode":                    stringFromMaps("billing_mode", stack.RuntimeSummary, stack.Config),
		"billing_cadence":                 stringFromMaps("billing_cadence", stack.RuntimeSummary, stack.Config),
		"catalog_ref":                     catalogRef,
		"stackkit_catalog_ref":            catalogRef,
		"desired_workloads":               desiredWorkloads,
		"workload_selection_source":       workloadSelectionSource,
		"e2e_scenario":                    e2eScenario,
		"verification_status":             stringFromMaps("verification_status", stack.RuntimeSummary, stack.Config),
		"server_provisioning_mode":        stringFromMaps("server_provisioning_mode", stack.RuntimeSummary, stack.Config),
		"server_connection_mode":          stringFromMaps("server_connection_mode", stack.RuntimeSummary, stack.Config),
		"server_remote_host_present":      boolFromMaps("server_remote_host_present", stack.RuntimeSummary, stack.Config),
		"server_remote_user_present":      boolFromMaps("server_remote_user_present", stack.RuntimeSummary, stack.Config),
		"server_remote_auth_method":       stringFromMaps("server_remote_auth_method", stack.RuntimeSummary, stack.Config),
		"server_remote_credential_ref":    stringFromMaps("server_remote_credential_ref", stack.RuntimeSummary, stack.Config),
		"server_remote_use_sudo":          boolFromMaps("server_remote_use_sudo", stack.RuntimeSummary, stack.Config),
		"server_install_command_required": boolFromMaps("server_install_command_required", stack.RuntimeSummary, stack.Config),
		"legacy":                          false,
		"demo_anchor":                     boolFromMaps("demo_anchor", stack.RuntimeSummary, stack.Config),
		"created":                         formatAPITime(stack.CreatedAt),
		"updated":                         formatAPITime(stack.UpdatedAt),
	}
}

func stackSpecMetadataString(config map[string]any, key string) string {
	spec, ok := stackSpecMapFromValue(config[stackConfigKeySpecV2])
	if !ok {
		return ""
	}
	metadata, ok := stackSpecMapFromValue(spec["metadata"])
	if !ok {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

// desiredWorkloadSelection is a read projection of the canonical StackSpec v2
// authority. It deliberately does not copy this state into services_json:
// runtime-service evidence and desired workloads have different semantics,
// and the validated spec must remain the only desired-state authority.
func desiredWorkloadSelection(config map[string]any) ([]map[string]any, string) {
	spec, ok := stackSpecMapFromValue(config[stackConfigKeySpecV2])
	if !ok {
		return nil, ""
	}
	workloads, _ := stackSpecMapFromValue(spec["workloads"])
	ids := make([]string, 0, len(workloads))
	for id := range workloads {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	selection := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		item := map[string]any{"id": id}
		if workload, ok := stackSpecMapFromValue(workloads[id]); ok {
			if alternative, _ := workload["alternative"].(string); strings.TrimSpace(alternative) != "" {
				item["alternative"] = strings.TrimSpace(alternative)
			}
		}
		selection = append(selection, item)
	}
	return selection, stackConfigKeySpecV2
}

func normalizeStackListKitRef(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "basement", "basementkit":
		return "basement-kit"
	case "cloud", "cloudkit", "kombify-cloud-kit":
		return "cloud-kit"
	default:
		return strings.TrimSpace(value)
	}
}

func stringFromMaps(key string, maps ...map[string]any) string {
	for _, values := range maps {
		if raw, ok := values[key]; ok {
			if value, ok := raw.(string); ok {
				return value
			}
		}
	}
	return ""
}

func boolFromMaps(key string, maps ...map[string]any) bool {
	for _, values := range maps {
		if raw, ok := values[key]; ok {
			if value, ok := raw.(bool); ok {
				return value
			}
		}
	}
	return false
}

func formatAPITime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}
