package specv2

import (
	"context"
	"slices"
	"testing"
)

type routeCapabilityAuthor struct{}

func (routeCapabilityAuthor) AuthorGoals(_ context.Context, _, _, _, _ string, selection GoalSelection) (GoalAuthoring, error) {
	authored := GoalAuthoring{Workloads: map[string]any{}, Routes: map[string]map[string]any{}, AccessPolicies: map[string]map[string]any{}, Capabilities: []string{"internal-pki"}}
	for _, goal := range selection.Goals {
		authored.Workloads[goal] = map[string]any{"alternative": goal}
		authored.Routes[goal] = map[string]any{goal + "-initial-https": map[string]any{"serviceRef": goal, "accessPolicyRef": goal + "-owner", "tls": map[string]any{"mode": "internal"}}}
		authored.AccessPolicies[goal] = map[string]any{goal + "-owner": map[string]any{"audience": "owner"}}
	}
	return authored, nil
}

// A route the release authors with internal TLS compiles only together with
// the capability the release enables for it (StackKits profile_spec_mismatch).
func TestProjectionEnablesTheCapabilitiesOfAuthoredRoutes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing bool
		want     bool
	}{
		{name: "new routed workload", want: true},
		{name: "workload already in the seed", existing: true, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seed := basementSeed()
			if tc.existing {
				seed["workloads"] = map[string]any{"vault": map[string]any{"alternative": "vault"}}
			}
			projected, err := NewReleaseProjector(routeCapabilityAuthor{}).Project(context.Background(), seed, foundIntent("route-capabilities", "vault"), "")
			if err != nil {
				t.Fatal(err)
			}
			capabilities, _ := projected.Spec["capabilities"].(map[string]any)
			enabled, _ := capabilities["enable"].([]any)
			if got := slices.Contains(enabled, any("internal-pki")); got != tc.want {
				t.Fatalf("capabilities.enable=%v, want internal-pki enabled=%v", enabled, tc.want)
			}
			if _, mutated := seed["capabilities"]; mutated {
				t.Fatal("seed capabilities were mutated")
			}
		})
	}
}
