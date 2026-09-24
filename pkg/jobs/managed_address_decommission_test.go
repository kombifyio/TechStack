package jobs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/kombifyio/techstack/pkg/kombifyme"
)

type orderedLeaseDecommissioner struct {
	inner  *fakeManagedLeaseDecommissioner
	record func(string)
}

func (d orderedLeaseDecommissioner) DecommissionManagedLeases(ctx context.Context, req ManagedLeaseDecommissionRequest) (*ManagedLeaseDecommissionResult, error) {
	d.record("provider_decommission")
	d.inner.requests = append(d.inner.requests, req)
	return d.inner.result, d.inner.err
}

// A stack destroy removes the stack's managed kombify.me addresses before the
// provider server is released, and never proceeds without proven absence.
func TestDestroyHandler_DeregistersManagedAddressesBeforeProviderTeardown(t *testing.T) {
	address, err := kombifyme.NewManagedAddress("org-1", "user-1", "stack-managed")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name          string
		deregister    int
		remainingLeft bool
		wantErr       bool
		resumed       bool
	}{
		{name: "absent", deregister: http.StatusOK},
		{name: "resumed destroy keeps the first pass's zone release", deregister: http.StatusOK, resumed: true},
		{name: "registry denies", deregister: http.StatusConflict, wantErr: true},
		{name: "route survives readback", deregister: http.StatusOK, remainingLeft: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			order := []string{}
			passes := 0
			record := func(step string) { mu.Lock(); order = append(order, step); mu.Unlock() }
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/subdomains/deregister":
					record("deregister")
					var body struct {
						Binding map[string]string `json:"binding"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					if body.Binding["owner_ref"] != address.OwnerRef || body.Binding["stack_ref"] != address.StackRef {
						t.Errorf("deregistration binding = %v, want the stack's binding", body.Binding)
					}
					w.WriteHeader(tc.deregister)
					if tc.deregister != http.StatusOK {
						_, _ = w.Write([]byte(`{"error_code":"binding_conflict"}`))
						return
					}
					passes++
					if passes > 1 {
						// An idempotent repeat finds nothing left to remove.
						_, _ = w.Write([]byte(`{"remaining":0,"deleted":[],"origin_records":[],"zone_records":[]}`))
						return
					}
					_, _ = w.Write([]byte(`{"remaining":0,"deleted":[{"fqdn":"sh-m1-device-auth.kombify.me"}],"origin_records":[{"name":"o-203-0-113-10-443.origin.kombify.me","state":"absent"}],"zone_records":[{"name":"*.m1-device.kombify.me","state":"absent"}]}`))
				case r.Method == http.MethodGet && r.URL.Path == "/subdomains" && r.URL.Query().Get("binding_stack_ref") == address.StackRef:
					record("readback")
					if tc.remainingLeft {
						_, _ = w.Write([]byte(`[{"name":"sh-m1-device-auth","subdomain_kind":"service","target_addr":"https://203.0.113.10:443"}]`))
						return
					}
					_, _ = w.Write([]byte(`[]`))
				default:
					http.Error(w, "unexpected request", http.StatusNotFound)
				}
			}))
			defer server.Close()
			t.Setenv("KOMBIFY_ME_API_BASE", server.URL)
			t.Setenv("KOMBIFY_ME_API_KEY", "test-key")

			decommissioner := &fakeManagedLeaseDecommissioner{result: &ManagedLeaseDecommissionResult{
				Decommissioned: 1,
				LeaseIDs:       []string{"lease-managed"},
				Proofs: []ManagedLeaseDecommissionProof{
					testManagedLeaseDecommissionProof("stack-managed", "org-1", "lease-managed", ManagedLeaseDecommissionObservedDecommissioned, ""),
				},
			}}
			handler := DestroyHandler(&ProvisionConfig{
				WorkDir:        t.TempDir(),
				RuntimeActions: RuntimeActions{LeaseDecommissioner: orderedLeaseDecommissioner{inner: decommissioner, record: record}},
			})
			job := &Job{
				ID: "destroy-managed-addresses", Type: JobTypeDestroy, TargetID: "stack-managed", TargetName: "homelab",
				Payload: map[string]interface{}{
					"owner_id": "user-1", "tenant_id": "org-1", ManagedRuntimeDecommissionRequiredField: true,
				},
			}
			queue := &Queue{jobs: map[string]*Job{job.ID: job}}

			err := handler(context.Background(), job, queue)
			if tc.resumed && err == nil {
				// A destroy waiting on the provider resumes from the top.
				err = handler(context.Background(), job, queue)
			}

			mu.Lock()
			defer mu.Unlock()
			if tc.wantErr {
				if err == nil {
					t.Fatal("destroy succeeded without proven address absence")
				}
				for _, step := range order {
					if step == "provider_decommission" {
						t.Fatalf("provider teardown ran before address absence was proven: %v", order)
					}
				}
				return
			}
			if len(order) < 3 || order[0] != "deregister" || order[1] != "readback" || order[2] != "provider_decommission" {
				t.Fatalf("lifecycle order = %v, want addresses removed and read back before provider teardown", order)
			}
			evidence, _ := job.Snapshot().Result[ManagedAddressDeregistrationResultField].(map[string]any)
			if evidence["status"] != "absent" || evidence["stack_ref"] != address.StackRef {
				t.Fatalf("deregistration evidence = %#v", evidence)
			}
			if tc.resumed {
				encoded, err := json.Marshal(evidence)
				if err != nil {
					t.Fatal(err)
				}
				var recorded struct {
					Deleted []string                       `json:"deleted"`
					Zones   []struct{ Name, State string } `json:"zone_records"`
					Origins []struct{ Name, State string } `json:"origin_records"`
				}
				if err := json.Unmarshal(encoded, &recorded); err != nil {
					t.Fatal(err)
				}
				if len(recorded.Zones) == 0 || recorded.Zones[0].Name != "*.m1-device.kombify.me" || recorded.Zones[0].State != "absent" ||
					len(recorded.Deleted) == 0 || recorded.Deleted[0] != "sh-m1-device-auth.kombify.me" ||
					len(recorded.Origins) == 0 || recorded.Origins[0].Name != "o-203-0-113-10-443.origin.kombify.me" || recorded.Origins[0].State != "absent" {
					t.Fatalf("resumed destroy lost the released records: %+v", recorded)
				}
			}
		})
	}
}
