package jobs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/kombifyme"
)

type addressReadySender struct {
	changeSetSender
	ready func() bool
	t     *testing.T
}

func (s *addressReadySender) SendStackKitCommand(ctx context.Context, agent string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	if !s.ready() {
		s.t.Fatal("Advanced mutation dispatched before candidate routes were exposed")
	}
	return s.changeSetSender.SendStackKitCommand(ctx, agent, command)
}

// Handler + registry HTTP + a pinned executable fixture cover the sensitive
// boundary: only exact current custody can expose candidate routes and mutate.
func TestAdvancedChangeSetReconcilesOnlyItsCurrentManagedRoutes(t *testing.T) {
	for _, scenario := range []string{"admitted", "foreign owner", "wrong origin", "wrong base", "wrong rollout", "changed generation", "registry refusal", "wrong planned host", "planner refusal", "unleased unchanged", "unleased additions", "unleased changed generation", "zone admitted", "zone foreign owner", "zone wrong origin", "zone wrong base", "zone planned suffix escape", "zone registry suffix escape", "zone nested domain", "zone flat registry", "zone initial admitted", "zone initial foreign owner", "zone initial wrong origin"} {
		t.Run(scenario, func(t *testing.T) {
			zone := strings.HasPrefix(scenario, "zone ")
			address, _ := kombifyme.NewManagedAddress("tenant-1", "owner-1", "stack-1")
			origin := "https://203.0.113.10:443"
			registered := map[string]bool{"auth": true}
			exposed := map[string]bool{"auth": true}
			if scenario == "unleased unchanged" || scenario == "unleased changed generation" {
				for _, key := range []string{"photos", "files", "vault"} {
					registered[key], exposed[key] = true, true
				}
			}
			mutated := false
			registryWrite := false
			base := map[string]any{"id": "base-id", "name": "owned", "fqdn": "owned.kombify.me", "subdomain_kind": "base", "layout": "flat", "binding_owner_ref": address.OwnerRef, "binding_stack_ref": address.StackRef}
			if zone && scenario != "zone flat registry" {
				delete(base, "layout") // Existing installations may predate the layout field.
			}
			service := func(key string) map[string]any {
				name := "owned-" + key
				if zone {
					name = key + ".owned"
				}
				return map[string]any{"id": "service-" + key, "name": name, "fqdn": name + ".kombify.me", "subdomain_kind": "service", "parent_id": "base-id", "target_type": "proxy", "target_addr": origin, "binding_owner_ref": address.OwnerRef, "binding_stack_ref": address.StackRef, "exposed": exposed[key]}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					if r.URL.Query().Get("binding_stack_ref") != address.StackRef {
						t.Error("registry read is not stack scoped")
					}
					initial := service("auth")
					switch scenario {
					case "foreign owner", "zone foreign owner", "zone initial foreign owner":
						initial["binding_owner_ref"] = "another-owner"
					case "wrong origin", "zone wrong origin", "zone initial wrong origin":
						initial["target_addr"] = "https://203.0.113.11:443"
					case "wrong base", "zone wrong base":
						base["name"] = "another-base"
					case "zone registry suffix escape":
						initial["name"], initial["fqdn"] = "auth.owned-escape", "auth.owned-escape.kombify.me"
					}
					routes := []any{base, initial}
					if scenario == "unleased unchanged" || scenario == "unleased changed generation" {
						for _, key := range []string{"photos", "files", "vault"} {
							routes = append(routes, service(key))
						}
					}
					_ = json.NewEncoder(w).Encode(routes)
					return
				}
				var body map[string]any
				registryWrite = true
				_ = json.NewDecoder(r.Body).Decode(&body)
				if r.Method == http.MethodPost {
					binding, _ := body["binding"].(map[string]any)
					if binding["owner_ref"] != address.OwnerRef || binding["stack_ref"] != address.StackRef || body["base_subdomain_name"] != "owned" {
						t.Fatal("registry write escaped current binding")
					}
				}
				switch {
				case r.URL.Path == "/subdomains/auto-register":
					_ = json.NewEncoder(w).Encode(base)
				case r.URL.Path == "/subdomains/auto-register/service":
					if scenario == "registry refusal" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					key, _ := body["service_name"].(string)
					if body["local_addr"] != origin {
						t.Error("candidate changed the runtime origin")
					}
					if zone && body["layout"] != "zone" {
						t.Fatal("candidate changed its existing installation layout")
					}
					mutated, registered[key] = true, true
					_ = json.NewEncoder(w).Encode(service(key))
				case strings.HasSuffix(r.URL.Path, "/expose"):
					parts := strings.Split(r.URL.Path, "/")
					id := parts[len(parts)-2]
					exposed[strings.TrimPrefix(id, "service-")] = true
					_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "exposed": true})
				default:
					t.Error("unexpected registry mutation")
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			t.Setenv("KOMBIFY_ME_API_BASE", server.URL)
			t.Setenv("KOMBIFY_ME_API_KEY", "must-not-reach-cli")
			t.Setenv(kombifyme.InstallZonesEnv, "false")
			plan := stackKitAddressPlan{APIVersion: stackKitAddressPlanV1, Provider: addressModeKombifyMe, StackName: "cloud-stack", Domain: addressModeKombifyMeDomain, SubdomainPrefix: "owned"}
			if zone {
				plan.Provider, plan.Domain, plan.SubdomainPrefix = "external", "owned.kombify.me", ""
			}
			for _, key := range []string{"auth", "photos", "files", "vault"} {
				host := "owned-" + key + ".kombify.me"
				if zone {
					host = key + ".owned.kombify.me"
				}
				plan.Services = append(plan.Services, stackKitAddressPlanService{ServiceKey: key, Host: host})
			}
			if scenario == "wrong planned host" {
				plan.Services[1].Host = "another-photos.kombify.me"
			}
			if scenario == "zone planned suffix escape" {
				plan.Services[1].Host = "photos.owned.kombify.me.evil.invalid"
			}
			raw, _ := json.Marshal(plan)
			script := "#!/bin/sh\n[ -z \"$KOMBIFY_ME_API_KEY\" ] || exit 9\n[ \"$6\" = address ] && [ \"$7\" = plan ] || exit 8\nprintf '%s' '" + string(raw) + "'\n"
			if scenario == "planner refusal" {
				script = "#!/bin/sh\nexit 1\n"
			}
			if strings.HasPrefix(scenario, "zone initial") {
				// Released BindZone nests the zone beneath the input domain. An
				// already bound candidate must retain its exact identity origin.
				script = "#!/bin/sh\ncase \"$6:$7\" in\nvalidate:) exit 0;;\naddress:plan) printf '%s' '" + string(raw) + "';;\naddress:bind) exit 8;;\n*) exit 8;;\nesac\n"
			}
			release := releaseWithAdvancedCatalog(t, []byte(script))
			if strings.HasPrefix(scenario, "zone initial") {
				dir := t.TempDir()
				path := filepath.Join(dir, "candidate.json")
				candidate := map[string]any{"network": map[string]any{"domain": map[string]any{"base": "owned.kombify.me"}}, "routes": map[string]any{}}
				routes := candidate["routes"].(map[string]any)
				for _, service := range plan.Services {
					routes[service.ServiceKey] = map[string]any{"exposure": "public", "serviceRef": service.ServiceKey, "host": service.Host}
				}
				routes["pocketid"] = map[string]any{"serviceRef": "id", "host": "id.owned.kombify.me"}
				candidateBytes, _ := json.Marshal(candidate)
				if err := os.WriteFile(path, candidateBytes, 0o600); err != nil {
					t.Fatal(err)
				}
				boundPath, _, bound, err := prepareCanonicalStackKitCLI(t.Context(), release.BinaryPath(), dir, path, []string{"--no-log", "--chdir", dir, "--spec", "candidate.json"}, time.Minute, StackKitArtifactGenerateRequest{StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", RuntimeTarget: &ManagedRuntimeTarget{PublicIP: "203.0.113.10"}})
				if scenario != "zone initial admitted" {
					if err == nil || registryWrite {
						t.Fatal("initial recovery escaped existing zone custody")
					}
					return
				}
				if err != nil || bound.Zone != "owned.kombify.me" || !exposed["vault"] || !registered["vault"] {
					t.Fatalf("initial recovery did not expose its admitted workload in the same zone: %v", err)
				}
				prepared, readErr := os.ReadFile(boundPath)
				if readErr != nil || !reflect.DeepEqual(prepared, candidateBytes) || filepath.Base(boundPath) != "stack-spec.address-bound.v2.json" {
					t.Fatalf("prepared node intent changed its established identity or Vault route: %v", readErr)
				}
				return
			}
			sender := &addressReadySender{t: t, ready: func() bool { return exposed["auth"] && exposed["photos"] && exposed["files"] && exposed["vault"] }}
			reads := 0
			originSender, originCertificates := newOriginAwareSender(sender)
			cfg := StackKitLifecycleConfig{Sender: originSender, OriginCertificates: originCertificates, AdvancedIssuer: &recordingAdvancedIssuer{}, releaseResolver: func() (*stackkitrelease.Release, error) { return &release, nil }, ManagedAddressAuthority: func(_ context.Context, req StackKitLifecycleRequest) (ManagedAddressRuntime, error) {
				reads++
				if req.TenantID != "tenant-1" || req.OwnerID != "owner-1" || req.StackID != "stack-1" || req.AgentID != "agent-1" {
					t.Fatal("runtime authority lost identity")
				}
				current := ManagedAddressRuntime{PublicIP: "203.0.113.10", LeaseID: "lease-1", ServerID: "server-1", Generation: 1, Rollout: StackKitRolloutBinding{StackKit: "cloud-kit", SpecPath: "bound.json"}}
				if strings.HasPrefix(scenario, "unleased") {
					current.LeaseID, current.PublicIP = "", ""
				}
				if scenario == "wrong rollout" {
					current.Rollout.SpecPath = "other.json"
				}
				if strings.HasSuffix(scenario, "changed generation") && reads > 1 {
					current.Generation = 2
				}
				return current, nil
			}}
			req := StackKitLifecycleRequest{StackID: "stack-1", StackKitInstanceID: "cloud-stack", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", OwnerApproved: true, Operation: StackKitLifecycleAdvancedChangeSet, StackKit: "cloud-kit", SpecPath: "bound.json", CandidateSpecJSON: []byte(`{"metadata":{"name":"cloud-stack"},"kit":{"slug":"cloud-kit"},"network":{"domain":{"base":"kombify.me","subdomainPrefix":"owned"}}}`)}
			if zone {
				req.CandidateSpecJSON = []byte(`{"metadata":{"name":"cloud-stack"},"kit":{"slug":"cloud-kit"},"network":{"domain":{"base":"owned.kombify.me"}}}`)
			}
			if scenario == "zone nested domain" {
				req.CandidateSpecJSON = []byte(`{"metadata":{"name":"cloud-stack"},"kit":{"slug":"cloud-kit"},"network":{"domain":{"base":"nested.owned.kombify.me"}}}`)
			}
			job := &Job{ID: "job-candidate-addresses", TargetID: req.StackID, Payload: StackKitLifecyclePayload(req)}
			err := StackKitLifecycleHandler(cfg)(t.Context(), job, NewQueue(0, nil))
			if scenario != "admitted" && scenario != "zone admitted" && scenario != "unleased unchanged" {
				if err == nil || len(sender.commands) != 0 {
					t.Fatalf("invalid authority dispatched mutation: %v", err)
				}
				if scenario != "changed generation" && mutated {
					t.Fatal("invalid authority modified service registry")
				}
				if strings.HasPrefix(scenario, "unleased") && registryWrite {
					t.Fatal("unleased candidate wrote registry state")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !registered["photos"] || !registered["files"] || !registered["vault"] || len(sender.commands) != 2 {
				t.Fatal("candidate applications were not registered and applied")
			}
			proof, _ := job.Snapshot().Result[ManagedAddressReconciliationResultField].(map[string]interface{})
			if scenario == "unleased unchanged" {
				if registryWrite || proof["status"] != "not_applicable" {
					t.Fatal("unleased unchanged operation acquired provider mutation authority")
				}
				return
			}
			want := []string{"owned-auth.kombify.me", "owned-files.kombify.me", "owned-photos.kombify.me", "owned-vault.kombify.me"}
			if zone {
				want = []string{"auth.owned.kombify.me", "files.owned.kombify.me", "photos.owned.kombify.me", "vault.owned.kombify.me"}
			}
			if proof["status"] != "registered" || proof["stack_ref"] != address.StackRef || proof["target_origin"] != origin || !reflect.DeepEqual(proof["hosts"], want) {
				t.Fatalf("missing exact route evidence: %v", proof)
			}
		})
	}
}
