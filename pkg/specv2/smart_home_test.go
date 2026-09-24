package specv2

import (
	"context"
	"reflect"
	"slices"
	"testing"
)

type smartHomeAuthor struct{}

func (smartHomeAuthor) AuthorGoals(context.Context, string, string, string, string, []string) (GoalAuthoring, error) {
	return GoalAuthoring{Workloads: map[string]any{"smart-home": map[string]any{"alternative": "home-assistant", "runtimeAdapterRef": "standalone-compose"}}}, nil
}

// Core custody invariant: creation must never replace an installed workload.
func TestProjectSmartHomeSelectionPreservesOwnedInstallation(t *testing.T) {
	for _, origin := range []string{"new", "existing"} {
		seed := basementSeed()
		intent := foundIntent("smart-home-owner", "smart-home")
		intent.UseCaseSettings = map[string]map[string]any{"smart-home": {"operating-form": "haos", "instance-origin": origin}}
		projector := NewReleaseProjector(smartHomeAuthor{})
		result, err := projector.Project(context.Background(), seed, intent, "")
		if err != nil {
			t.Fatal(err)
		}
		want := "home-assistant-haos"
		if origin == "existing" {
			want = "home-assistant-existing"
		}
		if result.Spec["workloads"].(map[string]any)["smart-home"].(map[string]any)["alternative"] != want {
			t.Fatal("selection not applied")
		}
		seed["workloads"] = map[string]any{"smart-home": map[string]any{"alternative": want, "settings": map[string]any{"user": "retained"}}}
		result, err = projector.Project(context.Background(), seed, intent, "")
		if err != nil || !reflect.DeepEqual(result.Spec["workloads"], seed["workloads"]) {
			t.Fatalf("owned configuration changed: %v", err)
		}
		intent.UseCaseSettings["smart-home"]["instance-origin"] = "new"
		intent.UseCaseSettings["smart-home"]["operating-form"] = "container"
		if _, err = projector.Project(context.Background(), seed, intent, ""); err == nil {
			t.Fatal("creation replaced an owned instance")
		}
	}
	intent := foundIntent("unsupported", "smart-home")
	intent.UseCaseSettings = map[string]map[string]any{"smart-home": {"operating-form": "haos"}}
	if _, err := NewReleaseProjector(releaseGoalAuthorFixture{}).Project(context.Background(), basementSeed(), intent, ""); err == nil {
		t.Fatal("unsupported release silently accepted selection")
	}
}

func TestSmartHomeRecommendationPreservesChoiceAndExposesConstraints(t *testing.T) {
	context := SmartHomeContext{ProxmoxAvailable: true, LANReachable: true, CPU: 4, MemoryMiB: 8192, DiskGiB: 64, NeedsApps: true, NeedsRadio: true}
	if got := RecommendSmartHome(context, nil); got.OperatingForm != "haos" {
		t.Fatal("suitable appliance not recommended")
	}
	got := RecommendSmartHome(context, map[string]any{"operating-form": "container"})
	if got.OperatingForm != "container" || !slices.Contains(got.Requirements, "apps-require-haos") || !slices.Contains(got.Requirements, "identify-and-exclusively-authorize-radio") {
		t.Fatal("explicit choice or compatibility constraints lost")
	}
	context.Existing = true
	if got = RecommendSmartHome(context, nil); got.ManagementScope != "observed" || got.OperatingForm != "" {
		t.Fatal("existing instance reinterpreted as a new installation")
	}
}
