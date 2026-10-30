package platformprojection

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"

	"github.com/kombifyio/techstack/pkg/serverregistry"
)

// maxProjectedServiceNames bounds what travels: a surface shows a handful of
// application chips, and the count carries the rest.
const maxProjectedServiceNames = 8

func Server(server serverregistry.Aggregate, serviceNames []string) serverregistry.ProjectedServer {
	metadata := safeAnyMap(server.Metadata)
	deployment := safeAnyMap(metadata["deployment"])
	host := safeAnyMap(metadata["host"])
	names := serviceNames
	if len(names) > maxProjectedServiceNames {
		names = names[:maxProjectedServiceNames]
	}
	observed := server.UpdatedAt
	if observed.IsZero() {
		observed = time.Now().UTC()
	}
	return serverregistry.ProjectedServer{
		ServerID: strings.TrimSpace(server.ID),
		TenantID: strings.TrimSpace(server.TenantID),
		// The platform list is owner-scoped; a server with no owner subject is
		// still projected so operators see it, but no product read will match it.
		OwnerID:  strings.TrimSpace(server.OwnerSubjectID),
		Name:     safeLabel(server.Name, 96),
		Provider: safeProviderLabel(firstNonEmptyString(stringFromAnyMap(metadata, "provider_id"), stringFromAnyMap(metadata, "provider"))),
		StackID:  strings.TrimSpace(server.StackID),
		StackkitName: safeLabel(firstNonEmptyString(
			stringFromAnyMap(metadata, "stackkit"),
			stringFromAnyMap(deployment, "stackkit"),
		), 96),
		StackkitVersion: safeLabel(firstNonEmptyString(
			stringFromAnyMap(metadata, "stackkit_version"),
			stringFromAnyMap(deployment, "stackkit_version"),
		), 96),
		LifecycleState:  strings.TrimSpace(server.LifecycleState),
		ConnectionState: strings.TrimSpace(server.ConnectionState),
		HealthState:     strings.TrimSpace(server.HealthState),
		PlatformOS: safeLabel(firstNonEmptyString(
			stringFromAnyMap(host, "os"),
			stringFromAnyMap(metadata, "os"),
		), 96),
		ServiceNames: names,
		ServiceCount: len(serviceNames),
		// Revision is the aggregate's own fence; the receiver refuses anything
		// older, so a slow pass can never overwrite a newer state.
		AggregateRevision: server.Revision,
		Retired:           server.DecommissionedAt != nil,
		ObservedAt:        observed.UTC().Format(time.RFC3339Nano),
	}
}

func safeLabel(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		value = value[:limit]
	}
	return value
}
func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
func stringFromAnyMap(values map[string]any, key string) string {
	switch value := values[key].(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	}
	return ""
}
func safeAnyMap(value any) map[string]any {
	if m, ok := value.(map[string]any); ok {
		return m
	}
	var raw []byte
	switch v := value.(type) {
	case json.RawMessage:
		raw = v
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}
func safeProviderLabel(value string) string {
	value = strings.ToLower(safeLabel(value, 64))
	if strings.Contains(value, "secret") || strings.Contains(value, "token") || strings.Contains(value, "credential") {
		return ""
	}
	for _, r := range value {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._:-", r) {
			return ""
		}
	}
	return value
}
