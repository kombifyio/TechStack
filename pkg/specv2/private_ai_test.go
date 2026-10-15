package specv2

import (
	"context"
	"reflect"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

type privateAIAuthor struct{}

func (privateAIAuthor) AuthorGoals(context.Context, string, string, string, string, GoalSelection) (GoalAuthoring, error) {
	return GoalAuthoring{Workloads: map[string]any{"ai": map[string]any{"alternative": "private-ai"}}}, nil
}

func privateAICatalog(realization string) stackkitrelease.UseCaseCatalog {
	return stackkitrelease.UseCaseCatalog{UseCases: []stackkitrelease.UseCase{{ID: "ai", Settings: []stackkitrelease.UseCaseSetting{
		{ID: "model-size", Kind: "choice", Default: "small", Realization: realization},
		{ID: "model-pack-general", Kind: "toggle", Default: true, Realization: realization},
		{ID: "model-pack-coding", Kind: "toggle", Default: false, Realization: realization},
	}}}}
}

// Apply pulls exactly the model packs the StackSpec names, so the operator's
// choice must reach the ai workload, and only for a release that installs it.
func TestProjectPrivateAIModelChoicesOnlyForAnInstallingRelease(t *testing.T) {
	intent := foundIntent("ai-owner", "ai")
	intent.UseCaseSettings = map[string]map[string]any{"ai": {"model-size": "medium", "model-pack-coding": true}}
	projector := NewReleaseProjector(privateAIAuthor{})
	projector.catalog = func() (stackkitrelease.UseCaseCatalog, error) { return privateAICatalog("install"), nil }
	result, err := projector.Project(context.Background(), basementSeed(), intent, "")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"model-size": "medium", "model-pack-general": true, "model-pack-coding": true}
	if got := result.Spec["workloads"].(map[string]any)["ai"].(map[string]any)["settings"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("ai settings = %v, want %v", got, want)
	}
	projector.catalog = func() (stackkitrelease.UseCaseCatalog, error) { return privateAICatalog("recorded"), nil }
	result, err = projector.Project(context.Background(), basementSeed(), intent, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, set := result.Spec["workloads"].(map[string]any)["ai"].(map[string]any)["settings"]; set {
		t.Fatal("a recording release received settings its StackSpec contract rejects")
	}
}
