package specv2

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

type releaseGoalAuthorFixture struct{}

func (releaseGoalAuthorFixture) AuthorGoals(_ context.Context, _, _, _, _ string, goals []string) (GoalAuthoring, error) {
	entries := map[string]any{
		"files": map[string]any{
			"alternative": "cloudreve", "runtimeAdapterRef": "standalone-compose",
			"placement": map[string]any{"siteRefs": []any{"home"}, "nodeRefs": []any{}, "requiresRoles": []any{}},
		},
		"photos": map[string]any{
			"alternative": "immich", "runtimeAdapterRef": "standalone-compose",
			"placement":  map[string]any{"siteRefs": []any{"home"}, "nodeRefs": []any{}, "requiresRoles": []any{}},
			"secretRefs": map[string]any{"database-password": "secret://workloads/photos/database-password"},
		},
		"vault": map[string]any{
			"alternative": "vaultwarden", "runtimeAdapterRef": "standalone-compose",
			"placement":  map[string]any{"siteRefs": []any{"home"}, "nodeRefs": []any{}, "requiresRoles": []any{}},
			"secretRefs": map[string]any{"admin-token": "secret://workloads/vault/admin-token"},
		},
	}
	authored := map[string]any{}
	var unmapped []string
	for _, goal := range goals {
		if entry, ok := entries[goal]; ok {
			authored[goal] = entry
		} else {
			unmapped = append(unmapped, goal)
		}
	}
	sort.Strings(unmapped)
	useCases := make([]string, 0, len(authored))
	for id := range authored {
		useCases = append(useCases, id)
	}
	sort.Strings(useCases)
	return GoalAuthoring{UseCases: useCases, Workloads: authored, UnmappedGoals: unmapped}, nil
}

func project(seed map[string]any, intent WizardIntent, homelabID string) (*Projection, error) {
	return NewReleaseProjector(releaseGoalAuthorFixture{}).Project(context.Background(), seed, intent, homelabID)
}

func TestProjectFoundDomainIntent(t *testing.T) {
	for _, tc := range []struct {
		name, kit, access, requested, want string
		wantError                          bool
	}{
		{name: "local automatic", kit: KitSlugBasement, access: AccessModeLocal, want: "home"},
		{name: "local custom", kit: KitSlugBasement, access: AccessModeLocal, requested: " Home.Example.com ", want: "home.example.com"},
		{name: "own VPS custom", kit: KitSlugCloud, access: AccessModeRemotePrivate, requested: "cloud.example.com", want: "cloud.example.com"},
		{name: "hybrid custom", kit: KitSlugModern, access: AccessModeRemotePrivate, requested: "hybrid.example.com", want: "hybrid.example.com"},
		{name: "remote without address", kit: KitSlugCloud, access: AccessModeRemotePrivate, wantError: true},
		{name: "reserved address", kit: KitSlugBasement, access: AccessModeLocal, requested: "TEMPLATE.INVALID.", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seed := basementSeed()
			seed["kit"] = map[string]any{"slug": tc.kit}
			seedDomainConsumers(seed, tc.kit)
			seed["network"] = map[string]any{"domain": map[string]any{"base": "template.invalid"}}
			intent := foundIntent("my-homelab")
			intent.KitAssignment.KitSlug = tc.kit
			intent.Server.Transport = TransportInstallCommand
			intent.Access.Mode = tc.access
			intent.DomainBase = tc.requested
			projection, err := project(seed, intent, "")
			if tc.wantError {
				if err == nil {
					t.Fatal("unusable address was admitted for deployment")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := projection.Spec["network"].(map[string]any)["domain"].(map[string]any)["base"]
			if got != tc.want {
				t.Fatalf("deployment domain = %v, want %s", got, tc.want)
			}
			serialized, err := json.Marshal(projection.Spec)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(serialized), ".invalid") {
				t.Fatalf("projected deployment retains an unusable endpoint: %s", serialized)
			}
			if tc.kit == KitSlugCloud {
				for _, raw := range projection.Spec["routes"].(map[string]any) {
					route := raw.(map[string]any)
					if route["host"] != route["serviceRef"].(string)+"."+tc.want {
						t.Fatalf("incorrect service endpoint: %v", route)
					}
				}
			}
			if tc.kit == KitSlugModern {
				publication := projection.Spec["bridge"].(map[string]any)["publications"].([]any)[0].(map[string]any)
				if publication["host"] != "photos."+tc.want {
					t.Fatalf("incorrect publication: %v", publication)
				}
			}
			if seed["network"].(map[string]any)["domain"].(map[string]any)["base"] != "template.invalid" {
				t.Fatal("projection changed the shared seed")
			}
		})
	}
}

