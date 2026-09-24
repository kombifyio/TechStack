package specv2

import (
	"context"
	"testing"
)

// Exercise the CLI-document consumer through the public projector: module
// custody must travel with a new application, without replacing owned intent.
type nativeDocumentAuthor struct{}

func (nativeDocumentAuthor) AuthorGoals(context.Context, string, string, string, string, []string) (GoalAuthoring, error) {
	return goalAuthoringFromDocument([]byte(`{"apiVersion":"stackkit/v2alpha2","kind":"StackSpec","kit":{"slug":"basement-kit"},"workloads":{"files":{"alternative":"cloudreve"},"vault":{"alternative":"vaultwarden"}},"modules":{"stackkits-basement-core-lite-runtime":{"computeProfile":"low"},"stackkits-cloudreve-runtime":{"computeProfile":"standard"},"stackkits-vaultwarden-runtime":{"computeProfile":"standard"}}}`), KitSlugBasement, []string{"files", "vault"}, nil, map[string]bool{"files": true, "vault": true}, map[string]workloadModuleBinding{"files": {AlternativeRef: "cloudreve", ModuleRef: "stackkits-cloudreve-runtime"}, "vault": {AlternativeRef: "vaultwarden", ModuleRef: "stackkits-vaultwarden-runtime"}})
}

func TestProjectNativeWorkloadsPreservesModuleCustody(t *testing.T) {
	seed := basementSeed()
	seed["apiVersion"] = NativeSpecAPIVersion
	seed["workloads"] = map[string]any{"files": map[string]any{"alternative": "owned-files", "runtimeAdapterRef": "komodo"}}
	seed["modules"] = map[string]any{"owned-files-runtime": map[string]any{"computeProfile": "high"}}
	result, err := NewReleaseProjector(nativeDocumentAuthor{}).Project(context.Background(), seed, foundIntent("native-custody", "files", "vault"), "")
	if err != nil {
		t.Fatal(err)
	}
	workloads := result.Spec["workloads"].(map[string]any)
	if workloads["files"].(map[string]any)["runtimeAdapterRef"] != "komodo" {
		t.Fatal("existing workload selection changed")
	}
	modules := result.Spec["modules"].(map[string]any)
	if profile, ok := modules["stackkits-vaultwarden-runtime"].(map[string]any); !ok || profile["computeProfile"] != "standard" {
		t.Fatal("new workload lost its native module profile")
	}
	for _, unrelated := range []string{"stackkits-cloudreve-runtime", "stackkits-basement-core-lite-runtime"} {
		if _, copied := modules[unrelated]; copied {
			t.Fatalf("copied unrelated or superseded module %s", unrelated)
		}
	}
	if _, changed := seed["modules"].(map[string]any)["stackkits-vaultwarden-runtime"]; changed {
		t.Fatal("projection mutated seed custody")
	}
}
