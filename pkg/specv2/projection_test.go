package specv2

import (
	"testing"
)

func basementSeed() map[string]any {
	return map[string]any{
		"apiVersion": "stackkit/v2alpha1",
		"kind":       "StackSpec",
		"kit":        map[string]any{"slug": "basement-kit"},
		"metadata":   map[string]any{"name": "seed-name"},
		"source":     map[string]any{"kind": "native-v2"},
		"sites": []any{
			map[string]any{"id": "home", "kind": "home", "failureDomain": "home-primary"},
		},
		"nodes": []any{
			map[string]any{
				"id": "main", "siteRef": "home", "enabled": true,
				"roles":         []any{"controller", "worker"},
				"failureDomain": "node-main",
				"hardware":      map[string]any{"arch": "amd64", "profile": "standard"},
			},
		},
	}
}

func modernSeed() map[string]any {
	seed := basementSeed()
	seed["kit"] = map[string]any{"slug": "modern-homelab"}
	seed["sites"] = []any{
		map[string]any{"id": "home", "kind": "home"},
		map[string]any{"id": "cloud", "kind": "cloud"},
	}
	seed["nodes"] = []any{
		map[string]any{"id": "cloud-edge", "siteRef": "cloud", "roles": []any{"edge", "worker"}},
		map[string]any{"id": "home-main", "siteRef": "home", "roles": []any{"controller"}},
	}
	seed["data"] = map[string]any{
		"defaultAuthority": "home",
		"bindings": map[string]any{
			"photos": map[string]any{"classes": []any{"personal"}, "primarySiteRef": "home"},
		},
	}
	seed["workloads"] = map[string]any{
		"photos": map[string]any{"alternative": "immich"},
	}
	return seed
}

func foundIntent(name string, goals ...string) WizardIntent {
	return WizardIntent{
		Schema:        WizardIntentSchema,
		RunKind:       RunKindFirstRun,
		Name:          name,
		Goals:         goals,
		KitAssignment: KitAssignment{Mode: KitAssignmentFound, KitSlug: "basement-kit"},
	}
}

func joinIntent(roles ...string) WizardIntent {
	return WizardIntent{
		Schema:        WizardIntentSchema,
		RunKind:       RunKindExpansion,
		Name:          "my-homelab",
		Server:        ServerIntent{Roles: roles},
		KitAssignment: KitAssignment{Mode: KitAssignmentJoin, KitDeploymentID: "stack-1"},
	}
}

func TestProjectFoundSetsIdentityAndMappedWorkload(t *testing.T) {
	seed := basementSeed()
	projection, err := Project(seed, foundIntent("My Homelab", "photos", "smart-home"), "hl-1")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	metadata := projection.Spec["metadata"].(map[string]any)
	if metadata["name"] != "my-homelab" || metadata["fleetRef"] != "hl-1" {
		t.Fatalf("metadata = %#v, want contract-id name my-homelab", metadata)
	}
	if projection.NodeID != "main" {
		t.Fatalf("NodeID = %q, want main", projection.NodeID)
	}

	workloads := projection.Spec["workloads"].(map[string]any)
	photos, ok := workloads["photos"].(map[string]any)
	if !ok || photos["alternative"] != "immich" {
		t.Fatalf("photos workload = %#v", workloads)
	}
	placement := photos["placement"].(map[string]any)
	sites := placement["siteRefs"].([]any)
	if len(sites) != 1 || sites[0] != "home" {
		t.Fatalf("placement = %#v, want controller site home", placement)
	}
	secretRefs := photos["secretRefs"].(map[string]any)
	if secretRefs["database-password"] != "techstack://my-homelab/photos/database-password" {
		t.Fatalf("secretRefs = %#v", secretRefs)
	}

	if len(projection.UnmappedGoals) != 1 || projection.UnmappedGoals[0] != "smart-home" {
		t.Fatalf("UnmappedGoals = %#v, want [smart-home]", projection.UnmappedGoals)
	}

	// The input seed must stay untouched.
	if basement := basementSeed(); basement["metadata"].(map[string]any)["name"] != seed["metadata"].(map[string]any)["name"] {
		t.Fatalf("seed was mutated: %#v", seed["metadata"])
	}
	if _, mutated := seed["workloads"]; mutated {
		t.Fatalf("seed gained workloads: %#v", seed)
	}
}

