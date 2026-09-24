package jobs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/kombifyme"
)

func TestAllocateManagedAddressPlanBindsExactRuntimeWithoutCredentialPayload(t *testing.T) {
	for _, tc := range []struct {
		name, prefix string
		refusal      int
		wrongBase    bool
		// Install zones (D-72): the switch decides for a new install; an
		// install kombify.me already reports as a zone stays one.
		zoneSwitch     bool
		existingLayout string
		wantZone       bool
	}{
		{name: "new address"}, {name: "existing address", prefix: "owned-original"},
		{name: "new install zone", zoneSwitch: true, wantZone: true},
		{name: "existing flat install with the switch on", zoneSwitch: true, existingLayout: "flat"},
		{name: "existing install zone with the switch off", existingLayout: "zone", wantZone: true},
		{name: "foreign address", prefix: "owned-original", refusal: http.StatusForbidden},
		{name: "missing address", prefix: "owned-original", refusal: http.StatusNotFound},
		{name: "unexpected binding", prefix: "owned-original", wrongBase: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			address, err := kombifyme.NewManagedAddress("tenant-1", "owner-1", "stack-1")
			if err != nil {
				t.Fatal(err)
			}
			serviceMutated := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				_ = json.NewDecoder(r.Body).Decode(&body)
				encoded, _ := json.Marshal(body)
				if strings.Contains(string(encoded), "never-send") {
					t.Error("runtime credential reached registry payload")
				}
				if strings.Contains(string(encoded), `"homelab_name":"demo"`) {
					t.Error("free-text stack name selected the managed address")
				}
				if binding, _ := body["binding"].(map[string]any); r.Method == http.MethodPost && (binding["owner_ref"] != address.OwnerRef || binding["stack_ref"] != address.StackRef) {
					t.Errorf("registration binding = %v, want the stack's owner binding", body["binding"])
				}
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/auth/me":
					_, _ = w.Write([]byte(`{"device_fingerprint":"device1"}`))
				case r.Method == http.MethodPost && r.URL.Path == "/subdomains/auto-register":
					name := "sh-" + address.Label + "-device1"
					if requested, _ := body["base_subdomain_name"].(string); requested != "" {
						if tc.refusal != 0 {
							http.Error(w, "unavailable", tc.refusal)
							return
						}
						if !tc.wrongBase {
							name = requested
						}
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"id": "base-id", "name": name, "fqdn": name + ".kombify.me", "subdomain_kind": "base", "binding_owner_ref": address.OwnerRef, "binding_stack_ref": address.StackRef, "layout": tc.existingLayout})
				case r.Method == http.MethodPost && r.URL.Path == "/subdomains/auto-register/service":
					serviceMutated = true
					service := body["service_name"].(string)
					name := body["base_subdomain_name"].(string) + "-" + service
					if body["layout"] == "zone" {
						name = service + "." + body["base_subdomain_name"].(string)
					}
					if body["local_addr"] != "https://203.0.113.10:443" {
						t.Errorf("registry origin = %v", body["local_addr"])
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"id": "service-" + service, "parent_id": "base-id", "name": name, "fqdn": name + ".kombify.me", "target_type": "proxy", "target_addr": "https://203.0.113.10:443", "binding_owner_ref": address.OwnerRef, "binding_stack_ref": address.StackRef})
				case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/expose"):
					serviceMutated = true
					parts := strings.Split(r.URL.Path, "/")
					_, _ = fmt.Fprintf(w, `{"id":%q,"exposed":true}`, parts[len(parts)-2])
				default:
					http.Error(w, "unexpected request", http.StatusNotFound)
				}
			}))
			defer server.Close()
			t.Setenv("KOMBIFY_ME_API_BASE", server.URL)
			t.Setenv("KOMBIFY_ME_API_KEY", "test-key")
			if tc.zoneSwitch {
				t.Setenv(kombifyme.InstallZonesEnv, "true")
			}
			binding, err := allocateManagedAddressPlan(t.Context(), stackKitAddressPlan{
				APIVersion: stackKitAddressPlanV1, Provider: addressModeKombifyMe, StackName: "demo", Domain: addressModeKombifyMeDomain, SubdomainPrefix: tc.prefix,
				Services: []stackKitAddressPlanService{{ServiceKey: "auth"}, {ServiceKey: "base"}},
			}, &ManagedRuntimeTarget{PublicIP: "203.0.113.10", SSHPrivateKey: "never-send"}, address)
			if tc.refusal != 0 || tc.wrongBase {
				if err == nil || serviceMutated {
					t.Fatalf("unavailable bound address was not preserved: err=%v mutated=%v", err, serviceMutated)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := tc.prefix
			if want == "" {
				want = "sh-" + address.Label + "-device1"
			}
			if tc.wantZone {
				if binding.Prefix != want || binding.Zone != want+".kombify.me" || binding.Hosts["auth"] != "auth."+want+".kombify.me" || binding.Hosts["base"] != "base."+want+".kombify.me" {
					t.Fatalf("binding = %#v, expected install zone %s.kombify.me", binding, want)
				}
				return
			}
			if binding.Prefix != want || binding.Zone != "" || binding.Hosts["auth"] != want+"-auth.kombify.me" || binding.Hosts["base"] != want+"-base.kombify.me" {
				t.Fatalf("binding = %#v, expected owned prefix %s", binding, want)
			}
		})
	}
}
