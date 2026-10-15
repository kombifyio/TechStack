package routes

import (
	"path"
	"sort"
	"strings"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

const serviceControlKey = "declared_control_service_key"
const serviceSupersededBy = "superseded_by_service_id"
const serviceControlID = "declared_control_service_id"
const serviceControlLocks = "declared_control_lock_service_ids"

// Runtime components keep their own identities. Their application is the
// verified StackKits control group, whose published capability set and key
// remain the only authority for commands against those components.
func projectDeclaredServiceControls(rows []controlplane.ServiceRuntime, servers map[string]*controlplane.ServerRuntime) []controlplane.ServiceRuntime {
	result := append([]controlplane.ServiceRuntime(nil), rows...)
	controls := map[string]int{}
	for i := range result {
		row := &result[i]
		if server := servers[row.ServerID]; server != nil {
			row.ServerID = server.ID
		}
		metadata := make(map[string]any, len(row.Metadata)+2)
		for key, value := range row.Metadata {
			metadata[key] = value
		}
		row.Metadata = metadata
		delete(row.Metadata, serviceControlKey)
		delete(row.Metadata, serviceSupersededBy)
		delete(row.Metadata, serviceControlID)
		delete(row.Metadata, serviceControlLocks)
		if row.Source != serviceregistry.SourceStackKitsInventory || len(row.Capabilities) == 0 {
			continue
		}
		key := declaredControlIdentity(*row, row.ServiceKey)
		if _, exists := controls[key]; exists {
			controls[key] = -1
		} else {
			controls[key] = i
		}
	}
	groups := map[int][]int{}
	for i := range result {
		row := &result[i]
		application := stringFromAnyMap(row.Metadata, "application_key")
		identity := stringMapFromAny(row.Metadata["runtime_identity"])
		if row.Source != serviceregistry.SourceStackKitsInventory || len(row.Capabilities) != 0 || application == "" || application == "system" || identity["container_name"] == "" {
			continue
		}
		controlIndex, found := controls[declaredControlIdentity(*row, application)]
		if !found || controlIndex < 0 {
			continue
		}
		control := result[controlIndex]
		if metadataInt64(row.Metadata, "inventory_revision") <= 0 || metadataInt64(row.Metadata, "inventory_revision") != metadataInt64(control.Metadata, "inventory_revision") {
			continue
		}
		row.Metadata[serviceControlKey] = control.ServiceKey
		row.Metadata[serviceControlID] = control.ID
		if len(row.Capabilities) == 0 {
			row.Capabilities = append([]string(nil), control.Capabilities...)
		}
		groups[controlIndex] = append(groups[controlIndex], i)
	}
	for controlIndex, components := range groups {
		group := append([]int{controlIndex}, components...)
		lockedIDs := []string{}
		for _, i := range group {
			if result[i].MutationLock.Locked() {
				lockedIDs = append(lockedIDs, result[i].ID)
			}
		}
		if len(lockedIDs) == 0 {
			continue
		}
		sort.Strings(lockedIDs)
		for _, i := range group {
			result[i].Metadata[serviceControlLocks] = lockedIDs
			capabilities := []string{}
			for _, action := range result[i].Capabilities {
				if action == "logs" {
					capabilities = append(capabilities, action)
				}
			}
			result[i].Capabilities = capabilities
		}
	}
	for i := range result {
		row := &result[i]
		if row.Source != serviceregistry.SourceObserved || stringFromAnyMap(row.Access, "reason_code") != "discovery_service_absent" {
			continue
		}
		counterpart := ""
		for j := range result {
			candidate := result[j]
			if candidate.Source != serviceregistry.SourceStackKitsInventory || candidate.TenantID != row.TenantID || candidate.StackID != row.StackID || candidate.ServerID != row.ServerID {
				continue
			}
			if !sameMeasuredRuntime(*row, candidate) {
				continue
			}
			if counterpart != "" {
				counterpart = ""
				break
			}
			counterpart = candidate.ID
		}
		if counterpart != "" {
			row.Metadata[serviceSupersededBy] = counterpart
			row.Capabilities = []string{}
		}
	}
	return result
}

func declaredControlIdentity(row controlplane.ServiceRuntime, key string) string {
	return row.TenantID + "\x00" + row.StackID + "\x00" + row.ServerID + "\x00" + key
}

func sameMeasuredRuntime(history, current controlplane.ServiceRuntime) bool {
	oldIdentity := stringMapFromAny(history.Metadata["runtime_identity"])
	identity := stringMapFromAny(current.Metadata["runtime_identity"])
	if identity["container_name"] == "" || strings.TrimPrefix(history.Name, "/") != identity["container_name"] || current.ObservedAt == nil || metadataInt64(current.Metadata, "inventory_revision") <= 0 || metadataInt64(current.Metadata, "inventory_revision") != metadataInt64(history.Metadata, "inventory_revision") {
		return false
	}
	if identity["kind"] != "docker_compose_service" || oldIdentity["kind"] != identity["kind"] || identity["project"] == "" || oldIdentity["project"] != identity["project"] || identity["service"] == "" || oldIdentity["service"] != identity["service"] {
		return false
	}
	oldFile, file := path.Clean(strings.ReplaceAll(oldIdentity["file"], "\\", "/")), path.Clean(strings.ReplaceAll(identity["file"], "\\", "/"))
	return file != "." && oldFile != "." && (oldFile == file || strings.HasSuffix(oldFile, "/"+file))
}

func serviceActionControlKey(service *controlplane.ServiceRuntime) string {
	return firstNonEmptyString(stringFromAnyMap(service.Metadata, serviceControlKey), service.ServiceKey)
}

func serviceGroupLockSources(service controlplane.ServiceRuntime) []string {
	ids, _ := service.Metadata[serviceControlLocks].([]string)
	return ids
}