// Regression: a managed Cloud seed otherwise reaches address plan as an
// external template.invalid domain and skips the existing allocator/bind.
func TestProjectManagedCloudSelectsPlatformAddressing(t *testing.T) {
	for _, transport := range []string{TransportKombifyCloud, TransportInstallCommand} {
		seed := basementSeed()
		seed["kit"] = map[string]any{"slug": KitSlugCloud}
		seedDomainConsumers(seed, KitSlugCloud)
		seed["network"] = map[string]any{"domain": map[string]any{"base": "template.invalid"}}
		intent := foundIntent("managed-address")
		intent.KitAssignment.KitSlug = KitSlugCloud
		intent.Server.Transport = transport
		if transport == TransportInstallCommand {
			intent.DomainBase = "cloud.example.com"
		}
		projection, err := project(seed, intent, "")
		if err != nil {
			t.Fatal(err)
		}
		want := "cloud.example.com"
		if transport == TransportKombifyCloud {
			want = "kombify.me"
		}
		domain := projection.Spec["network"].(map[string]any)["domain"].(map[string]any)["base"]
		if domain != want {
			t.Fatalf("%s address domain = %v, want %s", transport, domain, want)
		}
		if transport == TransportKombifyCloud {
			intent.DomainBase = "custom.example.com"
			if _, err := project(seed, intent, ""); err == nil {
				t.Fatal("managed creation accepted a conflicting external address authority")
			}
		}
	}
}

func seedDomainConsumers(seed map[string]any, kit string) {
	if kit == KitSlugCloud {
		routes := map[string]any{}
		for _, service := range []string{"base", "id", "auth"} {
			routes["cloud-"+service+"-public"] = map[string]any{"exposure": "public", "serviceRef": service, "host": service + ".template.invalid"}
		}
		seed["routes"] = routes
	}
	if kit == KitSlugModern {
		seed["bridge"] = map[string]any{"publications": []any{map[string]any{"serviceRef": "photos", "host": "photos.template.invalid"}}}
	}
}

