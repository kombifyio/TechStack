package jobs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/stackrouting"
	"github.com/kombifyio/techstack/pkg/unifier"
)

func TestDeployApplyRoutingOverlayTargetsDerivedSpecAndStackKitsHandoff(t *testing.T) {
	t.Parallel()

	const (
		tenantID = "tenant-routing"
		ownerID  = "owner-routing"
		stackID  = "stack-routing"
		serverID = "server-routing"
		leaseID  = "lease-routing"
	)
	store := stackrouting.NewMemoryStore()
	putJobRoutingState(t, store, stackrouting.DesiredState{
		TenantID: tenantID, OwnerSubjectID: ownerID, StackID: stackID,
		ServerID: serverID, LeaseID: leaseID, Mode: stackrouting.ModeCustomDomain,
		Domain: "kombified.com",
		Provenance: stackrouting.Provenance{
			Source: "kombify-cloud", DNSProvider: "cloudflare",
			ZoneID: "zone-new", ExternalDomainID: "domain-product-1",
		},
		RolloutStatus: stackrouting.RolloutPending,
	})

	persister, err := unifier.NewSpecPersisterWithPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	intent := []byte("name: immutable\nnetwork:\n  domain: kombify.me\n")
	if _, _, saveErr := persister.SaveIntentBytes(intent); saveErr != nil {
		t.Fatal(saveErr)
	}
	if _, _, saveErr := persister.SaveStackSpecBytes([]byte(`name: derived
stackkit: cloud-kit
domain: kombify.me
network:
  domain: kombify.me
metadata:
  domain: kombify.me
  dns_provider: stale-provider
  dns_zone_id: stale-zone
  routing_lease_id: stale-lease
user_config:
  domain: kombify.me
  metadata:
    domain: kombify.me
    subdomainPrefix: stale-prefix
config:
  network:
    domain: kombify.me
  metadata:
    domain: kombify.me
    routing_source: stale-source
`)); saveErr != nil {
		t.Fatal(saveErr)
	}
	spec := &core.KombinationSpec{
		Network: core.NetworkSpec{Domain: "kombify.me"},
		Metadata: map[string]string{
			"domain": "kombify.me", "dns_provider": "stale-provider",
			"dns_zone_id": "stale-zone", "subdomainPrefix": "stale-prefix",
		},
	}
	job := &Job{TargetID: stackID, Payload: map[string]interface{}{
		tenantIDField: tenantID, "owner_id": ownerID, leaseIDField: leaseID,
		routingDispatchKindField:   routingDispatchKindExact,
		routingIdempotencyKeyField: "routing-job-1", routingRevisionField: int64(1),
		routingServerIDField: serverID, routingLeaseIDField: leaseID,
	}}

	path, err := deployApplyRoutingOverlay(context.Background(), &ProvisionConfig{RoutingStore: store}, job, persister, spec)
	if err != nil {
		t.Fatalf("deployApplyRoutingOverlay: %v", err)
	}
	if path == "" || path != persister.GetStackSpecPath() {
		t.Fatalf("handoff path = %q, want %q", path, persister.GetStackSpecPath())
	}
	if spec.Network.Domain != "kombified.com" || spec.Metadata["domain"] != "kombified.com" || spec.Metadata["dns_zone_id"] != "zone-new" {
		t.Fatalf("derived KombinationSpec routing = %#v", spec)
	}
	if spec.Metadata["routing_external_domain_id"] != "domain-product-1" {
		t.Fatalf("derived provenance = %#v", spec.Metadata)
	}
	gotIntent, err := persister.LoadIntentBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotIntent, intent) {
		t.Fatalf("immutable intent changed:\n%s", gotIntent)
	}
	handoff, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stale := range []string{"kombify.me", "stale-provider", "stale-zone", "stale-lease", "stale-prefix", "stale-source"} {
		if strings.Contains(string(handoff), stale) {
			t.Fatalf("stale routing value %q survived:\n%s", stale, handoff)
		}
	}
	for _, want := range []string{"domain: kombified.com", `subdomainPrefix: ""`, "dns_zone_id: zone-new", "routing_external_domain_id: domain-product-1"} {
		if !strings.Contains(string(handoff), want) {
			t.Fatalf("handoff missing %q:\n%s", want, handoff)
		}
	}
	if job.Result["routing_revision"] != int64(1) || job.Result["routing_server_id"] != serverID || job.Result["routing_lease_id"] != leaseID {
		t.Fatalf("routing evidence = %#v", job.Result)
	}
}

