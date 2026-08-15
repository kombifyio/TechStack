package stackrouting

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
)

type testManagedLeaseLister struct {
	leases []vmlease.Lease
	err    error
}

type testRolloutDispatcher struct {
	jobID string
	err   error
	calls []RolloutRequest
}

type testFastRolloutDispatcher struct {
	store *MemoryStore
	calls int
}

type testRecoveringRolloutDispatcher struct{ calls int }

func (d *testRecoveringRolloutDispatcher) DispatchRoutingRollout(context.Context, RolloutRequest) (*RolloutResult, error) {
	d.calls++
	if d.calls == 1 {
		return nil, errors.New("transient dispatch response loss")
	}
	return &RolloutResult{JobID: "job-recovered"}, nil
}

func (d *testFastRolloutDispatcher) DispatchRoutingRollout(ctx context.Context, req RolloutRequest) (*RolloutResult, error) {
	d.calls++
	const jobID = "job-fast"
	if _, err := d.store.MarkRolloutFinished(ctx, req.TenantID, req.StackID, req.RoutingRevision, jobID, RolloutCompleted, ""); err != nil {
		return nil, err
	}
	return &RolloutResult{JobID: jobID}, nil
}

func (d *testRolloutDispatcher) DispatchRoutingRollout(_ context.Context, req RolloutRequest) (*RolloutResult, error) {
	d.calls = append(d.calls, req)
	if d.err != nil {
		return nil, d.err
	}
	jobID := d.jobID
	if jobID == "" {
		jobID = "job-routing-1"
	}
	return &RolloutResult{JobID: jobID}, nil
}

func (l testManagedLeaseLister) ListByTenant(context.Context, string) ([]vmlease.Lease, error) {
	return l.leases, l.err
}

func testManagedRoutingLease(id, tenantID, ownerID, stackID string) vmlease.Lease {
	now := time.Now().UTC()
	return vmlease.Lease{
		ID:             vmlease.LeaseID(id),
		Subject:        vmlease.Subject{Kind: vmlease.SubjectUser, ID: ownerID, OrgID: tenantID},
		Resource:       vmlease.ResourceRef{ProviderID: monthlyruntime.ProviderIONOS, EngineVMID: "vm-1"},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyManual,
		ValidFrom:      now.Add(-time.Hour),
		ValidUntil:     now.Add(time.Hour),
		RenewedAt:      now,
		Metadata: monthlyruntime.NormalizeMetadata(map[string]string{
			"stack_id": stackID, "lease_provider": monthlyruntime.ProviderIONOS,
		}, serverruntime.RuntimeOfferingStandard),
	}
}

func routingFixture(t *testing.T, managed bool) (Service, Principal, *controlplane.MemoryStore, *MemoryStore) {
	t.Helper()
	resources := controlplane.NewMemoryStore()
	if _, err := resources.CreateStack(context.Background(), controlplane.CreateStackRequest{
		ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "auth0|owner", Name: "Demo", Status: "running",
		Config: map[string]any{"domain": "kombify.me"},
	}); err != nil {
		t.Fatalf("create stack: %v", err)
	}
	serverID := "server-1"
	if managed {
		serverID = runtimeidentity.LeaseServerID("lease-1")
	}
	server := controlplane.ServerRuntime{
		ID: serverID, TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "auth0|owner", Name: "Demo server",
	}
	if managed {
		server.LeaseID = "lease-1"
		server.ProviderRef = "ionos-managed:" + serverID
	} else {
		server.ProviderRef = "local"
	}
	if _, err := resources.UpsertServerRuntime(context.Background(), server); err != nil {
		t.Fatalf("create server: %v", err)
	}
	routing := NewMemoryStore()
	service := Service{Stacks: resources, Servers: resources, Store: routing}
	if managed {
		service.Leases = testManagedLeaseLister{leases: []vmlease.Lease{testManagedRoutingLease("lease-1", "tenant-1", "auth0|owner", "stack-1")}}
		service.Dispatcher = &testRolloutDispatcher{}
	}
	return service, Principal{TenantID: "tenant-1", OwnerSubjectID: "auth0|owner"}, resources, routing
}