func basementSeed() map[string]any {
	return map[string]any{
		"apiVersion": "stackkit/v2alpha1",
		"kind":       "StackSpec",
		"kit":        map[string]any{"slug": "basement-kit"},
		"metadata":   map[string]any{"name": "seed-name"},
		"network":    map[string]any{"domain": map[string]any{"base": "home"}},
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
	seedDomainConsumers(seed, KitSlugModern)
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
		"photos": map[string]any{
			"alternative": "immich",
			"secretRefs":  map[string]any{"database-password": "secret://workloads/photos/database-password"},
		},
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

type failingGoalAuthor struct{}

func (failingGoalAuthor) AuthorGoals(context.Context, string, string, string, string, []string) (GoalAuthoring, error) {
	return GoalAuthoring{}, context.DeadlineExceeded
}

func TestProjectFoundRecordsUnmappedWhenAuthorFails(t *testing.T) {
	projection, err := NewReleaseProjector(failingGoalAuthor{}).Project(
		context.Background(), basementSeed(), foundIntent("My Homelab", "photos"), "hl-1",
	)
	if err != nil {
		t.Fatalf("offered goals must not fail found: %v", err)
	}
	if projection.NodeID != "main" {
		t.Fatalf("NodeID = %q, want main", projection.NodeID)
	}
	hasPhotos := false
	for _, goal := range projection.UnmappedGoals {
		if goal == "photos" {
			hasPhotos = true
		}
	}
	if !hasPhotos {
		t.Fatalf("UnmappedGoals = %#v, want photos recorded as intent", projection.UnmappedGoals)
	}
	if _, hasWorkloads := projection.Spec["workloads"]; hasWorkloads {
		t.Fatalf("failed author must not persist workloads: %#v", projection.Spec["workloads"])
	}
}

func TestProjectFoundPreservesReleaseWorkloadsAndLocalCustody(t *testing.T) {
	seed := basementSeed()
	projection, err := project(seed, foundIntent("My Homelab", "files", "photos", "vault", "smart-home"), "hl-1")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}

	metadata := projection.Spec["metadata"].(map[string]any)
	if metadata["name"] != "my-homelab" || metadata["stackId"] != "my-homelab" || metadata["fleetRef"] != "hl-1" {
		t.Fatalf("metadata = %#v, want contract-id name my-homelab", metadata)
	}
	if projection.NodeID != "main" {
		t.Fatalf("NodeID = %q, want main", projection.NodeID)
	}

	workloads := projection.Spec["workloads"].(map[string]any)
	if workloads["files"].(map[string]any)["alternative"] != "cloudreve" || workloads["vault"].(map[string]any)["alternative"] != "vaultwarden" {
		t.Fatalf("release-authored workloads = %#v", workloads)
	}
	photos, ok := workloads["photos"].(map[string]any)
	if !ok || photos["alternative"] != "immich" || photos["runtimeAdapterRef"] != "standalone-compose" {
		t.Fatalf("photos workload = %#v", workloads)
	}
	placement := photos["placement"].(map[string]any)
	sites := placement["siteRefs"].([]any)
	if len(sites) != 1 || sites[0] != "home" {
		t.Fatalf("placement = %#v, want controller site home", placement)
	}
	secretRefs := photos["secretRefs"].(map[string]any)
	if secretRefs["database-password"] != "secret://workloads/photos/database-password" {
		t.Fatalf("secretRefs = %#v", secretRefs)
	}
	vaultRefs := workloads["vault"].(map[string]any)["secretRefs"].(map[string]any)
	if vaultRefs["admin-token"] != "secret://workloads/vault/admin-token" {
		t.Fatalf("vault secretRefs = %#v", vaultRefs)
	}

	if len(projection.UnmappedGoals) != 1 || projection.UnmappedGoals[0] != "smart-home" {
		t.Fatalf("UnmappedGoals = %#v, want [smart-home]", projection.UnmappedGoals)
	}
	if _, hasUseCases := projection.Spec["useCases"]; hasUseCases {
		t.Fatalf("operational spec must not carry useCases: %#v", projection.Spec["useCases"])
	}

	// The input seed must stay untouched.
	if basement := basementSeed(); basement["metadata"].(map[string]any)["name"] != seed["metadata"].(map[string]any)["name"] {
		t.Fatalf("seed was mutated: %#v", seed["metadata"])
	}
	if _, mutated := seed["workloads"]; mutated {
		t.Fatalf("seed gained workloads: %#v", seed)
	}
}

func TestProjectDropsUseCasesFromCanonicalSeed(t *testing.T) {
	seed := basementSeed()
	seed["useCases"] = []any{"photos", "vault"}
	projection, err := project(seed, joinIntent("worker"), "hl-1")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if _, hasUseCases := projection.Spec["useCases"]; hasUseCases {
		t.Fatalf("join must drop useCases before validate: %#v", projection.Spec["useCases"])
	}
	if projection.NodeID == "" {
		t.Fatal("join must still add a node")
	}
}

func TestProjectJoinAppendsWorkerOnControllerSite(t *testing.T) {
	projection, err := project(basementSeed(), joinIntent(), "hl-1")
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
	second, err := project(projection.Spec, joinIntent(), "hl-1")
	if err != nil {
		t.Fatalf("second Project: %v", err)
	}
	if second.NodeID != "worker-2" {
		t.Fatalf("second NodeID = %q, want worker-2", second.NodeID)
	}
}

func TestProjectJoinStorageGetsStorageProfile(t *testing.T) {
	projection, err := project(basementSeed(), joinIntent("storage"), "")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	joined := projection.Spec["nodes"].([]any)[1].(map[string]any)
	hardware := joined["hardware"].(map[string]any)
	if projection.NodeID != "storage-1" || hardware["profile"] != "storage" {
		t.Fatalf("storage join = %#v (NodeID %q)", joined, projection.NodeID)
	}
}

func TestProjectJoinCoercesControllerToWorker(t *testing.T) {
	projection, err := project(basementSeed(), joinIntent("controller"), "")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if projection.NodeID != "worker-1" {
		t.Fatalf("NodeID = %q, want worker-1", projection.NodeID)
	}
}

func TestProjectJoinLegacyRoleVocabularyNormalizes(t *testing.T) {
	intent := joinIntent("foundation")
	if err := intent.Validate(); err != nil {
		t.Fatalf("legacy role should validate via normalization: %v", err)
	}
	projection, err := project(basementSeed(), intent, "")
	if err != nil {
		t.Fatalf("Additional Node with foundation role must join as a worker: %v", err)
	}
	if projection.NodeID != "worker-1" {
		t.Fatalf("NodeID = %q, want worker-1", projection.NodeID)
	}
}

func TestProjectRespectsPreseededWorkloadAndDataBinding(t *testing.T) {
	seed := modernSeed()
	intent := foundIntent("Modern", "photos")
	intent.KitAssignment.KitSlug = "modern-homelab"
	projection, err := project(seed, intent, "hl-9")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	photos := projection.Spec["workloads"].(map[string]any)["photos"].(map[string]any)
	if _, overridden := photos["placement"]; overridden {
		t.Fatalf("preseeded workload was overridden: %#v", photos)
	}
	if photos["secretRefs"].(map[string]any)["database-password"] != "secret://workloads/photos/database-password" {
		t.Fatalf("preseeded workload custody was not preserved: %#v", photos)
	}
	if len(projection.UnmappedGoals) != 0 {
		t.Fatalf("UnmappedGoals = %#v", projection.UnmappedGoals)
	}
}

func TestProjectRecordsUnmappedPurpose(t *testing.T) {
	intent := joinIntent("storage")
	intent.Server.Purpose = "Backup"
	projection, err := project(basementSeed(), intent, "")
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
		{"unknown access mode", func(i *WizardIntent) { i.Access.Mode = "vpn" }},
		{"publication without service", func(i *WizardIntent) {
			i.Access.Publications = []ServicePublicationIntent{{Exposure: AccessExposurePublic}}
		}},
		{"solo household with drafts", func(i *WizardIntent) {
			i.Household = HouseholdIntent{
				Profile:       HouseholdProfileSolo,
				PlannedPeople: []PlannedPersonIntent{{ClientRef: "person-1", Name: "Alex"}},
			}
		}},
		{"household draft without identity", func(i *WizardIntent) {
			i.Household = HouseholdIntent{
				Profile:       HouseholdProfileShared,
				PlannedPeople: []PlannedPersonIntent{{ClientRef: "person-1"}},
			}
		}},
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