func TestDeployApplyRoutingOverlayFailsClosedWhenJobRevisionIsStale(t *testing.T) {
	t.Parallel()

	store := stackrouting.NewMemoryStore()
	putJobRoutingState(t, store, stackrouting.DesiredState{
		TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1",
		ServerID: "server-1", LeaseID: "lease-1", Mode: stackrouting.ModeCustomDomain,
		Domain: "kombified.com", Provenance: stackrouting.Provenance{Source: "cloud", DNSProvider: "manual"},
	})
	persister, err := unifier.NewSpecPersisterWithPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("name: derived\nstackkit: cloud-kit\ndomain: kombify.me\n")
	if _, _, saveErr := persister.SaveStackSpecBytes(original); saveErr != nil {
		t.Fatal(saveErr)
	}
	spec := &core.KombinationSpec{Network: core.NetworkSpec{Domain: "kombify.me"}}
	job := &Job{TargetID: "stack-1", Payload: map[string]interface{}{
		tenantIDField: "tenant-1", "owner_id": "owner-1", leaseIDField: "lease-1",
		routingDispatchKindField:   routingDispatchKindExact,
		routingIdempotencyKeyField: "routing-job-stale", routingRevisionField: int64(2),
		routingServerIDField: "server-1", routingLeaseIDField: "lease-1",
	}}

	path, err := deployApplyRoutingOverlay(context.Background(), &ProvisionConfig{RoutingStore: store}, job, persister, spec)
	if err == nil || !strings.Contains(err.Error(), "immutable receipt mismatch") {
		t.Fatalf("error = %v, want immutable receipt mismatch", err)
	}
	if path != "" || spec.Network.Domain != "kombify.me" {
		t.Fatalf("stale job mutated derived state: path=%q spec=%#v", path, spec)
	}
	handoff, readErr := os.ReadFile(persister.GetStackSpecPath())
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(handoff, original) {
		t.Fatalf("stale job mutated handoff:\n%s", handoff)
	}
}

func TestReconcileRoutingRolloutOutcomeMarksExactJobTerminal(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		rolloutErr error
		wantStatus string
		wantReason string
	}{
		{name: "completed", wantStatus: stackrouting.RolloutCompleted},
		{name: "failed", rolloutErr: errors.New("rollout failed"), wantStatus: stackrouting.RolloutFailed, wantReason: stackrouting.ReasonRolloutFailed},
		{name: "waiting", rolloutErr: newManagedRuntimeEnrollmentPendingError("lease-1", time.Second, errors.New("enrollment pending")), wantStatus: stackrouting.RolloutPending},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := stackrouting.NewMemoryStore()
			putJobRoutingState(t, store, stackrouting.DesiredState{
				TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1",
				ServerID: "server-1", LeaseID: "lease-1", Mode: stackrouting.ModeCustomDomain,
				Domain: "kombified.com", Provenance: stackrouting.Provenance{Source: "cloud", DNSProvider: "manual"},
			})
			if _, dispatchErr := store.MarkRolloutDispatched(context.Background(), "tenant-1", "stack-1", 1, "job-1"); dispatchErr != nil {
				t.Fatal(dispatchErr)
			}
			job := &Job{ID: "job-1", TargetID: "stack-1", Payload: map[string]interface{}{
				routingDispatchKindField: routingDispatchKindExact,
				tenantIDField:            "tenant-1", routingIdempotencyKeyField: "key-1", routingRevisionField: int64(1),
				routingServerIDField: "server-1", routingLeaseIDField: "lease-1",
			}}
			if reconcileErr := reconcileRoutingRolloutOutcome(context.Background(), &ProvisionConfig{RoutingStore: store}, job, test.rolloutErr); reconcileErr != nil {
				t.Fatal(reconcileErr)
			}
			state, err := store.Get(context.Background(), "tenant-1", "stack-1")
			if err != nil {
				t.Fatal(err)
			}
			if state.RolloutStatus != test.wantStatus || state.ReasonCode != test.wantReason || state.RolloutJobID != "job-1" {
				t.Fatalf("terminal state = %#v", state)
			}
		})
	}
}

