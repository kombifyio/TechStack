package specv2

import (
	"context"
	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"testing"
)

type platformTLSAuthor struct{}

func (platformTLSAuthor) AuthorGoals(_ context.Context, _, _, _, _ string, selection GoalSelection) (GoalAuthoring, error) {
	workloads := map[string]any{}
	for _, goal := range selection.Goals {
		workloads[goal] = map[string]any{"alternative": "coolify"}
	}
	return GoalAuthoring{Workloads: workloads, TLSMinVersion: "TLS1.3"}, nil
}

// This is a narrow compiler policy boundary: a release-owned new-platform
// minimum may configure a fresh deployment, never migrate an existing policy.
func TestPlatformTLSProjectionPreservesExistingInstallations(t *testing.T) {
	for _, tc := range []struct {
		name, run, goal, want string
		existing              bool
	}{
		{name: "found platform", run: RunKindFirstRun, goal: "marketplace", want: "TLS1.3"},
		{name: "extend installed platform", run: RunKindExpansion, goal: "photos", want: "TLS1.2", existing: true},
		{name: "found existing platform", run: RunKindFirstRun, goal: "marketplace", want: "TLS1.2", existing: true},
		{name: "found application only", run: RunKindFirstRun, goal: "files", want: "TLS1.2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seed := basementSeed()
			seed["apiVersion"] = NativeSpecAPIVersion
			seed["network"].(map[string]any)["tls"] = map[string]any{"minVersion": "TLS1.2"}
			if tc.existing {
				seed["workloads"] = map[string]any{"marketplace": map[string]any{"alternative": "coolify"}}
			}
			catalog := stackkitrelease.UseCaseCatalog{UseCases: []stackkitrelease.UseCase{{ID: "marketplace", Alternatives: []stackkitrelease.UseCaseAlternative{{ID: "coolify", ProvidesRuntimeAdapterRef: "coolify"}}}}}
			projector := NewReleaseProjector(platformTLSAuthor{})
			projector.catalog = func() (stackkitrelease.UseCaseCatalog, error) { return catalog, nil }
			intent := foundIntent("platform-tls", tc.goal)
			intent.RunKind = tc.run
			if tc.run == RunKindExpansion {
				intent.KitAssignment = KitAssignment{Mode: KitAssignmentExtend, KitDeploymentID: "existing"}
			}
			projected, err := projector.Project(context.Background(), seed, intent, "")
			if err != nil {
				t.Fatal(err)
			}
			got := projected.Spec["network"].(map[string]any)["tls"].(map[string]any)["minVersion"]
			if got != tc.want {
				t.Fatalf("projected TLS=%v, want %s", got, tc.want)
			}
			if seed["network"].(map[string]any)["tls"].(map[string]any)["minVersion"] != "TLS1.2" {
				t.Fatal("seed TLS policy was mutated")
			}
		})
	}
}
