package specv2

import (
	"context"
	"testing"
)

// Exercise the CLI-document consumer through the public projector: module
// custody must travel with a new application, without replacing owned intent.
type nativeDocumentAuthor struct{}

func (nativeDocumentAuthor) AuthorGoals(context.Context, string, string, string, string, GoalSelection) (GoalAuthoring, error) {
	return goalAuthoringFromDocument([]byte(`{
"apiVersion":"stackkit/v2alpha2","kind":"StackSpec","kit":{"slug":"basement-kit"},
"workloads":{"files":{"alternative":"cloudreve"},"vault":{"alternative":"vaultwarden"}},
"modules":{"stackkits-basement-core-lite-runtime":{"computeProfile":"low"},"stackkits-cloudreve-runtime":{"computeProfile":"standard"},"stackkits-vaultwarden-runtime":{"computeProfile":"standard"}},
"routes":{"files-https":{"moduleRef":"stackkits-cloudreve-runtime","serviceRef":"files","host":"files.home.example.com","protocol":"https","accessPolicyRef":"files-access"},"vault-https":{"moduleRef":"stackkits-vaultwarden-runtime","serviceRef":"vault-store","host":"vault.home.example.com","protocol":"https","accessPolicyRef":"vault-access"}},
"access":{"files-access":{"authentication":"human+device"},"vault-access":{"authentication":"human+device","privilege":"vault","ownerStepUpRequired":true}},
"data":{"bindings":{"vault-store":{"classes":["secret"],"primarySiteRef":"home"},"files":{"classes":["personal"],"primarySiteRef":"home"},"scratch":{"classes":["secret"],"primarySiteRef":"scratch-owner"}}},
"owner":{"email":"scratch-owner@example.invalid"}
}`), KitSlugBasement, []string{"files", "vault"}, nil, map[string]bool{"files": true, "vault": true}, map[string]workloadModuleBinding{"files": {AlternativeRef: "cloudreve", ModuleRef: "stackkits-cloudreve-runtime"}, "vault": {AlternativeRef: "vaultwarden", ModuleRef: "stackkits-vaultwarden-runtime"}})
}

func TestProjectNativeWorkloadsPreservesModuleCustody(t *testing.T) {
	seed := basementSeed()
	seed["apiVersion"] = NativeSpecAPIVersion
	seed["workloads"] = map[string]any{"files": map[string]any{"alternative": "owned-files", "runtimeAdapterRef": "komodo"}}
	seed["modules"] = map[string]any{"owned-files-runtime": map[string]any{"computeProfile": "high"}}
	seed["routes"] = map[string]any{"files-https": map[string]any{"serviceRef": "files", "host": "files.owned.example.com", "accessPolicyRef": "owned-files-access"}}
	seed["access"] = map[string]any{"owned-files-access": map[string]any{"authentication": "device"}}
	seed["data"] = map[string]any{"bindings": map[string]any{"files": map[string]any{"primarySiteRef": "owned-storage"}}}
	seed["owner"] = map[string]any{"email": "actual-owner@example.com"}
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
	routes := result.Spec["routes"].(map[string]any)
	route := routes["vault-https"].(map[string]any)
	policy := result.Spec["access"].(map[string]any)[route["accessPolicyRef"].(string)].(map[string]any)
	if route["protocol"] != "https" || route["host"] != "vault.home.example.com" || policy["ownerStepUpRequired"] != true {
		t.Fatal("new vault lost its authenticated HTTPS callback route")
	}
	data := result.Spec["data"].(map[string]any)["bindings"].(map[string]any)
	if data["vault-store"].(map[string]any)["primarySiteRef"] != "home" || data["files"].(map[string]any)["primarySiteRef"] != "owned-storage" || routes["files-https"].(map[string]any)["host"] != "files.owned.example.com" || result.Spec["owner"].(map[string]any)["email"] != "actual-owner@example.com" {
		t.Fatal("projection replaced owned access, data placement or identity with scratch authoring intent")
	}
	if _, copied := data["scratch"]; copied {
		t.Fatal("new workload imported unrelated scratch data custody")
	}
}