func TestProjectFoundRolesKeepController(t *testing.T) {
	intent := foundIntent("Roles Homelab")
	intent.Server.Roles = []string{"storage"}
	projection, err := Project(basementSeed(), intent, "")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	nodes := projection.Spec["nodes"].([]any)
	roles := nodes[0].(map[string]any)["roles"].([]any)
	if len(roles) != 2 || roles[0] != RoleController || roles[1] != RoleStorage {
		t.Fatalf("roles = %#v, want controller retained first", roles)
	}
}

func TestProjectJoinAppendsWorkerOnControllerSite(t *testing.T) {
	projection, err := Project(basementSeed(), joinIntent(), "hl-1")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	nodes := projection.Spec["nodes"].([]any)
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(nodes))
	}
	joined := nodes[1].(map[string]any)
	if projection.NodeID != "worker-1" || joined["id"] != "worker-1" {
		t.Fatalf("joined node = %#v (NodeID %q)", joined, projection.NodeID)
	}
	if joined["siteRef"] != "home" || joined["failureDomain"] != "node-worker-1" {
		t.Fatalf("joined node placement = %#v", joined)
	}

	// A second join allocates the next free id.
	second, err := Project(projection.Spec, joinIntent(), "hl-1")
	if err != nil {
		t.Fatalf("second Project: %v", err)
	}
	if second.NodeID != "worker-2" {
		t.Fatalf("second NodeID = %q, want worker-2", second.NodeID)
	}
}

func TestProjectJoinStorageGetsStorageProfile(t *testing.T) {
	projection, err := Project(basementSeed(), joinIntent("storage"), "")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	joined := projection.Spec["nodes"].([]any)[1].(map[string]any)
	hardware := joined["hardware"].(map[string]any)
	if projection.NodeID != "storage-1" || hardware["profile"] != "storage" {
		t.Fatalf("storage join = %#v (NodeID %q)", joined, projection.NodeID)
	}
}

func TestProjectJoinRejectsSecondController(t *testing.T) {
	if _, err := Project(basementSeed(), joinIntent("controller"), ""); err == nil {
		t.Fatal("expected error for second controller join")
	}
}

func TestProjectJoinLegacyRoleVocabularyNormalizes(t *testing.T) {
	intent := joinIntent("foundation")
	if err := intent.Validate(); err != nil {
		t.Fatalf("legacy role should validate via normalization: %v", err)
	}
	// foundation normalizes to controller, which join rejects — the error
	// must be the closed contract, not an unknown-role panic.
	if _, err := Project(basementSeed(), intent, ""); err == nil {
		t.Fatal("expected controller-join rejection for normalized foundation role")
	}
}

func TestProjectRespectsPreseededWorkloadAndDataBinding(t *testing.T) {
	seed := modernSeed()
	intent := foundIntent("Modern", "photos")
	intent.KitAssignment.KitSlug = "modern-homelab"
	projection, err := Project(seed, intent, "hl-9")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	photos := projection.Spec["workloads"].(map[string]any)["photos"].(map[string]any)
	if _, overridden := photos["placement"]; overridden {
		t.Fatalf("preseeded workload was overridden: %#v", photos)
	}
	if len(projection.UnmappedGoals) != 0 {
		t.Fatalf("UnmappedGoals = %#v", projection.UnmappedGoals)
	}
}

func TestProjectRecordsUnmappedPurpose(t *testing.T) {
	intent := joinIntent("storage")
	intent.Server.Purpose = "Backup"
	projection, err := Project(basementSeed(), intent, "")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if projection.UnmappedPurpose != "backup" {
		t.Fatalf("UnmappedPurpose = %q, want backup", projection.UnmappedPurpose)
	}
	// Purpose never invents spec state.
	if _, exists := projection.Spec["addons"]; exists {
		t.Fatalf("purpose leaked into spec addons: %#v", projection.Spec["addons"])
	}
}

func TestWizardIntentValidateContract(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*WizardIntent)
	}{
		{"bad schema", func(i *WizardIntent) { i.Schema = "v0" }},
		{"bad run kind", func(i *WizardIntent) { i.RunKind = "again" }},
		{"empty name", func(i *WizardIntent) { i.Name = "  " }},
		{"unknown kit", func(i *WizardIntent) { i.KitAssignment.KitSlug = "ha-kit" }},
		{"join without target", func(i *WizardIntent) {
			i.KitAssignment = KitAssignment{Mode: KitAssignmentJoin}
		}},
		{"unknown role", func(i *WizardIntent) { i.Server.Roles = []string{"boss"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			intent := foundIntent("ok", "photos")
			tc.mutate(&intent)
			if err := intent.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