func managedRoutingInput() EnsureInput {
	return EnsureInput{
		ServerID: runtimeidentity.LeaseServerID("lease-1"), LeaseID: "lease-1", Domain: "Kombified.COM.", Mode: ModeCustomDomain,
		Provenance: Provenance{Source: "byod", DNSProvider: "cloudflare", ZoneID: "zone-1"}, EnsureRollout: true,
	}
}

func TestServiceEnsureManagedTargetIsExactAndReplaySafe(t *testing.T) {
	service, principal, resources, _ := routingFixture(t, true)
	view, err := service.Ensure(context.Background(), principal, "stack-1", managedRoutingInput(), MutationOptions{IdempotencyKey: "attach-1"})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if view.Desired.Domain != "kombified.com" || view.Desired.Revision != 1 {
		t.Fatalf("desired = %#v", view.Desired)
	}
	if view.Rollout.Status != RolloutPending || view.Rollout.JobID != "job-routing-1" || view.Rollout.ReasonCode != "" || view.Observed.Applied {
		t.Fatalf("view claims convergence: %#v", view)
	}
	replay, err := service.Ensure(context.Background(), principal, "stack-1", managedRoutingInput(), MutationOptions{IdempotencyKey: "attach-1"})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !replay.IdempotentReplay || replay.Desired.Revision != 1 {
		t.Fatalf("replay = %#v", replay)
	}
	if calls := len(service.Dispatcher.(*testRolloutDispatcher).calls); calls != 1 {
		t.Fatalf("dispatcher calls = %d, want one idempotent dispatch", calls)
	}
	stack, err := resources.GetStack(context.Background(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatal(err)
	}
	if stack.Config["domain"] != "kombify.me" {
		t.Fatalf("immutable stack config was mutated: %#v", stack.Config)
	}
}

func TestServiceFastTerminalJobCannotBeRegressedToPendingAndReplayDoesNotDispatch(t *testing.T) {
	service, principal, _, routing := routingFixture(t, true)
	fast := &testFastRolloutDispatcher{store: routing}
	service.Dispatcher = fast
	first, err := service.Ensure(context.Background(), principal, "stack-1", managedRoutingInput(), MutationOptions{IdempotencyKey: "fast-key"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Rollout.Status != RolloutCompleted || first.Rollout.JobID != "job-fast" || first.Observed.Applied || first.Observed.Status != "pending" {
		t.Fatalf("fast terminal rollout regressed: %#v", first)
	}
	replay, err := service.Ensure(context.Background(), principal, "stack-1", managedRoutingInput(), MutationOptions{IdempotencyKey: "fast-key"})
	if err != nil {
		t.Fatal(err)
	}
	if fast.calls != 1 || !replay.IdempotentReplay || replay.Rollout.Status != RolloutCompleted || replay.Rollout.JobID != "job-fast" {
		t.Fatalf("terminal replay dispatched again: calls=%d replay=%#v", fast.calls, replay)
	}
}

func TestServiceNotRequestedReplayRecoversDispatchWithSameKey(t *testing.T) {
	service, principal, _, routing := routingFixture(t, true)
	dispatcher := &testRecoveringRolloutDispatcher{}
	service.Dispatcher = dispatcher
	if _, err := service.Ensure(context.Background(), principal, "stack-1", managedRoutingInput(), MutationOptions{IdempotencyKey: "recover-key"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("first dispatch error = %v, want ErrUnavailable", err)
	}
	before, err := routing.Get(context.Background(), principal.TenantID, "stack-1")
	if err != nil || before.RolloutStatus != RolloutNotRequested || before.RolloutJobID != "" {
		t.Fatalf("pre-recovery state=%#v err=%v", before, err)
	}
	recovered, err := service.Ensure(context.Background(), principal, "stack-1", managedRoutingInput(), MutationOptions{IdempotencyKey: "recover-key"})
	if err != nil {
		t.Fatal(err)
	}
	if dispatcher.calls != 2 || !recovered.IdempotentReplay || recovered.Rollout.Status != RolloutPending || recovered.Rollout.JobID != "job-recovered" {
		t.Fatalf("recovery calls=%d view=%#v", dispatcher.calls, recovered)
	}
}

func TestServiceEnsureRejectsWrongManagedLease(t *testing.T) {
	service, principal, _, _ := routingFixture(t, true)
	input := managedRoutingInput()
	input.LeaseID = "lease-other"
	_, err := service.Ensure(context.Background(), principal, "stack-1", input, MutationOptions{IdempotencyKey: "attach-1"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
}

func TestServiceEnsureRejectsExistingNonCanonicalManagedServerID(t *testing.T) {
	service, principal, resources, _ := routingFixture(t, true)
	if _, err := resources.UpsertServerRuntime(context.Background(), controlplane.ServerRuntime{
		ID: "legacy-server-row", TenantID: principal.TenantID, StackID: "stack-1",
		OwnerSubjectID: principal.OwnerSubjectID, LeaseID: "lease-1",
		ProviderRef: "ionos-managed:legacy-server-row", Name: "Legacy duplicate",
	}); err != nil {
		t.Fatal(err)
	}
	input := managedRoutingInput()
	input.ServerID = "legacy-server-row"
	_, err := service.Ensure(context.Background(), principal, "stack-1", input, MutationOptions{IdempotencyKey: "legacy-target"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}
	if calls := len(service.Dispatcher.(*testRolloutDispatcher).calls); calls != 0 {
		t.Fatalf("non-canonical target dispatched %d rollout(s)", calls)
	}
}

func TestServiceEnsureRejectsCrossOwnerAndCrossStackServer(t *testing.T) {
	service, principal, resources, _ := routingFixture(t, true)
	other := principal
	other.OwnerSubjectID = "auth0|other"
	if _, err := service.Ensure(context.Background(), other, "stack-1", managedRoutingInput(), MutationOptions{IdempotencyKey: "attach-1"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-owner error = %v", err)
	}
	if _, err := resources.CreateStack(context.Background(), controlplane.CreateStackRequest{
		ID: "stack-2", TenantID: "tenant-1", OwnerSubjectID: "auth0|owner", Name: "Other",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Ensure(context.Background(), principal, "stack-2", managedRoutingInput(), MutationOptions{IdempotencyKey: "attach-2"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-stack error = %v", err)
	}
}

func TestServiceEnsureLocalTargetNeedsNoFakeLease(t *testing.T) {
	service, principal, _, _ := routingFixture(t, false)
	input := managedRoutingInput()
	input.ServerID = "server-1"
	input.LeaseID = ""
	input.Provenance = Provenance{Source: "operator", DNSProvider: "manual"}
	input.EnsureRollout = false
	view, err := service.Ensure(context.Background(), principal, "stack-1", input, MutationOptions{IdempotencyKey: "local-1"})
	if err != nil {
		t.Fatalf("Ensure local: %v", err)
	}
	if view.Desired.LeaseID != "" || view.Rollout.Status != RolloutNotRequested {
		t.Fatalf("local view = %#v", view)
	}
	input.LeaseID = "fake-lease"
	if _, err := service.Ensure(context.Background(), principal, "stack-1", input, MutationOptions{IdempotencyKey: "local-2"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("fake lease error = %v", err)
	}
}

// Managed indicators are persisted in two vocabularies: the canonical vendor
// identity and the historical composite alias. Routing must fail closed on
// either, so a canonical-identity rename can never silently reopen the gap.
func TestServiceEnsureManagedIndicatorFailsClosedWithoutLease(t *testing.T) {
	for _, test := range []struct {
		name        string
		providerRef string
		metadata    map[string]any
	}{
		{name: "composite alias provider ref", providerRef: "centron-managed:missing-lease"},
		{name: "canonical provider ref", providerRef: monthlyruntime.ProviderCentron + ":missing-lease"},
		{name: "canonical lease provider metadata", providerRef: "local", metadata: map[string]any{"lease_provider": monthlyruntime.ProviderIONOS}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, principal, resources, _ := routingFixture(t, false)
			server, err := resources.GetServerRuntime(context.Background(), "tenant-1", "server-1")
			if err != nil {
				t.Fatal(err)
			}
			server.ProviderRef = test.providerRef
			server.Metadata = test.metadata
			if _, upsertErr := resources.UpsertServerRuntime(context.Background(), *server); upsertErr != nil {
				t.Fatal(upsertErr)
			}
			input := managedRoutingInput()
			input.ServerID = "server-1"
			input.LeaseID = ""
			_, err = service.Ensure(context.Background(), principal, "stack-1", input, MutationOptions{IdempotencyKey: "managed-missing"})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestServiceEnsureBackfillsCanonicalServerFromExactActiveLease(t *testing.T) {
	resources := controlplane.NewMemoryStore()
	if _, err := resources.CreateStack(context.Background(), controlplane.CreateStackRequest{
		ID: "stack-live", TenantID: "tenant-live", OwnerSubjectID: "owner-live", Name: "Live IONOS", Status: "provisioning",
	}); err != nil {
		t.Fatal(err)
	}
	lease := testManagedRoutingLease("lease-live", "tenant-live", "owner-live", "stack-live")
	serverID := runtimeidentity.LeaseServerID(string(lease.ID))
	routingStore := NewMemoryStore()
	service := Service{
		Stacks: resources, Servers: resources, Store: routingStore,
		Leases:     testManagedLeaseLister{leases: []vmlease.Lease{lease}},
		Dispatcher: &testRolloutDispatcher{jobID: "job-live"},
	}
	view, err := service.Ensure(context.Background(), Principal{TenantID: "tenant-live", OwnerSubjectID: "owner-live"}, "stack-live", EnsureInput{
		ServerID: serverID, LeaseID: string(lease.ID), Mode: ModeCustomDomain, Domain: "kombified.com",
		Provenance: Provenance{Source: "kombify-cloud", DNSProvider: "cloudflare", ZoneID: "zone-live"}, EnsureRollout: true,
	}, MutationOptions{IdempotencyKey: "live-routing"})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	server, err := resources.GetServerRuntime(context.Background(), "tenant-live", serverID)
	if err != nil {
		t.Fatalf("canonical server projection missing: %v", err)
	}
	if server.StackID != "stack-live" || server.OwnerSubjectID != "owner-live" || server.LeaseID != "lease-live" || server.LifecycleState != "enrolling" {
		t.Fatalf("canonical projection = %#v", server)
	}
	if server.Metadata["authority"] != "runtime-lease" || server.Metadata["projection_backfill"] != true {
		t.Fatalf("projection provenance = %#v", server.Metadata)
	}
	if view.Desired.ServerID != serverID || view.Desired.LeaseID != "lease-live" || view.Rollout.JobID != "job-live" || view.Observed.Applied {
		t.Fatalf("routing view = %#v", view)
	}
}

func TestServiceEnsureMissingServerBackfillRejectsWrongLeaseOwnerAndServerID(t *testing.T) {
	for _, test := range []struct {
		name      string
		lease     vmlease.Lease
		serverID  string
		wantError error
	}{
		{
			name:     "wrong owner",
			lease:    testManagedRoutingLease("lease-owner", "tenant-1", "owner-other", "stack-1"),
			serverID: runtimeidentity.LeaseServerID("lease-owner"), wantError: ErrForbidden,
		},
		{
			name:     "wrong canonical server id",
			lease:    testManagedRoutingLease("lease-server", "tenant-1", "owner-1", "stack-1"),
			serverID: "server-caller-invented", wantError: ErrInvalid,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			resources := controlplane.NewMemoryStore()
			if _, err := resources.CreateStack(context.Background(), controlplane.CreateStackRequest{
				ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Exact",
			}); err != nil {
				t.Fatal(err)
			}
			service := Service{Stacks: resources, Servers: resources, Store: NewMemoryStore(), Leases: testManagedLeaseLister{leases: []vmlease.Lease{test.lease}}}
			_, err := service.Ensure(context.Background(), Principal{TenantID: "tenant-1", OwnerSubjectID: "owner-1"}, "stack-1", EnsureInput{
				ServerID: test.serverID, LeaseID: string(test.lease.ID), Domain: "kombified.com",
				Provenance: Provenance{Source: "cloud", DNSProvider: "cloudflare", ZoneID: "zone-1"},
			}, MutationOptions{IdempotencyKey: "denied"})
			if !errors.Is(err, test.wantError) {
				t.Fatalf("error = %v, want %v", err, test.wantError)
			}
			if _, getErr := resources.GetServerRuntime(context.Background(), "tenant-1", test.serverID); !errors.Is(getErr, controlplane.ErrNotFound) {
				t.Fatalf("unsafe server projection persisted: %v", getErr)
			}
		})
	}
}

func TestServiceRevisionAndIdempotencyConflicts(t *testing.T) {
	service, principal, _, _ := routingFixture(t, true)
	if _, err := service.Ensure(context.Background(), principal, "stack-1", managedRoutingInput(), MutationOptions{IdempotencyKey: "key-1"}); err != nil {
		t.Fatal(err)
	}
	changed := managedRoutingInput()
	changed.Domain = "other.example"
	zero := int64(0)
	if _, err := service.Ensure(context.Background(), principal, "stack-1", changed, MutationOptions{IdempotencyKey: "key-2", ExpectedRevision: &zero}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("revision error = %v", err)
	}
	if _, err := service.Ensure(context.Background(), principal, "stack-1", changed, MutationOptions{IdempotencyKey: "key-1"}); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency error = %v", err)
	}
}

func TestServiceIdenticalDesiredRoutePreservesPendingRolloutTruth(t *testing.T) {
	service, principal, _, _ := routingFixture(t, true)
	first, err := service.Ensure(context.Background(), principal, "stack-1", managedRoutingInput(), MutationOptions{IdempotencyKey: "key-1"})
	if err != nil {
		t.Fatal(err)
	}
	input := managedRoutingInput()
	input.EnsureRollout = false
	second, err := service.Ensure(context.Background(), principal, "stack-1", input, MutationOptions{IdempotencyKey: "key-2"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Desired.Revision != first.Desired.Revision || second.Rollout.Status != RolloutPending || second.Rollout.JobID != first.Rollout.JobID {
		t.Fatalf("identical desired route erased rollout truth: first=%#v second=%#v", first, second)
	}
	if calls := len(service.Dispatcher.(*testRolloutDispatcher).calls); calls != 1 {
		t.Fatalf("dispatcher calls = %d, want one", calls)
	}
}

func TestServiceChangedDesiredRouteIsBlockedWhileRolloutPending(t *testing.T) {
	service, principal, _, routing := routingFixture(t, true)
	if _, err := service.Ensure(context.Background(), principal, "stack-1", managedRoutingInput(), MutationOptions{IdempotencyKey: "key-1"}); err != nil {
		t.Fatal(err)
	}
	changed := managedRoutingInput()
	changed.Domain = "other.example"
	if _, err := service.Ensure(context.Background(), principal, "stack-1", changed, MutationOptions{IdempotencyKey: "key-2"}); !errors.Is(err, ErrRolloutInProgress) {
		t.Fatalf("changed pending route error = %v, want ErrRolloutInProgress", err)
	}
	state, err := routing.Get(context.Background(), principal.TenantID, "stack-1")
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision != 1 || state.Domain != "kombified.com" || state.RolloutStatus != RolloutPending {
		t.Fatalf("pending routing state changed: %#v", state)
	}
}

func TestMemoryStoreFinishedRolloutAwaitsIndependentObservationAndUnlocksChange(t *testing.T) {
	service, principal, _, routing := routingFixture(t, true)
	first, err := service.Ensure(context.Background(), principal, "stack-1", managedRoutingInput(), MutationOptions{IdempotencyKey: "key-1"})
	if err != nil {
		t.Fatal(err)
	}
	finished, err := routing.MarkRolloutFinished(context.Background(), principal.TenantID, "stack-1", first.Desired.Revision, first.Rollout.JobID, RolloutCompleted, "")
	if err != nil {
		t.Fatal(err)
	}
	view := viewFor(finished, false)
	if view.Rollout.Status != RolloutCompleted || view.Observed.Applied || view.Observed.AppliedRevision != 0 || view.Observed.Status != "pending" {
		t.Fatalf("completed routing view = %#v", view)
	}
	changed := managedRoutingInput()
	changed.Domain = "other.example"
	changed.EnsureRollout = false
	next, err := service.Ensure(context.Background(), principal, "stack-1", changed, MutationOptions{IdempotencyKey: "key-2"})
	if err != nil {
		t.Fatalf("changed route after terminal rollout: %v", err)
	}
	if next.Desired.Revision != first.Desired.Revision+1 || next.Desired.Domain != "other.example" {
		t.Fatalf("next route = %#v", next)
	}
}
