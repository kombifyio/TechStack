package routes

import (
	"strings"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

func serverRuntimeDisplayName(server controlplane.ServerRuntime, target serverregistry.RuntimeTarget) string {
	name := serverregistry.DisplayName(server.Name, server.ProviderRef, target, observedServerHostname(server.Metadata))
	if name != "" {
		return name
	}
	return strings.TrimSpace(server.ID)
}

func observedServerHostname(metadata map[string]any) string {
	host, _ := metadata["host"].(map[string]any)
	return stringFromAnyMap(host, workerFieldHostname)
}

func qualifyCollidingServerNames(names []string, qualifiers []string) []string {
	if len(names) != len(qualifiers) {
		return names
	}
	counts := map[string]int{}
	for _, name := range names {
		counts[strings.ToLower(strings.TrimSpace(name))]++
	}
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = name
		qualifier := strings.TrimSpace(qualifiers[i])
		if counts[strings.ToLower(strings.TrimSpace(name))] < 2 || qualifier == "" {
			continue
		}
		if strings.EqualFold(name, qualifier) {
			continue
		}
		out[i] = name + " · " + qualifier
	}
	return out
}