func TestGenericDeployAppliesRoutingWithoutReconcilingPriorRoutingJob(t *testing.T) {
	t.Parallel()

	store := stackrouting.NewMemoryStore()
	putJobRoutingState(t, store, stackrouting.DesiredState{
		TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1",
		ServerID: "server-1", LeaseID: "lease-1", Mode: stackrouting.ModeCustomDomain,
		Domain: "kombified.com", Provenance: stackrouting.Provenance{Source: "cloud", DNSProvider: "manual"},
	})
	if _, dispatchErr := store.MarkRolloutDispatched(context.Background(), "tenant-1", "stack-1", 1, "job-routing-1"); dispatchErr != nil {
		t.Fatal(dispatchErr)
	}
	if _, finishErr := store.MarkRolloutFinished(context.Background(), "tenant-1", "stack-1", 1, "job-routing-1", stackrouting.RolloutCompleted, ""); finishErr != nil {
		t.Fatal(finishErr)
	}
	persister, err := unifier.NewSpecPersisterWithPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, saveErr := persister.SaveStackSpecBytes([]byte("name: derived\nstackkit: cloud-kit\ndomain: kombify.me\n")); saveErr != nil {
		t.Fatal(saveErr)
	}
	job := &Job{ID: "job-generic", TargetID: "stack-1", Payload: map[string]interface{}{
		tenantIDField: "tenant-1", "owner_id": "owner-1", leaseIDField: "lease-1",
	}}
	if _, applyErr := deployApplyRoutingOverlay(context.Background(), &ProvisionConfig{RoutingStore: store}, job, persister, &core.KombinationSpec{}); applyErr != nil {
		t.Fatalf("generic deploy overlay: %v", applyErr)
	}
	if _, marked := job.Result[routingDispatchKindField]; marked {
		t.Fatalf("generic deploy acquired an exact routing marker: %#v", job.Result)
	}
	if reconcileErr := reconcileRoutingRolloutOutcome(context.Background(), &ProvisionConfig{RoutingStore: store}, job, nil); reconcileErr != nil {
		t.Fatalf("generic deploy reconciliation: %v", reconcileErr)
	}
	state, err := store.Get(context.Background(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatal(err)
	}
	if state.RolloutStatus != stackrouting.RolloutCompleted || state.RolloutJobID != "job-routing-1" {
		t.Fatalf("generic deploy changed prior routing job truth: %#v", state)
	}
}

func TestCopyRoutingDispatchReceiptPreservesDurableReplayMarkers(t *testing.T) {
	t.Parallel()
	dst := map[string]interface{}{}
	copyRoutingDispatchReceipt(dst, map[string]interface{}{
		routingDispatchKindField:   routingDispatchKindExact,
		routingIdempotencyKeyField: "routing-key", routingRevisionField: int64(7),
		routingServerIDField: "server-7", routingLeaseIDField: "lease-7",
	}, map[string]interface{}{routingRevisionField: int64(6)})
	if dst[routingDispatchKindField] != routingDispatchKindExact || dst[routingIdempotencyKeyField] != "routing-key" || dst[routingRevisionField] != int64(7) || dst[routingServerIDField] != "server-7" || dst[routingLeaseIDField] != "lease-7" {
		t.Fatalf("routing receipt = %#v", dst)
	}
}

func TestDeployApplyRoutingOverlayFailsClosedOnLeaseMismatch(t *testing.T) {
	t.Parallel()

	store := stackrouting.NewMemoryStore()
	putJobRoutingState(t, store, stackrouting.DesiredState{
		TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1",
		ServerID: "server-1", LeaseID: "lease-expected", Mode: stackrouting.ModeCustomDomain,
		Domain: "kombified.com", Provenance: stackrouting.Provenance{Source: "cloud", DNSProvider: "cloudflare"},
	})
	persister, err := unifier.NewSpecPersisterWithPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("name: derived\nstackkit: cloud-kit\ndomain: kombify.me\n")
	if _, _, saveErr := persister.SaveStackSpecBytes(original); saveErr != nil {
		t.Fatal(saveErr)
	}
	spec := &core.KombinationSpec{Network: core.NetworkSpec{Domain: "kombify.me"}}
	job := &Job{TargetID: "stack-1", Payload: map[string]interface{}{
		tenantIDField: "tenant-1", "owner_id": "owner-1", leaseIDField: "lease-other",
	}}

	path, err := deployApplyRoutingOverlay(context.Background(), &ProvisionConfig{RoutingStore: store}, job, persister, spec)
	if err == nil || !strings.Contains(err.Error(), "routing overlay lease mismatch") {
		t.Fatalf("error = %v, want lease mismatch", err)
	}
	if path != "" || spec.Network.Domain != "kombify.me" {
		t.Fatalf("mismatched overlay mutated derived state: path=%q spec=%#v", path, spec)
	}
	handoff, readErr := os.ReadFile(persister.GetStackSpecPath())
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(handoff, original) {
		t.Fatalf("mismatched overlay mutated handoff:\n%s", handoff)
	}
}

func putJobRoutingState(t *testing.T, store *stackrouting.MemoryStore, desired stackrouting.DesiredState) {
	t.Helper()
	if desired.RolloutStatus == "" {
		desired.RolloutStatus = stackrouting.RolloutNotRequested
	}
	if _, putErr := store.Put(context.Background(), stackrouting.PutRequest{
		DesiredState: desired, IdempotencyKey: "job-routing-test", RequestHash: "job-routing-test",
	}); putErr != nil {
		t.Fatal(putErr)
	}
}
