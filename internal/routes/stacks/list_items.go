//nolint:goconst
package stacks

import (
	"strings"
	"time"

	"github.com/pocketbase/pocketbase/core"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

func stackListItem(stack *core.Record) map[string]any {
	catalogRef := normalizeStackListKitRef(stack.GetString("stackkit_catalog_ref"))
	return map[string]any{
		"id":                              stack.Id,
		"name":                            stack.GetString("name"),
		"mode":                            stack.GetString("mode"),
		"status":                          stack.GetString("status"),
		"runtime_phase":                   stack.GetString("runtime_phase"),
		"server_mode":                     stack.GetString("server_mode"),
		"runtime_lane":                    stack.GetString("runtime_lane"),
		"runtime_offering_id":             stack.GetString("runtime_offering_id"),
		"provider_id":                     stack.GetString("provider_id"),
		"lease_provider":                  stack.GetString("lease_provider"),
		"provider_region":                 stack.GetString("provider_region"),
		"ionos_datacenter":                stack.GetString("ionos_datacenter"),
		"lease_id":                        stack.GetString("lease_id"),
		"simulate_provider_id":            stack.GetString("simulate_provider_id"),
		"simulate_node_lifecycle":         stack.GetString("simulate_node_lifecycle"),
		"desired_state":                   stack.GetString("desired_state"),
		"billing_mode":                    stack.GetString("billing_mode"),
		"billing_cadence":                 stack.GetString("billing_cadence"),
		"catalog_ref":                     catalogRef,
		"stackkit_catalog_ref":            catalogRef,
		"verification_status":             stack.GetString("verification_status"),
		"server_provisioning_mode":        stack.GetString("server_provisioning_mode"),
		"server_connection_mode":          stack.GetString("server_connection_mode"),
		"server_remote_host_present":      stack.GetBool("server_remote_host_present"),
		"server_remote_user_present":      stack.GetBool("server_remote_user_present"),
		"server_remote_auth_method":       stack.GetString("server_remote_auth_method"),
		"server_remote_credential_ref":    stack.GetString("server_remote_credential_ref"),
		"server_remote_use_sudo":          stack.GetBool("server_remote_use_sudo"),
		"server_install_command_required": stack.GetBool("server_install_command_required"),
		// Rows still served from the retired PocketBase store are labeled so the
		// dashboard can mark them and destroy/prune can treat them legacy-aware.
		"legacy":      true,
		"demo_anchor": false,
		"created":     stack.GetString("created"),
		"updated":     stack.GetString("updated"),
	}
}

//nolint:goconst // Response keys intentionally mirror the stacks API schema.
func stackListItemFromStore(stack controlplane.Stack) map[string]any {
	catalogRef := normalizeStackListKitRef(stringFromMaps("stackkit_catalog_ref", stack.RuntimeSummary, stack.Config))
	return map[string]any{
		"id":                              stack.ID,
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

func normalizeStackListKitRef(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "base-kit", "basement", "basementkit":
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
