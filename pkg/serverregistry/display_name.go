package serverregistry

import (
	"strings"
	"unicode"
)

// DisplayName is the operator-facing identity for one runtime server.
//
// A Hostinger SRV identity is the customer's name for that VPS. It wins over a
// cloned Linux hostname so two enrolled nodes never collapse into the same
// label when only one of them is the provider instance.
func DisplayName(name, providerRef string, target RuntimeTarget, observedHostname string) string {
	for _, candidate := range []string{target.ProviderTargetRef, providerRef, observedHostname} {
		if label := HostingerServerLabel(candidate); label != "" {
			return label
		}
	}
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		return trimmed
	}
	if trimmed := strings.TrimSpace(observedHostname); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(providerRef)
}

// HostingerServerLabel extracts a provider-assigned SRV identity such as
// srv1161760 from a provider ref, target ref, or observed hostname.
func HostingerServerLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if i := strings.LastIndex(value, ":"); i >= 0 {
		value = strings.TrimSpace(value[i+1:])
	}
	if i := strings.IndexAny(value, "./"); i >= 0 {
		value = value[:i]
	}
	value = strings.ToLower(strings.TrimSpace(value))
	if !strings.HasPrefix(value, "srv") || len(value) < 4 {
		return ""
	}
	for _, r := range value[3:] {
		if !unicode.IsDigit(r) {
			return ""
		}
	}
	return value
}

// DisplayQualifier distinguishes one of several servers that still share a
// display name after Hostinger SRV extraction. It prefers a provider id, then
// offering, then a short durable id — never a second copy of the same name.
func DisplayQualifier(providerID, offering, id string) string {
	if providerID = strings.TrimSpace(providerID); providerID != "" {
		if label := HostingerServerLabel(providerID); label != "" {
			return label
		}
		return providerID
	}
	if offering = strings.ReplaceAll(strings.TrimSpace(offering), "_", " "); offering != "" {
		return offering
	}
	id = strings.TrimSpace(id)
	if len(id) > 6 {
		return id[len(id)-6:]
	}
	return id
}
