package routes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/monitoring"
	"github.com/kombifyio/techstack/pkg/nodehandoff"
	"github.com/kombifyio/techstack/pkg/pairingtoken"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/vmleases"
	"github.com/kombifyio/techstack/pkg/workerauth"
	"github.com/pocketbase/pocketbase/core"
)

func TestReadWorkerRegistrationRequestValidatesEarlyBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantErr  string
		wantHost string
		wantTok  string
	}{
		{name: "malformed json", body: `{`, wantErr: "Invalid request body"},
		{name: "missing token", body: `{"hostname":"worker-1"}`, wantErr: "token is required"},
		{name: "missing hostname", body: `{"token":"pair"}`, wantErr: "hostname is required"},
		{name: "trims token and host", body: `{"token":" pair ","hostname":" worker-1 ","os":"linux","arch":"amd64"}`, wantTok: "pair", wantHost: "worker-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, recorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/register", tt.body)

			got, validationErr, ok := readWorkerRegistrationRequest(event)
			if tt.wantErr != "" {
				if ok || !strings.Contains(validationErr, tt.wantErr) {
					t.Fatalf("expected validation error containing %q, got ok=%v error=%q", tt.wantErr, ok, validationErr)
				}
				if recorder.Body.Len() != 0 {
					t.Fatalf("parser wrote response before handler boundary: %s", recorder.Body.String())
				}
				return
			}
			if !ok {
				t.Fatalf("unexpected validation error: %s", validationErr)
			}
			if got.Token != tt.wantTok || got.Hostname != tt.wantHost {
				t.Fatalf("unexpected request: %+v", got)
			}
		})
	}
}

func TestWorkerListRequiresAuthenticationBeforeStoreLookup(t *testing.T) {
	event, recorder := workerRouteTestEvent(http.MethodGet, "/api/v1/workers", "")

	err := (workerRouteHandlers{}).list(event)
	if err == nil {
		t.Fatal("list must return an error once it has written the refusal")
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestWorkerListPaginationUsesDeterministicWorkerIDOrder(t *testing.T) {
	const (
		tenantID = "tenant-1"
		ownerID  = "owner-1"
	)
	store := controlplane.NewMemoryStore()
	for _, workerID := range []string{"worker-z", "worker-a", "worker-m"} {
		if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
			ID: workerID, TenantID: tenantID, OwnerSubjectID: ownerID,
			Status: "approved", Approved: true,
		}); err != nil {
			t.Fatalf("seed worker %s: %v", workerID, err)
		}
	}

	handler := workerRouteHandlers{wst: store, managedRuntimeLeases: fakeManagedRuntimeLeaseLister{}}
	got := make([]string, 0, 3)
	var inventorySHA256 string
	for page := 1; page <= 3; page++ {
		event, recorder := workerRouteTestEvent(http.MethodGet, "/api/v1/workers?page="+strconv.Itoa(page)+"&per_page=1", "")
		event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: ownerID, OrgID: tenantID}))
		if err := handler.list(event); err != nil {
			t.Fatalf("list page %d: %v", page, err)
		}
		for _, field := range []string{
			`"managed_runtime_active_lease_ids":[]`,
			`"managed_runtime_inactive_lease_ids":[]`,
			`"managed_runtime_lease_generation_digests":[]`,
			`"managed_runtime_duplicate_lease_ids":[]`,
			`"managed_runtime_attachment_conflict_lease_ids":[]`,
		} {
			if !strings.Contains(recorder.Body.String(), field) {
				t.Fatalf("page %d missing canonical empty authority array %s: %s", page, field, recorder.Body.String())
			}
		}
		var envelope struct {
			Data []map[string]any `json:"data"`
			Meta struct {
				Total                           int      `json:"total"`
				Page                            int      `json:"page"`
				PerPage                         int      `json:"per_page"`
				ManagedRuntimeInventoryComplete *bool    `json:"managed_runtime_inventory_complete"`
				WorkerInventorySHA256           string   `json:"worker_inventory_sha256"`
				ManagedRuntimeActiveLeaseIDs    []string `json:"managed_runtime_active_lease_ids"`
				ManagedRuntimeInactiveLeaseIDs  []string `json:"managed_runtime_inactive_lease_ids"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode page %d: %v", page, err)
		}
		if envelope.Meta.Total != 3 || envelope.Meta.Page != page || envelope.Meta.PerPage != 1 || len(envelope.Data) != 1 {
			t.Fatalf("unexpected page %d envelope: %#v", page, envelope)
		}
		if envelope.Meta.ManagedRuntimeInventoryComplete == nil || !*envelope.Meta.ManagedRuntimeInventoryComplete {
			t.Fatalf("page %d managed runtime completeness = %#v, want true with an empty authority snapshot", page, envelope.Meta.ManagedRuntimeInventoryComplete)
		}
		if len(envelope.Meta.ManagedRuntimeActiveLeaseIDs) != 0 || len(envelope.Meta.ManagedRuntimeInactiveLeaseIDs) != 0 {
			t.Fatalf("page %d managed runtime authority IDs = active %v inactive %v, want empty", page, envelope.Meta.ManagedRuntimeActiveLeaseIDs, envelope.Meta.ManagedRuntimeInactiveLeaseIDs)
		}
		if page == 1 {
			inventorySHA256 = envelope.Meta.WorkerInventorySHA256
		} else if envelope.Meta.WorkerInventorySHA256 != inventorySHA256 {
			t.Fatalf("page %d inventory digest = %q, want %q", page, envelope.Meta.WorkerInventorySHA256, inventorySHA256)
		}
		got = append(got, envelope.Data[0]["id"].(string))
	}
	if len(inventorySHA256) != 64 {
		t.Fatalf("worker inventory digest = %q, want sha256", inventorySHA256)
	}
	if want := []string{"worker-a", "worker-m", "worker-z"}; !slices.Equal(got, want) {
		t.Fatalf("worker page order = %v, want %v", got, want)
	}
}

func TestWorkerListProjectsManagedMonthlyRuntimeLeases(t *testing.T) {
	active := createStackOperationsTestLease("lease-1", "owner-1", "owner-1", "stack-1", "enrolled")
	active.Resource.EngineVMID = "engine-active"
	active.Metadata[vmleases.MetadataKeyResourceGenerationID] = "11111111-1111-4111-8111-111111111111"
	canceled := createStackOperationsTestLease("lease-canceled", "owner-1", "owner-1", "stack-2", "enrolled")
	canceled.Resource.EngineVMID = "engine-canceled"
	canceled.Metadata[vmleases.MetadataKeyResourceGenerationID] = "22222222-2222-4222-8222-222222222222"
	now := time.Now().UTC()
	canceled.CancelledAt = &now
	canceled.DesiredState = vmlease.DesiredStateStopped
	lister := fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{
		active,
		canceled,
		createStackOperationsTestLease("lease-other-owner", "owner-2", "owner-2", "stack-1", "enrolled"),
	}}
	event, recorder := workerRouteTestEvent(http.MethodGet, "/api/v1/workers", "")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1"}))

	err := (workerRouteHandlers{wst: controlplane.NewMemoryStore(), managedRuntimeLeases: lister}).list(event)
	if err != nil {
		t.Fatalf("list returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data []map[string]any `json:"data"`
		Meta struct {
			ManagedRuntimeInventoryComplete      *bool                                       `json:"managed_runtime_inventory_complete"`
			ManagedRuntimeActiveLeaseIDs         []string                                    `json:"managed_runtime_active_lease_ids"`
			ManagedRuntimeInactiveLeaseIDs       []string                                    `json:"managed_runtime_inactive_lease_ids"`
			ManagedRuntimeLeaseGenerationDigests []ksapi.ManagedRuntimeLeaseGenerationDigest `json:"managed_runtime_lease_generation_digests"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := len(envelope.Data); got != 2 {
		t.Fatalf("workers = %d, want active plus canceled authority inventory", got)
	}
	row := envelope.Data[0]
	if row["id"] != "lease:lease-1" || row["source"] != managedRuntimeInventorySource || row["lease_id"] != "lease-1" {
		t.Fatalf("unexpected worker projection: %#v", row)
	}
	if row["approved"] != true || row["assignable"] != true {
		t.Fatalf("managed projection flags wrong: %#v", row)
	}
	if row["managed_runtime_lease_authority_state"] != string(vmleases.LeaseAuthorityStateNativeActive) {
		t.Fatalf("managed projection authority state wrong: %#v", row)
	}
	if envelope.Meta.ManagedRuntimeInventoryComplete == nil || !*envelope.Meta.ManagedRuntimeInventoryComplete {
		t.Fatalf("managed runtime inventory completeness = %#v, want true", envelope.Meta.ManagedRuntimeInventoryComplete)
	}
	if !slices.Equal(envelope.Meta.ManagedRuntimeActiveLeaseIDs, []string{"lease-1"}) || !slices.Equal(envelope.Meta.ManagedRuntimeInactiveLeaseIDs, []string{"lease-canceled"}) {
		t.Fatalf("managed runtime authority IDs = active %v inactive %v", envelope.Meta.ManagedRuntimeActiveLeaseIDs, envelope.Meta.ManagedRuntimeInactiveLeaseIDs)
	}
	wantGenerationDigests := managedRuntimeLeaseGenerationDigestProofs("owner-1", []vmlease.Lease{active, canceled})
	if !slices.Equal(envelope.Meta.ManagedRuntimeLeaseGenerationDigests, wantGenerationDigests) {
		t.Fatalf("managed runtime generation digests = %#v, want %#v", envelope.Meta.ManagedRuntimeLeaseGenerationDigests, wantGenerationDigests)
	}
}

type failingManagedRuntimeLeaseLister struct{}

func (failingManagedRuntimeLeaseLister) ListByTenant(context.Context, string) ([]vmlease.Lease, error) {
	return nil, errors.New("lease store unavailable")
}

func (failingManagedRuntimeLeaseLister) ListInventoryByTenant(context.Context, string) ([]vmleases.LeaseInventoryRecord, error) {
	return nil, errors.New("lease store unavailable")
}

func TestWorkerListFailsClosedWhenManagedRuntimeAuthorityFails(t *testing.T) {
	event, recorder := workerRouteTestEvent(http.MethodGet, "/api/v1/workers", "")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	handler := workerRouteHandlers{
		wst:                  controlplane.NewMemoryStore(),
		managedRuntimeLeases: failingManagedRuntimeLeaseLister{},
	}
	if err := handler.list(event); err != nil {
		t.Fatalf("list returned router error: %v", err)
	}
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s, want 500", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "lease store unavailable") {
		t.Fatalf("worker inventory leaked lease-store details: %s", recorder.Body.String())
	}
}

func TestWorkerListFailsClosedOnDuplicateManagedRuntimeAuthorityLease(t *testing.T) {
	lease := createStackOperationsTestLease("lease-duplicate", "tenant-1", "owner-1", "stack-1", "enrolled")
	event, recorder := workerRouteTestEvent(http.MethodGet, "/api/v1/workers", "")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	handler := workerRouteHandlers{
		wst: controlplane.NewMemoryStore(),
		managedRuntimeLeases: fakeManagedRuntimeLeaseLister{
			leases: []vmlease.Lease{lease, lease},
		},
	}
	if err := handler.list(event); err != nil {
		t.Fatalf("list returned router error: %v", err)
	}
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s, want 500", recorder.Code, recorder.Body.String())
	}
}

func TestManagedRuntimeWorkerAuthorityUsesLeaseState(t *testing.T) {
	active := createStackOperationsTestLease("lease-active", "tenant-1", "owner-1", "stack-1", "enrolled")
	canceled := createStackOperationsTestLease("lease-canceled", "tenant-1", "owner-1", "stack-2", "enrolled")
	now := time.Now().UTC()
	canceled.CancelledAt = &now
	canceled.DesiredState = vmlease.DesiredStateStopped
	records := map[string]vmleases.LeaseInventoryRecord{
		string(active.ID): nativeActiveManagedRuntimeRecord(active),
		string(canceled.ID): {
			Lease:              canceled,
			ExecutionAuthority: vmleases.LeaseExecutionAuthorityTechStackProviderControl,
			AuthorityState:     vmleases.LeaseAuthorityStateNativeInactive,
		},
	}

	for _, test := range []struct {
		leaseID string
		want    string
	}{
		{leaseID: string(active.ID), want: string(vmleases.LeaseAuthorityStateNativeActive)},
		{leaseID: string(canceled.ID), want: string(vmleases.LeaseAuthorityStateNativeInactive)},
		{leaseID: "lease-missing", want: "missing"},
	} {
		row := map[string]any{}
		annotateManagedRuntimeWorkerAuthority(row, test.leaseID, records)
		if row["managed_runtime_lease_authority_state"] != test.want {
			t.Fatalf("lease %s authority state = %#v, want %q", test.leaseID, row, test.want)
		}
	}

	activeIDs, inactiveIDs, duplicateIDs := managedRuntimeLeaseAuthorityIDs([]vmleases.LeaseInventoryRecord{records[string(canceled.ID)], records[string(active.ID)]})
	if !slices.Equal(activeIDs, []string{"lease-active"}) || !slices.Equal(inactiveIDs, []string{"lease-canceled"}) || len(duplicateIDs) != 0 {
		t.Fatalf("authority IDs = active %v inactive %v duplicate %v", activeIDs, inactiveIDs, duplicateIDs)
	}
	activeIDs, inactiveIDs, duplicateIDs = managedRuntimeLeaseAuthorityIDs([]vmleases.LeaseInventoryRecord{records[string(active.ID)], records[string(canceled.ID)], records[string(active.ID)]})
	if !slices.Equal(activeIDs, []string{"lease-active"}) || !slices.Equal(inactiveIDs, []string{"lease-canceled"}) || !slices.Equal(duplicateIDs, []string{"lease-active"}) {
		t.Fatalf("duplicate authority IDs = active %v inactive %v duplicate %v", activeIDs, inactiveIDs, duplicateIDs)
	}
}

func TestWorkerInventoryBindingChangesOnAttachmentMutation(t *testing.T) {
	base := []map[string]any{{"id": "worker-1", "stack_id": "stack-1", "lease_id": "lease-1", "cpu_percent": 1}}
	first, err := workerInventoryBindingSHA256(base, []string{"lease-1"}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("first digest: %v", err)
	}
	base[0]["cpu_percent"] = 99
	metricsOnly, err := workerInventoryBindingSHA256(base, []string{"lease-1"}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("metrics digest: %v", err)
	}
	if metricsOnly != first {
		t.Fatal("volatile metrics changed the worker inventory binding")
	}
	base[0]["stack_id"] = "stack-2"
	attachmentChanged, err := workerInventoryBindingSHA256(base, []string{"lease-1"}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("attachment digest: %v", err)
	}
	if attachmentChanged == first {
		t.Fatal("worker attachment mutation did not change the inventory binding")
	}
	base[0]["stack_id"] = "stack-1"
	authorityChanged, err := workerInventoryBindingSHA256(base, nil, []string{"lease-1"}, nil, nil, nil)
	if err != nil {
		t.Fatalf("authority digest: %v", err)
	}
	if authorityChanged == first {
		t.Fatal("managed runtime authority mutation did not change the inventory binding")
	}
	conflictChanged, err := workerInventoryBindingSHA256(base, []string{"lease-1"}, nil, nil, []string{"lease-1"}, nil)
	if err != nil {
		t.Fatalf("attachment conflict digest: %v", err)
	}
	if conflictChanged == first {
		t.Fatal("managed runtime attachment conflict did not change the inventory binding")
	}
	generationChanged, err := workerInventoryBindingSHA256(base, []string{"lease-1"}, nil, nil, nil, []ksapi.ManagedRuntimeLeaseGenerationDigest{{
		LeaseID:                  "lease-1",
		ResourceGenerationDigest: strings.Repeat("a", 64),
	}})
	if err != nil {
		t.Fatalf("generation digest: %v", err)
	}
	if generationChanged == first {
		t.Fatal("managed runtime resource generation did not change the inventory binding")
	}
}

func TestManagedRuntimeLeaseGenerationDigestProofsAreCanonicalAndOmitIncompleteLeases(t *testing.T) {
	leaseB := createStackOperationsTestLease("lease-b", "tenant-1", "owner-1", "stack-b", "enrolled")
	leaseB.Resource.EngineVMID = "engine-b"
	leaseB.Metadata[vmleases.MetadataKeyResourceGenerationID] = "22222222-2222-4222-8222-222222222222"
	leaseA := createStackOperationsTestLease("lease-a", "tenant-1", "owner-1", "stack-a", "enrolled")
	leaseA.Resource.EngineVMID = "engine-a"
	leaseA.Metadata[vmleases.MetadataKeyResourceGenerationID] = "11111111-1111-4111-8111-111111111111"
	incomplete := createStackOperationsTestLease("lease-incomplete", "tenant-1", "owner-1", "stack-c", "enrolled")

	proofs := managedRuntimeLeaseGenerationDigestProofs("tenant-1", []vmlease.Lease{leaseB, incomplete, leaseA})
	if len(proofs) != 2 || proofs[0].LeaseID != "lease-a" || proofs[1].LeaseID != "lease-b" {
		t.Fatalf("generation proofs = %#v, want canonical complete lease A/B proofs", proofs)
	}
	for _, proof := range proofs {
		if len(proof.ResourceGenerationDigest) != 64 {
			t.Fatalf("generation proof digest = %q, want SHA-256", proof.ResourceGenerationDigest)
		}
	}
}

func TestWorkerListAttestsForeignOrgScopedAttachmentWithoutLeakingWorker(t *testing.T) {
	const (
		tenantID = "tenant-1"
		ownerID  = "owner-1"
		leaseID  = "lease-org-shared"
	)
	lease := createStackOperationsTestLease(leaseID, tenantID, tenantID, "stack-shared", "enrolled")
	lease.Subject.Kind = vmlease.SubjectOrg
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
		ID:             "worker-private-detail",
		TenantID:       tenantID,
		OwnerSubjectID: "owner-2",
		StackID:        "stack-shared",
		Hostname:       "foreign-private-hostname",
		Capabilities: map[string]any{
			runtimeLeaseIDKey: leaseID,
			"server_id":       runtimeidentity.LeaseServerID(leaseID),
		},
	}); err != nil {
		t.Fatalf("seed foreign org worker: %v", err)
	}

	envelope, raw := listWorkerAttachmentProof(t, workerRouteHandlers{
		wst:                  store,
		managedRuntimeLeases: fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{lease}},
	}, tenantID, ownerID)
	if !slices.Equal(envelope.Meta.ManagedRuntimeAttachmentConflictLeaseIDs, []string{leaseID}) {
		t.Fatalf("attachment conflicts = %v, want %s", envelope.Meta.ManagedRuntimeAttachmentConflictLeaseIDs, leaseID)
	}
	if strings.Contains(raw, "worker-private-detail") || strings.Contains(raw, "foreign-private-hostname") {
		t.Fatalf("worker response leaked foreign worker details: %s", raw)
	}
}

func TestWorkerListUnboundLegacyAttachmentDoesNotClaimNativeCustodyConflict(t *testing.T) {
	const (
		tenantID = "tenant-1"
		ownerID  = "owner-1"
		leaseID  = "lease-unbound"
	)
	lease := createStackOperationsTestLease(leaseID, tenantID, ownerID, "stack-unbound", "enrolled")
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
		ID:             "worker-legacy",
		TenantID:       tenantID,
		OwnerSubjectID: ownerID,
		StackID:        "stack-unbound",
		Capabilities: map[string]any{
			runtimeLeaseIDKey: leaseID,
			"server_id":       "legacy-server-id",
		},
	}); err != nil {
		t.Fatalf("seed legacy worker: %v", err)
	}

	envelope, _ := listWorkerAttachmentProof(t, workerRouteHandlers{
		wst: store,
		managedRuntimeLeases: fakeManagedRuntimeLeaseLister{inventory: []vmleases.LeaseInventoryRecord{{
			Lease: lease, AuthorityState: vmleases.LeaseAuthorityStateUnbound,
		}}},
	}, tenantID, ownerID)
	if len(envelope.Meta.ManagedRuntimeActiveLeaseIDs) != 0 {
		t.Fatalf("active authority = %v, want none", envelope.Meta.ManagedRuntimeActiveLeaseIDs)
	}
	if len(envelope.Meta.ManagedRuntimeAttachmentConflictLeaseIDs) != 0 {
		t.Fatalf("attachment conflicts = %v, unbound legacy record has no native custody", envelope.Meta.ManagedRuntimeAttachmentConflictLeaseIDs)
	}
}

func TestManagedRuntimeAttachmentConflictsBindVisibleIdentities(t *testing.T) {
	leaseA := createStackOperationsTestLease("lease-a", "tenant-1", "owner-1", "stack-a", "enrolled")
	leaseB := createStackOperationsTestLease("lease-b", "tenant-1", "owner-1", "stack-b", "enrolled")
	leaseC := createStackOperationsTestLease("lease-c", "tenant-1", "owner-1", "stack-b", "enrolled")
	visible := []vmlease.Lease{leaseC, leaseB, leaseA}

	tests := []struct {
		name   string
		worker controlplane.Worker
		want   []string
	}{
		{
			name: "raw lease A with canonical server B binds both",
			worker: controlplane.Worker{
				OwnerSubjectID: "owner-1", StackID: "stack-a",
				Capabilities: map[string]any{runtimeLeaseIDKey: "lease-a", "server_id": runtimeidentity.LeaseServerID("lease-b")},
			},
			want: []string{"lease-a", "lease-b"},
		},
		{
			name: "arbitrary server mismatch binds only raw visible lease",
			worker: controlplane.Worker{
				OwnerSubjectID: "owner-1", StackID: "stack-a",
				Capabilities: map[string]any{runtimeLeaseIDKey: "lease-a", "server_id": "server-arbitrary"},
			},
			want: []string{"lease-a"},
		},
		{
			name: "unknown raw lease with canonical visible server binds visible lease",
			worker: controlplane.Worker{
				OwnerSubjectID: "owner-1", StackID: "stack-private",
				Capabilities: map[string]any{runtimeLeaseIDKey: "lease-private", "server_id": runtimeidentity.LeaseServerID("lease-b")},
			},
			want: []string{"lease-b"},
		},
		{
			name: "stack-only identity binds every ambiguous visible lease",
			worker: controlplane.Worker{
				OwnerSubjectID: "owner-1", StackID: "stack-b",
				Capabilities: map[string]any{runtimeLeaseIDKey: "lease-private", "server_id": "server-private"},
			},
			want: []string{"lease-b", "lease-c"},
		},
		{
			name: "own canonical attachment remains conflict free",
			worker: controlplane.Worker{
				OwnerSubjectID: "owner-1", StackID: "stack-a",
				Capabilities: map[string]any{runtimeLeaseIDKey: "lease-a", "server_id": runtimeidentity.LeaseServerID("lease-a")},
			},
			want: []string{},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := managedRuntimeAttachmentConflictLeaseIDs([]controlplane.Worker{test.worker}, visible, visible, "owner-1")
			if !slices.Equal(got, test.want) {
				t.Fatalf("attachment conflicts = %v, want %v", got, test.want)
			}
		})
	}
}

func TestWorkerListBindsForeignPrivateActiveLeaseToVisibleSharedStack(t *testing.T) {
	const (
		tenantID      = "tenant-1"
		ownerID       = "owner-1"
		visibleLease  = "lease-visible"
		privateLease  = "lease-private"
		sharedStackID = "stack-shared"
	)
	visible := createStackOperationsTestLease(visibleLease, tenantID, ownerID, sharedStackID, "enrolled")
	foreignPrivate := createStackOperationsTestLease(privateLease, tenantID, "owner-2", sharedStackID, "enrolled")

	envelope, raw := listWorkerAttachmentProof(t, workerRouteHandlers{
		wst: controlplane.NewMemoryStore(),
		managedRuntimeLeases: fakeManagedRuntimeLeaseLister{
			leases: []vmlease.Lease{foreignPrivate, visible},
		},
	}, tenantID, ownerID)

	if !slices.Equal(envelope.Meta.ManagedRuntimeActiveLeaseIDs, []string{visibleLease}) {
		t.Fatalf("visible active authority = %v, want only %s", envelope.Meta.ManagedRuntimeActiveLeaseIDs, visibleLease)
	}
	if !slices.Equal(envelope.Meta.ManagedRuntimeAttachmentConflictLeaseIDs, []string{visibleLease}) {
		t.Fatalf("attachment conflicts = %v, want visible shared-stack lease", envelope.Meta.ManagedRuntimeAttachmentConflictLeaseIDs)
	}
	if strings.Contains(raw, privateLease) || strings.Contains(raw, "owner-2") {
		t.Fatalf("worker response leaked private lease authority: %s", raw)
	}
}

func TestWorkerListForeignUserScopedAttachmentDoesNotLeakOrChangeHash(t *testing.T) {
	const (
		tenantID      = "tenant-1"
		ownerID       = "owner-1"
		privateLease  = "lease-private"
		privateWorker = "worker-private"
	)
	visible := createStackOperationsTestLease("lease-visible", tenantID, ownerID, "stack-visible", "enrolled")
	foreignPrivate := createStackOperationsTestLease(privateLease, tenantID, "owner-2", "stack-private", "enrolled")
	store := controlplane.NewMemoryStore()
	handler := workerRouteHandlers{
		wst:                  store,
		managedRuntimeLeases: fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{visible, foreignPrivate}},
	}
	before, _ := listWorkerAttachmentProof(t, handler, tenantID, ownerID)

	if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
		ID:             privateWorker,
		TenantID:       tenantID,
		OwnerSubjectID: "owner-2",
		StackID:        "stack-private",
		Hostname:       "private-hostname",
		Capabilities: map[string]any{
			runtimeLeaseIDKey: privateLease,
			"server_id":       runtimeidentity.LeaseServerID(privateLease),
		},
	}); err != nil {
		t.Fatalf("seed foreign private worker: %v", err)
	}
	after, raw := listWorkerAttachmentProof(t, handler, tenantID, ownerID)

	if len(after.Meta.ManagedRuntimeAttachmentConflictLeaseIDs) != 0 {
		t.Fatalf("foreign private attachment leaked through conflicts: %v", after.Meta.ManagedRuntimeAttachmentConflictLeaseIDs)
	}
	if after.Meta.WorkerInventorySHA256 != before.Meta.WorkerInventorySHA256 {
		t.Fatalf("foreign private attachment changed caller digest: before=%s after=%s", before.Meta.WorkerInventorySHA256, after.Meta.WorkerInventorySHA256)
	}
	if strings.Contains(raw, privateLease) || strings.Contains(raw, privateWorker) || strings.Contains(raw, "private-hostname") {
		t.Fatalf("worker response leaked foreign private attachment: %s", raw)
	}
}

type workerAttachmentProofEnvelope struct {
	Data []map[string]any `json:"data"`
	Meta struct {
		WorkerInventorySHA256                    string   `json:"worker_inventory_sha256"`
		ManagedRuntimeActiveLeaseIDs             []string `json:"managed_runtime_active_lease_ids"`
		ManagedRuntimeAttachmentConflictLeaseIDs []string `json:"managed_runtime_attachment_conflict_lease_ids"`
	} `json:"meta"`
}

func listWorkerAttachmentProof(t *testing.T, handler workerRouteHandlers, tenantID, ownerID string) (workerAttachmentProofEnvelope, string) {
	t.Helper()
	event, recorder := workerRouteTestEvent(http.MethodGet, "/api/v1/workers", "")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: ownerID, OrgID: tenantID}))
	if err := handler.list(event); err != nil {
		t.Fatalf("list workers: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	var envelope workerAttachmentProofEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode worker attachment proof: %v", err)
	}
	return envelope, recorder.Body.String()
}

func TestWorkerListReplacesManagedLeaseProjectionWithPersistedInventory(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "worker-secret")
	const (
		tenantID = "tenant-1"
		ownerID  = "owner-1"
		leaseID  = "lease-centron-1"
	)
	store := controlplane.NewMemoryStore()
	handler := workerRouteHandlers{
		wst:           store,
		registryStore: store,
		managedRuntimeLeases: fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{
			createStackOperationsTestLease(leaseID, tenantID, ownerID, "stack-1", "enrolled"),
		}},
	}
	listWorkers := func(t *testing.T) []map[string]any {
		t.Helper()
		event, recorder := workerRouteTestEvent(http.MethodGet, "/api/v1/workers", "")
		event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: ownerID, OrgID: tenantID}))
		if err := handler.list(event); err != nil {
			t.Fatalf("list: %v", err)
		}
		if recorder.Code != http.StatusOK {
			t.Fatalf("list status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
		}
		var envelope struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode worker list: %v", err)
		}
		return envelope.Data
	}

	wantServerID := runtimeidentity.LeaseServerID(leaseID)
	beforeInventory := listWorkers(t)
	if len(beforeInventory) != 1 || beforeInventory[0]["id"] != "lease:"+leaseID || beforeInventory[0]["server_id"] != wantServerID {
		t.Fatalf("expected one virtual lease projection before inventory, got %#v", beforeInventory)
	}

	for _, agentID := range []string{"agent-first", "agent-retry"} {
		token, err := workerauth.Issue(workerauth.SecretFromEnv(), workerauth.Claims{
			TenantID: tenantID, OwnerID: ownerID, StackID: "stack-1", LeaseID: leaseID,
			ServerID: wantServerID, RuntimeAgentID: agentID,
		}, time.Now().UTC(), time.Hour)
		if err != nil {
			t.Fatalf("issue inventory token: %v", err)
		}
		if _, upsertErr := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
			ID: agentID, TenantID: tenantID, OwnerSubjectID: ownerID, StackID: "stack-1",
			Status: "approved", Approved: true,
			Resources: map[string]any{"agent_token_sha256": workerauth.SHA256Hex(token)},
			Capabilities: map[string]any{
				"server_id": wantServerID, runtimeLeaseIDKey: leaseID,
			},
		}); upsertErr != nil {
			t.Fatalf("seed enrolled worker: %v", upsertErr)
		}
		event, recorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/"+agentID+"/inventory", `{"source_epoch":"`+agentID+`-epoch","source_sequence":1,"observed_at":"2026-07-21T00:00:00Z","server_id":"`+wantServerID+`","runtime_agent_id":"`+agentID+`","host":{"hostname":"centron-`+agentID+`"}}`)
		event.Request.SetPathValue("id", agentID)
		event.Request.Header.Set("Authorization", "Bearer "+token)
		if err := handler.inventory(event); err != nil {
			t.Fatalf("inventory: %v", err)
		}
		if recorder.Code != http.StatusOK {
			t.Fatalf("inventory status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
		}
	}

	afterInventory := listWorkers(t)
	if len(afterInventory) != 1 {
		t.Fatalf("workers after retry = %d, want one persisted server: %#v", len(afterInventory), afterInventory)
	}
	row := afterInventory[0]
	if row["source"] != workerRegistryInventorySource || row["lease_id"] != leaseID || row["server_id"] != wantServerID || !strings.HasPrefix(row["id"].(string), "agent-") {
		t.Fatalf("persisted managed server lost its canonical lease identity: %#v", row)
	}
	event, recorder := workerRouteTestEvent(http.MethodGet, "/api/v1/workers", "")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: ownerID, OrgID: tenantID}))
	if err := handler.list(event); err != nil {
		t.Fatalf("list duplicate proof: %v", err)
	}
	var duplicateEnvelope struct {
		Meta struct {
			ManagedRuntimeDuplicateLeaseIDs []string `json:"managed_runtime_duplicate_lease_ids"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &duplicateEnvelope); err != nil {
		t.Fatalf("decode duplicate proof: %v", err)
	}
	if !slices.Equal(duplicateEnvelope.Meta.ManagedRuntimeDuplicateLeaseIDs, []string{leaseID}) {
		t.Fatalf("duplicate managed lease IDs = %v, want %s", duplicateEnvelope.Meta.ManagedRuntimeDuplicateLeaseIDs, leaseID)
	}
}

func TestWorkerListDoesNotApproveManagedLeaseWithoutRuntimeTarget(t *testing.T) {
	lease := createStackOperationsTestLease("lease-1", "owner-1", "owner-1", "stack-1", "enrolled")
	delete(lease.Metadata, "public_ip")
	lister := fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{lease}}
	event, recorder := workerRouteTestEvent(http.MethodGet, "/api/v1/workers", "")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1"}))

	err := (workerRouteHandlers{wst: controlplane.NewMemoryStore(), managedRuntimeLeases: lister}).list(event)
	if err != nil {
		t.Fatalf("list returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := len(envelope.Data); got != 1 {
		t.Fatalf("workers = %d, want projected managed runtime row", got)
	}
	row := envelope.Data[0]
	if row["approved"] != false || row["assignable"] != false || row["ip"] != "" {
		t.Fatalf("managed projection without runtime target should stay non-assignable and targetless: %#v", row)
	}
}

func TestWorkerListKeepsUnenrolledManagedRuntimeLeasesNonAssignable(t *testing.T) {
	lister := fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{
		createStackOperationsTestLease("lease-pending", "owner-1", "owner-1", "stack-1", "pending"),
		createStackOperationsTestLease("lease-failed", "owner-1", "owner-1", "stack-1", "failed"),
	}}
	event, recorder := workerRouteTestEvent(http.MethodGet, "/api/v1/workers", "")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1"}))

	err := (workerRouteHandlers{wst: controlplane.NewMemoryStore(), managedRuntimeLeases: lister}).list(event)
	if err != nil {
		t.Fatalf("list returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := len(envelope.Data); got != 2 {
		t.Fatalf("workers = %d, want pending and failed managed runtime rows", got)
	}
	for _, row := range envelope.Data {
		if row["source"] != managedRuntimeInventorySource || row["assignable"] != false || row["approved"] != false {
			t.Fatalf("unenrolled managed runtime row should be visible but non-assignable: %#v", row)
		}
	}
}

func TestWorkerApproveRequiresAuthenticationBeforeStoreLookup(t *testing.T) {
	event, recorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/worker-1/approve", "")
	event.Request.SetPathValue("id", "worker-1")

	err := (workerRouteHandlers{}).approve(event)
	if err == nil {
		t.Fatal("approve must return an error once it has written the refusal")
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

func TestWorkerRegisterUsesControlPlaneStore(t *testing.T) {
	store := controlplane.NewMemoryStore()
	rawToken, tokenHash, generateErr := pairingtoken.Generate("tenant-1")
	if generateErr != nil {
		t.Fatalf("generate pairing token: %v", generateErr)
	}
	hash := sha256.Sum256([]byte(rawToken))
	if got := hex.EncodeToString(hash[:]); got != tokenHash {
		t.Fatalf("generated hash = %q, want %q", tokenHash, got)
	}
	expiresAt := time.Now().Add(time.Hour)
	if _, err := store.UpsertPairingToken(t.Context(), controlplane.PairingToken{
		ID:             "pair-1",
		TenantID:       "tenant-1",
		StackID:        "stack-1",
		OwnerSubjectID: "owner-1",
		Name:           "homelab enrollment",
		TokenHash:      tokenHash,
		Status:         "active",
		ExpiresAt:      &expiresAt,
		Metadata: map[string]any{
			nodehandoff.KeyServerNodeRole:         "worker",
			nodehandoff.KeyRequestedServices:      []string{"ollama", "transcode"},
			nodehandoff.KeyServerRemoteHost:       "worker-1.lan",
			nodehandoff.KeyServerRemoteUser:       "ubuntu",
			nodehandoff.KeyServerRemotePort:       2222,
			nodehandoff.KeyServerRemoteCredential: "ssh-key:worker-1",
			nodehandoff.KeyServerRemoteUseSudo:    true,
		},
	}); err != nil {
		t.Fatalf("UpsertPairingToken: %v", err)
	}

	event, recorder := workerRouteTestEvent(
		http.MethodPost,
		"/api/v1/workers/register",
		`{"token":"`+rawToken+`","hostname":"node-1","os":"linux","arch":"amd64","cpu_cores":4,"ram_mb":8192,"disk_gb":120,"docker_version":"26.1.0","type":"main","provider":"local","tags":"docker-desktop,local-e2e"}`,
	)
	issuer := &recordingWorkerAgentIdentityIssuer{}
	registerErr := (workerRouteHandlers{wst: store, agentIdentityIssuer: issuer}).register(event)
	if registerErr != nil {
		t.Fatalf("register returned router error: %v", registerErr)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
	}

	workerID := workerStoreID("tenant-1", tokenHash, "node-1")
	if issuer.request.AgentID != workerID || issuer.request.TenantID != "tenant-1" ||
		strings.Join(issuer.request.AllowedCommandClasses, ",") != "health_check,get_logs,stackkit" ||
		issuer.request.EnrolledBy != "pairing-redemption" {
		t.Fatalf("identity issue request = %#v", issuer.request)
	}
	var registrationEnvelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &registrationEnvelope); err != nil {
		t.Fatalf("decode registration response: %v", err)
	}
	if _, ok := registrationEnvelope.Data["grpc_mtls"].(map[string]any); !ok {
		t.Fatalf("registration response missing grpc_mtls: %s", recorder.Body.String())
	}
	worker, err := store.GetWorker(t.Context(), "tenant-1", workerID)
	if err != nil {
		t.Fatalf("GetWorker: %v", err)
	}
	if worker.StackID != "stack-1" || worker.OwnerSubjectID != "owner-1" || worker.CPUCores != 4 || worker.RAMMB != 8192 {
		t.Fatalf("unexpected worker: %#v", worker)
	}
	// A2: a stack-scoped pairing token IS the owner's approval — the BYOS worker
	// must register already approved + stack-assigned so it deploys without a
	// second manual /approve click and without ErrNoAssignedWorkers.
	if !worker.Approved || worker.Status != "approved" || worker.ApprovedAt == nil {
		t.Fatalf("stack-scoped BYOS registration must be auto-approved, got approved=%v status=%q approvedAt=%v", worker.Approved, worker.Status, worker.ApprovedAt)
	}
	if worker.Type != "worker" {
		t.Fatalf("worker type = %q, want worker from pairing metadata", worker.Type)
	}
	services := nodehandoff.ServiceKeysFromAny(worker.Capabilities[nodehandoff.KeyRequestedServices])
	if strings.Join(services, ",") != "ollama,transcode" {
		t.Fatalf("worker requested services = %#v, want ollama/transcode", services)
	}
	if worker.Resources[nodehandoff.KeyServerRemoteHost] != "worker-1.lan" || worker.Resources[nodehandoff.KeyServerRemotePort] != 2222 {
		t.Fatalf("worker remote resources not persisted: %#v", worker.Resources)
	}
	if raw, _ := worker.Tags["raw"].(string); !strings.Contains(raw, nodehandoff.KeyServerNodeRole+"=worker") || !strings.Contains(raw, "local-e2e") {
		t.Fatalf("worker raw tags did not merge metadata and request tags: %q", raw)
	}
	token, err := store.GetPairingTokenByHash(t.Context(), "tenant-1", tokenHash)
	if err != nil {
		t.Fatalf("GetPairingTokenByHash: %v", err)
	}
	if token.Status != "used" || token.UsedAt == nil {
		t.Fatalf("pairing token was not marked used: %#v", token)
	}
}

func TestOwnerAuthenticatedHeartbeatCannotWriteGuardState(t *testing.T) {
	store := controlplane.NewMemoryStore()
	approvedAt := time.Now().Add(-time.Hour)
	if _, upsertErr := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
		ID:             "worker-1",
		TenantID:       "tenant-1",
		StackID:        "stack-1",
		Hostname:       "node-1",
		TokenHash:      strings.Repeat("a", 64),
		Status:         "approved",
		Approved:       true,
		ApprovedAt:     &approvedAt,
		Provider:       "local",
		OwnerSubjectID: "owner-1",
	}); upsertErr != nil {
		t.Fatalf("UpsertWorkerHeartbeat: %v", upsertErr)
	}
	before, beforeErr := store.GetWorker(t.Context(), "tenant-1", "worker-1")
	if beforeErr != nil {
		t.Fatal(beforeErr)
	}

	writer := &fakeWorkerMetricWriter{}
	event, recorder := workerRouteTestEvent(
		http.MethodPost,
		"/api/v1/workers/worker-1/heartbeat",
		`{"cpu_percent":42.5,"memory_used_bytes":512,"memory_total_bytes":1024,"disk_used_bytes":25,"disk_total_bytes":100,"uptime_seconds":1234}`,
	)
	event.Request.SetPathValue("id", "worker-1")
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))

	heartbeatErr := (workerRouteHandlers{wst: store, metricWriter: writer}).heartbeat(event)
	if heartbeatErr != nil {
		t.Fatalf("heartbeat returned router error: %v", heartbeatErr)
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s, want 401", recorder.Code, recorder.Body.String())
	}
	updated, err := store.GetWorker(t.Context(), "tenant-1", "worker-1")
	if err != nil {
		t.Fatalf("GetWorker: %v", err)
	}
	if updated.LastSeenAt == nil || before.LastSeenAt == nil || !updated.LastSeenAt.Equal(*before.LastSeenAt) ||
		!updated.UpdatedAt.Equal(before.UpdatedAt) || updated.Status != "approved" {
		t.Fatalf("owner-authenticated heartbeat mutated worker: %#v", updated)
	}
	if got := len(writer.samples); got != 0 {
		t.Fatalf("samples = %d, want zero: %#v", got, writer.samples)
	}
}

func TestUnscopedRuntimeAgentCannotApproveItselfWithHeartbeatOrInventory(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "worker-secret")
	store := controlplane.NewMemoryStore()
	const agentID = "pending-agent"
	token, issueErr := workerauth.Issue(workerauth.SecretFromEnv(), workerauth.Claims{
		TenantID: "tenant-1", OwnerID: "owner-1", ServerID: runtimeServerIDForWorker(agentID), RuntimeAgentID: agentID,
	}, time.Now().UTC(), time.Hour)
	if issueErr != nil {
		t.Fatal(issueErr)
	}
	if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
		ID: agentID, TenantID: "tenant-1", OwnerSubjectID: "owner-1", Status: "pending", Approved: false,
		Resources:    map[string]any{"agent_token_sha256": workerauth.SHA256Hex(token)},
		Capabilities: map[string]any{"server_id": runtimeServerIDForWorker(agentID)},
	}); err != nil {
		t.Fatal(err)
	}
	handler := workerRouteHandlers{wst: store}

	heartbeatEvent, heartbeatRecorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/"+agentID+"/heartbeat", `{"source_epoch":"epoch-a","source_sequence":1,"observed_at":"2026-07-21T00:00:00Z"}`)
	heartbeatEvent.Request.SetPathValue("id", agentID)
	heartbeatEvent.Request.Header.Set("Authorization", "Bearer "+token)
	if err := handler.heartbeat(heartbeatEvent); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if heartbeatRecorder.Code != http.StatusOK {
		t.Fatalf("heartbeat status = %d body=%s", heartbeatRecorder.Code, heartbeatRecorder.Body.String())
	}

	inventoryEvent, inventoryRecorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/"+agentID+"/inventory", `{"source_epoch":"epoch-a","source_sequence":2,"observed_at":"2026-07-21T00:00:01Z","runtime_agent_id":"pending-agent","host":{"hostname":"node-1","os":"linux","arch":"amd64"}}`)
	inventoryEvent.Request.SetPathValue("id", agentID)
	inventoryEvent.Request.Header.Set("Authorization", "Bearer "+token)
	if err := handler.inventory(inventoryEvent); err != nil {
		t.Fatalf("inventory: %v", err)
	}
	if inventoryRecorder.Code != http.StatusOK || !strings.Contains(inventoryRecorder.Body.String(), `"status":"pending"`) {
		t.Fatalf("inventory status = %d body=%s, want pending", inventoryRecorder.Code, inventoryRecorder.Body.String())
	}

	worker, err := store.GetWorker(t.Context(), "tenant-1", agentID)
	if err != nil {
		t.Fatal(err)
	}
	if worker.Approved || worker.ApprovedAt != nil || worker.Status != "pending" {
		t.Fatalf("unscoped runtime agent self-approved: %#v", worker)
	}
}

func TestRuntimeAgentTokenCannotRecreateDeletedOrRejectedWorker(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "worker-secret")
	const agentID = "revoked-agent"
	token, issueErr := workerauth.Issue(workerauth.SecretFromEnv(), workerauth.Claims{
		TenantID: "tenant-1", OwnerID: "owner-1", StackID: "stack-1",
		ServerID: runtimeServerIDForWorker(agentID), RuntimeAgentID: agentID,
	}, time.Now().UTC(), time.Hour)
	if issueErr != nil {
		t.Fatal(issueErr)
	}

	for _, test := range []struct {
		name    string
		prepare func(*controlplane.MemoryStore)
	}{
		{name: "deleted"},
		{
			name: "rejected",
			prepare: func(store *controlplane.MemoryStore) {
				if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
					ID: agentID, TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1",
					Status: managedRuntimeStatusRejected, Approved: false,
					Resources:    map[string]any{"agent_token_sha256": workerauth.SHA256Hex(token)},
					Capabilities: map[string]any{"server_id": runtimeServerIDForWorker(agentID)},
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := controlplane.NewMemoryStore()
			if test.prepare != nil {
				test.prepare(store)
			}
			event, recorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/"+agentID+"/heartbeat", `{"source_epoch":"auth-kind-epoch","source_sequence":1,"observed_at":"2026-07-21T00:00:00Z"}`)
			event.Request.SetPathValue("id", agentID)
			event.Request.Header.Set("Authorization", "Bearer "+token)
			if err := (workerRouteHandlers{wst: store}).heartbeat(event); err != nil {
				t.Fatalf("heartbeat: %v", err)
			}
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d body=%s, want 401", recorder.Code, recorder.Body.String())
			}
			if _, err := store.GetWorker(t.Context(), "tenant-1", agentID); test.name == "deleted" && !errors.Is(err, controlplane.ErrNotFound) {
				t.Fatalf("deleted worker was recreated: %v", err)
			}
		})
	}
}

func TestRuntimeAgentAuthFailsClosedForSignedTokensAndKeepsOpaqueCredentials(t *testing.T) {
	const agentID = "agent-auth-kind"
	now := time.Now().UTC()
	tests := []struct {
		name       string
		token      func(t *testing.T) string
		wantStatus int
	}{
		{
			name: "expired signed token",
			token: func(t *testing.T) string {
				t.Helper()
				token, err := workerauth.Issue([]byte("current-secret"), workerauth.Claims{
					TenantID: "tenant-1", OwnerID: "owner-1", StackID: "stack-1",
					ServerID: runtimeServerIDForWorker(agentID), RuntimeAgentID: agentID,
				}, now.Add(-2*time.Hour), time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				return token
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "signed token from rotated secret",
			token: func(t *testing.T) string {
				t.Helper()
				token, err := workerauth.Issue([]byte("old-secret"), workerauth.Claims{
					TenantID: "tenant-1", OwnerID: "owner-1", StackID: "stack-1",
					ServerID: runtimeServerIDForWorker(agentID), RuntimeAgentID: agentID,
				}, now, time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				return token
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "stateful opaque credential",
			token: func(t *testing.T) string {
				t.Helper()
				token, err := workerauth.OpaqueToken()
				if err != nil {
					t.Fatal(err)
				}
				return token
			},
			wantStatus: http.StatusOK,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "current-secret")
			token := test.token(t)
			store := controlplane.NewMemoryStore()
			if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
				ID: agentID, TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1",
				Status: "approved", Approved: true,
				Resources:    map[string]any{"agent_token_sha256": workerauth.SHA256Hex(token)},
				Capabilities: map[string]any{"server_id": runtimeServerIDForWorker(agentID)},
			}); err != nil {
				t.Fatal(err)
			}
			event, recorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/"+agentID+"/heartbeat", `{"source_epoch":"auth-kind-epoch","source_sequence":1,"observed_at":"2026-07-21T00:00:00Z"}`)
			event.Request.SetPathValue("id", agentID)
			event.Request.Header.Set("Authorization", "Bearer "+token)
			event.Request.Header.Set("X-Kombify-Tenant-ID", "tenant-1")
			if err := (workerRouteHandlers{wst: store}).heartbeat(event); err != nil {
				t.Fatalf("heartbeat: %v", err)
			}
			if recorder.Code != test.wantStatus {
				t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), test.wantStatus)
			}
		})
	}
}

func TestRuntimeAgentAuthRejectsUsedOrExpiredPairingSecretWhenAgentHashExists(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "")
	const (
		agentID       = "agent-pairing-separated"
		pairingSecret = "pairing-secret-must-not-remain-runtime-auth"
	)
	pairingHash := workerauth.SHA256Hex(pairingSecret)
	now := time.Now().UTC()

	for _, test := range []struct {
		name      string
		status    string
		usedAt    *time.Time
		expiresAt *time.Time
	}{
		{
			name:      "used pairing secret",
			status:    "used",
			usedAt:    timePointer(now),
			expiresAt: timePointer(now.Add(time.Hour)),
		},
		{
			name:      "expired pairing secret",
			status:    "active",
			expiresAt: timePointer(now.Add(-time.Minute)),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			agentToken, tokenErr := workerauth.OpaqueToken()
			if tokenErr != nil {
				t.Fatal(tokenErr)
			}
			store := controlplane.NewMemoryStore()
			if _, seedPairingErr := store.UpsertPairingToken(t.Context(), controlplane.PairingToken{
				ID: "pairing-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1",
				TokenHash: pairingHash, Status: test.status, UsedAt: test.usedAt, ExpiresAt: test.expiresAt,
			}); seedPairingErr != nil {
				t.Fatalf("seed pairing token: %v", seedPairingErr)
			}
			if _, seedWorkerErr := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
				ID: agentID, TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1",
				TokenHash: pairingHash, Status: "approved", Approved: true,
				Resources:    map[string]any{"agent_token_sha256": workerauth.SHA256Hex(agentToken)},
				Capabilities: map[string]any{"server_id": runtimeServerIDForWorker(agentID)},
			}); seedWorkerErr != nil {
				t.Fatalf("seed worker: %v", seedWorkerErr)
			}

			event, recorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/"+agentID+"/heartbeat", `{}`)
			event.Request.SetPathValue("id", agentID)
			event.Request.Header.Set("Authorization", "Bearer "+pairingSecret)
			event.Request.Header.Set("X-Kombify-Tenant-ID", "tenant-1")
			if heartbeatErr := (workerRouteHandlers{wst: store}).heartbeat(event); heartbeatErr != nil {
				t.Fatalf("heartbeat: %v", heartbeatErr)
			}
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("pairing credential status = %d body=%s, want 401", recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), `"data"`) {
				t.Fatalf("rejected pairing credential continued into a success response: %s", recorder.Body.String())
			}
			unchanged, err := store.GetWorker(t.Context(), "tenant-1", agentID)
			if err != nil || unchanged.TokenHash != pairingHash || stringFromAny(unchanged.Resources["agent_token_sha256"]) != workerauth.SHA256Hex(agentToken) || unchanged.Status != "approved" {
				t.Fatalf("rejected pairing credential mutated worker: worker=%#v err=%v", unchanged, err)
			}

			agentEvent, agentRecorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/"+agentID+"/heartbeat", `{"source_epoch":"pairing-separated-epoch","source_sequence":1,"observed_at":"2026-07-21T00:00:00Z"}`)
			agentEvent.Request.SetPathValue("id", agentID)
			agentEvent.Request.Header.Set("Authorization", "Bearer "+agentToken)
			agentEvent.Request.Header.Set("X-Kombify-Tenant-ID", "tenant-1")
			if err := (workerRouteHandlers{wst: store}).heartbeat(agentEvent); err != nil {
				t.Fatalf("agent heartbeat: %v", err)
			}
			if agentRecorder.Code != http.StatusOK {
				t.Fatalf("agent credential status = %d body=%s, want 200", agentRecorder.Code, agentRecorder.Body.String())
			}
		})
	}
}

func TestWorkerConnectCreatesRuntimeEnrollment(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "worker-secret")
	store := controlplane.NewMemoryStore()
	event, recorder := workerRouteTestEvent(
		http.MethodPost,
		"/v1/ril/servers/connect",
		`{"server_id":"server-1","runtime_agent_id":"runtime-1","stack_id":"stack-1","hostname":"node-1","mode":"advanced","connection_mode":"ssh","provider":"local"}`,
	)
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))

	if err := (workerRouteHandlers{wst: store}).connectServer(event); err != nil {
		t.Fatalf("connectServer returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data["server_id"] != "server-1" || envelope.Data["runtime_agent_id"] != "runtime-1" || envelope.Data["agent_token"] == "" {
		t.Fatalf("unexpected enrollment response: %#v", envelope.Data)
	}
	worker, err := store.GetWorker(t.Context(), "tenant-1", "runtime-1")
	if err != nil {
		t.Fatalf("GetWorker: %v", err)
	}
	if worker.Status != "pending" || !worker.Approved {
		t.Fatalf("connect should create an accepted-but-not-connected worker: %#v", worker)
	}
	if worker.Capabilities["liveness_required"] != "guard_inventory" {
		t.Fatalf("worker capabilities missing liveness gate: %#v", worker.Capabilities)
	}
}

func TestWorkerConnectBindsWizardPlannedServerWithoutForgingGuardState(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "worker-secret")
	store := controlplane.NewMemoryStore()
	plannedID := runtimeidentity.StackServerID("stack-1", "primary")
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: plannedID, TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
		Name: "planned", LifecycleState: "planned", ConnectionState: "pending", HealthState: "unknown",
	}); err != nil {
		t.Fatalf("persist planned server: %v", err)
	}
	event, recorder := workerRouteTestEvent(
		http.MethodPost,
		"/v1/ril/servers/connect",
		`{"stack_id":"stack-1","hostname":"node-1","connection_mode":"ssh"}`,
	)
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))

	if err := (workerRouteHandlers{wst: store, serverStore: store}).connectServer(event); err != nil {
		t.Fatalf("connectServer returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data["server_id"] != plannedID {
		t.Fatalf("server_id = %#v, want planned %q", envelope.Data["server_id"], plannedID)
	}
	servers, _ := store.ListServerRuntimesByTenant(t.Context(), "tenant-1", "stack-1")
	if len(servers) != 1 || servers[0].ID != plannedID || servers[0].LifecycleState != "enrolling" || servers[0].ConnectionState != "pending" {
		t.Fatalf("connect must reuse the planned server while leaving connection observations to Guard: %#v", servers)
	}
}

func TestWorkerConnectLeaseFailsClosedWithoutAuthority(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "worker-secret")
	store := controlplane.NewMemoryStore()
	event, recorder := workerRouteTestEvent(
		http.MethodPost,
		"/v1/ril/servers/connect",
		`{"lease_id":"lease-centron-1","stack_id":"stack-1","hostname":"node-1","connection_mode":"managed"}`,
	)
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	if err := (workerRouteHandlers{wst: store}).connectServer(event); err != nil {
		t.Fatalf("connectServer returned router error: %v", err)
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("lease-bound connect without authority = %d body=%s, want %d", recorder.Code, recorder.Body.String(), http.StatusServiceUnavailable)
	}
}

func TestWorkerConnectLeaseValidatesAuthorityAndUsesCanonicalServerID(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "worker-secret")
	store := controlplane.NewMemoryStore()
	const leaseID = "lease-centron-1"
	lister := fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{
		createStackOperationsTestLease(leaseID, "tenant-1", "owner-1", "stack-1", "enrolled"),
	}}
	event, recorder := workerRouteTestEvent(
		http.MethodPost,
		"/v1/ril/servers/connect",
		`{"lease_id":"lease-centron-1","server_id":"server-user-supplied","stack_id":"stack-1","runtime_agent_id":"agent-1","hostname":"node-1","connection_mode":"managed"}`,
	)
	event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	if err := (workerRouteHandlers{wst: store, managedRuntimeLeases: lister}).connectServer(event); err != nil {
		t.Fatalf("connectServer returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("lease-bound connect = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode enrollment: %v", err)
	}
	wantServerID := runtimeidentity.LeaseServerID(leaseID)
	if envelope.Data["server_id"] != wantServerID {
		t.Fatalf("lease enrollment server id = %#v, want %q", envelope.Data, wantServerID)
	}
	token, _ := envelope.Data["agent_token"].(string)
	worker, err := store.GetWorker(t.Context(), "tenant-1", "agent-1")
	if err != nil || !workerauth.IsOpaqueToken(token) || stringFromAny(worker.Resources["agent_token_sha256"]) != workerauth.SHA256Hex(token) || stringFromAny(worker.Capabilities["server_id"]) != wantServerID || runtimeLeaseIDFromMetadata(worker.Capabilities) != leaseID {
		t.Fatalf("lease enrollment credential is not statefully bound: token=%q worker=%#v err=%v", token, worker, err)
	}
}

func TestWorkerConnectLeaseRejectsInactiveOrCrossStackLease(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "worker-secret")
	const leaseID = "lease-centron-rejected"
	for _, test := range []struct {
		name    string
		stackID string
		lease   vmlease.Lease
	}{
		{
			name:    "cross stack",
			lease:   createStackOperationsTestLease(leaseID, "tenant-1", "owner-1", "stack-actual", "enrolled"),
			stackID: "stack-requested",
		},
		func() struct {
			name    string
			stackID string
			lease   vmlease.Lease
		} {
			lease := createStackOperationsTestLease(leaseID, "tenant-1", "owner-1", "stack-1", "enrolled")
			now := time.Now().UTC()
			lease.CancelledAt = &now
			return struct {
				name    string
				stackID string
				lease   vmlease.Lease
			}{name: "inactive", stackID: "stack-1", lease: lease}
		}(),
	} {
		t.Run(test.name, func(t *testing.T) {
			event, recorder := workerRouteTestEvent(http.MethodPost, "/v1/ril/servers/connect", `{"lease_id":"`+leaseID+`","stack_id":"`+test.stackID+`","hostname":"node-1"}`)
			event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
			if err := (workerRouteHandlers{wst: controlplane.NewMemoryStore(), managedRuntimeLeases: fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{test.lease}}}).connectServer(event); err != nil {
				t.Fatalf("connectServer returned router error: %v", err)
			}
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("invalid lease connect = %d body=%s, want 403", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestWorkerConnectLeaseRejectsNonNativeExecutionAuthorityWithoutMintingCredential(t *testing.T) {
	const leaseID = "lease-centron-non-native"
	lease := createStackOperationsTestLease(leaseID, "tenant-1", "owner-1", "stack-1", "enrolled")
	for _, test := range []struct {
		name      string
		authority vmleases.LeaseExecutionAuthority
		state     vmleases.LeaseAuthorityState
	}{
		{name: "legacy", authority: vmleases.LeaseExecutionAuthorityLegacySimulate, state: vmleases.LeaseAuthorityStateLegacyQuarantined},
		{name: "unbound", state: vmleases.LeaseAuthorityStateUnbound},
		{name: "native inactive", authority: vmleases.LeaseExecutionAuthorityTechStackProviderControl, state: vmleases.LeaseAuthorityStateNativeInactive},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := controlplane.NewMemoryStore()
			event, recorder := workerRouteTestEvent(http.MethodPost, "/v1/ril/servers/connect", `{"lease_id":"`+leaseID+`","stack_id":"stack-1","runtime_agent_id":"agent-1","hostname":"node-1"}`)
			event.Request = event.Request.WithContext(identity.NewContext(event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
			record := vmleases.LeaseInventoryRecord{Lease: lease, ExecutionAuthority: test.authority, AuthorityState: test.state}
			handler := workerRouteHandlers{wst: store, managedRuntimeLeases: fakeManagedRuntimeLeaseLister{inventory: []vmleases.LeaseInventoryRecord{record}}}
			if err := handler.connectServer(event); err != nil {
				t.Fatalf("connectServer returned router error: %v", err)
			}
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("status = %d body=%s, want 403", recorder.Code, recorder.Body.String())
			}
			if workers, err := store.ListWorkersByTenant(t.Context(), "tenant-1"); err != nil || len(workers) != 0 {
				t.Fatalf("workers = %#v err=%v, non-native connect minted a credential", workers, err)
			}
		})
	}
}

func TestWorkerInventorySignedClaimsRejectCrossStackAndPreserveClaimedPlacement(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "worker-secret")
	store := controlplane.NewMemoryStore()
	const leaseID = "lease-centron-claim"
	const agentID = "agent-claims"
	const claimedStackID = "stack-claimed"
	serverID := runtimeidentity.LeaseServerID(leaseID)
	token, err := workerauth.Issue(workerauth.SecretFromEnv(), workerauth.Claims{
		TenantID: "tenant-1", OwnerID: "owner-1", StackID: claimedStackID, LeaseID: leaseID, ServerID: serverID, RuntimeAgentID: agentID,
	}, time.Now().UTC(), time.Hour)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, upsertErr := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
		ID: agentID, TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: claimedStackID,
		Status: "approved", Approved: true,
		Resources: map[string]any{"agent_token_sha256": workerauth.SHA256Hex(token)},
		Capabilities: map[string]any{
			"server_id": serverID, runtimeLeaseIDKey: leaseID,
		},
	}); upsertErr != nil {
		t.Fatalf("UpsertWorkerHeartbeat: %v", upsertErr)
	}
	handler := workerRouteHandlers{wst: store, registryStore: store, rilStore: store}

	for _, payload := range []string{
		`{"tenant_id":"tenant-1","stack_id":"stack-other","server_id":"` + serverID + `","runtime_agent_id":"` + agentID + `"}`,
		`{"tenant_id":"tenant-1","stack_id":"` + claimedStackID + `","lease_id":"` + leaseID + `","server_id":"server-attacker","runtime_agent_id":"` + agentID + `"}`,
	} {
		event, recorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/"+agentID+"/inventory", payload)
		event.Request.SetPathValue("id", agentID)
		event.Request.Header.Set("Authorization", "Bearer "+token)
		if routeErr := handler.inventory(event); routeErr != nil {
			t.Fatalf("inventory returned router error: %v", routeErr)
		}
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("mismatched signed inventory = %d body=%s, want 403", recorder.Code, recorder.Body.String())
		}
	}

	validPayload := `{"source_epoch":"epoch-a","source_sequence":1,"observed_at":"2026-07-21T00:00:00Z","tenant_id":"tenant-1","owner_id":"owner-1","stack_id":"stack-claimed","lease_id":"lease-centron-claim","server_id":"` + serverID + `","runtime_agent_id":"agent-claims","host":{"hostname":"node-1"},"services":[{"service_id":"vaultwarden","name":"Vaultwarden","status":"healthy","owner_stack":"stack-other","endpoints":[{"url":"https://vault.example.test/health","health":"ok"}]}]}`
	event, recorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/"+agentID+"/inventory", validPayload)
	event.Request.SetPathValue("id", agentID)
	event.Request.Header.Set("Authorization", "Bearer "+token)
	if routeErr := handler.inventory(event); routeErr != nil {
		t.Fatalf("inventory returned router error: %v", routeErr)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("valid signed inventory = %d body=%s", recorder.Code, recorder.Body.String())
	}
	worker, err := store.GetWorker(t.Context(), "tenant-1", agentID)
	if err != nil || worker.StackID != claimedStackID || worker.OwnerSubjectID != "owner-1" || worker.Capabilities["server_id"] != serverID {
		t.Fatalf("worker lost signed placement: worker=%#v err=%v", worker, err)
	}
	nodes, err := store.ListNodesByStack(t.Context(), "tenant-1", claimedStackID)
	if err != nil || len(nodes) != 1 || nodes[0].ID != serverID {
		t.Fatalf("registry node must share signed lease server id: nodes=%#v err=%v", nodes, err)
	}
	services, err := store.ListServicesByStack(t.Context(), "tenant-1", claimedStackID)
	if err != nil || len(services) != 1 || services[0].NodeID != serverID {
		t.Fatalf("service must remain in claimed stack: services=%#v err=%v", services, err)
	}
	foreign, err := store.ListServicesByStack(t.Context(), "tenant-1", "stack-other")
	if err != nil || len(foreign) != 0 {
		t.Fatalf("inventory owner_stack must not create cross-stack service: services=%#v err=%v", foreign, err)
	}
}

func TestWorkerInventoryIngestsServicesEndpointsAndMetrics(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "worker-secret")
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{
		ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Stack one",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	writer := &fakeWorkerMetricWriter{}
	connectEvent, connectRecorder := workerRouteTestEvent(
		http.MethodPost,
		"/v1/ril/servers/connect",
		`{"server_id":"server-1","runtime_agent_id":"runtime-1","stack_id":"stack-1","hostname":"node-1","mode":"advanced","connection_mode":"managed"}`,
	)
	connectEvent.Request = connectEvent.Request.WithContext(identity.NewContext(connectEvent.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	handler := workerRouteHandlers{wst: store, registryStore: store, serverStore: store, rilStore: store, metricWriter: writer}
	if err := handler.connectServer(connectEvent); err != nil {
		t.Fatalf("connectServer returned router error: %v", err)
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(connectRecorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode connect response: %v", err)
	}
	agentToken, _ := envelope.Data["agent_token"].(string)
	if agentToken == "" {
		t.Fatalf("connect response missing agent_token: %#v", envelope.Data)
	}

	observedAt := time.Now().UTC().Add(-2 * time.Second)
	inventoryBody := strings.Replace(`{
		"source_epoch":"epoch-a",
		"source_sequence":1,
		"observed_at":"{{observed_at}}",
		"server_id":"server-1",
		"runtime_agent_id":"runtime-1",
		"hostname":"node-1",
		"manifest_observed":true,
		"host":{"hostname":"node-1","os":"linux","arch":"amd64","public_ip":"203.0.113.10","cpu_cores":4,"ram_mb":8192,"disk_gb":120,"cpu_percent":12.5,"memory_used_bytes":1024,"memory_total_bytes":2048,"disk_used_bytes":100,"disk_total_bytes":1000,"uptime_seconds":99},
		"channels":[{"kind":"https","url":"https://techstack.example/api/v1/workers/runtime-1/inventory","status":"ok"}],
		"services":[{
			"service_id":"vaultwarden",
			"name":"Vaultwarden",
			"status":"healthy",
			"container_id":"container-1",
			"endpoints":[
				{"url":"https://vault.example.com","visibility":"public","provenance":"custom-domain","health":"ok"},
				{"url":"http://10.0.0.4:8080","visibility":"local","provenance":"lan","health":"ok"}
			]
		}]
	}`, "{{observed_at}}", observedAt.Format(time.RFC3339Nano), 1)
	inventoryEvent, inventoryRecorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/runtime-1/inventory", inventoryBody)
	inventoryEvent.Request.SetPathValue("id", "runtime-1")
	inventoryEvent.Request.Header.Set("Authorization", "Bearer "+agentToken)
	inventoryEvent.Request.Header.Set("X-Kombify-Tenant-ID", "tenant-1")

	if err := handler.inventory(inventoryEvent); err != nil {
		t.Fatalf("inventory returned router error: %v", err)
	}
	if inventoryRecorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", inventoryRecorder.Code, inventoryRecorder.Body.String())
	}
	worker, err := store.GetWorker(t.Context(), "tenant-1", "runtime-1")
	if err != nil {
		t.Fatalf("GetWorker: %v", err)
	}
	if worker.Status != "connected" || worker.CPUCores != 4 || worker.RAMMB != 8192 || worker.DiskGB != 120 {
		t.Fatalf("inventory did not update worker health/resources: %#v", worker)
	}
	services, err := store.ListServicesByStack(t.Context(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("ListServicesByStack: %v", err)
	}
	if len(services) != 1 {
		t.Fatalf("services = %d, want 1: %#v", len(services), services)
	}
	// The legacy status column is derived from the observed dimension. Health
	// is measured independently and lives on the runtime projection.
	if services[0].ServiceKey != "vaultwarden" || services[0].Status != "running" || services[0].URL != "https://vault.example.com" {
		t.Fatalf("service inventory not persisted correctly: %#v", services[0])
	}
	runtimeRow, err := store.GetServiceRuntime(t.Context(), "tenant-1", services[0].ID)
	if err != nil {
		t.Fatalf("GetServiceRuntime: %v", err)
	}
	if runtimeRow.ObservedState != "running" || runtimeRow.HealthState != "healthy" {
		t.Fatalf("status and health were conflated: %#v", runtimeRow)
	}
	server, err := store.GetServerRuntime(t.Context(), "tenant-1", "server-1")
	if err != nil {
		t.Fatalf("GetServerRuntime: %v", err)
	}
	if server.InventoryRevision != 1 {
		t.Fatalf("server inventory revision = %d, want 1", server.InventoryRevision)
	}
	if revision, ok := services[0].Metadata["inventory_revision"].(int64); !ok || revision != server.InventoryRevision {
		t.Fatalf("legacy service inventory revision = %#v, want %d", services[0].Metadata["inventory_revision"], server.InventoryRevision)
	}
	runtimeService, err := store.GetServiceRuntime(t.Context(), "tenant-1", services[0].ID)
	if err != nil {
		t.Fatalf("GetServiceRuntime: %v", err)
	}
	if revision, ok := runtimeService.Metadata["inventory_revision"].(int64); !ok || revision != server.InventoryRevision {
		t.Fatalf("runtime service inventory revision = %#v, want %d", runtimeService.Metadata["inventory_revision"], server.InventoryRevision)
	}
	scope, err := controlplane.NewOwnerInventoryReadScope("tenant-1", "owner-1")
	if err != nil {
		t.Fatalf("NewOwnerInventoryReadScope: %v", err)
	}
	page, err := store.ListInventoryServices(t.Context(), scope, "server-1", controlplane.InventoryPageRequest{Limit: 10})
	if err != nil || len(page.Services) != 1 || page.Services[0].ID != services[0].ID {
		t.Fatalf("canonical inventory services = %#v err=%v, want one current service", page.Services, err)
	}
	endpoints := sliceOfMapsFromAny(services[0].Metadata["endpoints"])
	if len(endpoints) != 2 || endpoints[0]["visibility"] != "public" || endpoints[1]["visibility"] != "local" {
		t.Fatalf("service endpoints not preserved: %#v", services[0].Metadata["endpoints"])
	}
	if got := len(writer.samples); got != 0 {
		t.Fatalf("Guard inventory must not emit unfenced TSDB samples: got %d: %#v", got, writer.samples)
	}

	emptyInventoryBody := strings.Replace(`{
		"source_epoch":"epoch-a",
		"source_sequence":2,
		"observed_at":"{{observed_at}}",
		"server_id":"server-1",
		"runtime_agent_id":"runtime-1",
		"hostname":"node-1",
		"manifest_observed":true,
		"host":{"hostname":"node-1","os":"linux","arch":"amd64","public_ip":"203.0.113.11"},
		"services":[]
	}`, "{{observed_at}}", observedAt.Add(time.Second).Format(time.RFC3339Nano), 1)
	emptyEvent, emptyRecorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/runtime-1/inventory", emptyInventoryBody)
	emptyEvent.Request.SetPathValue("id", "runtime-1")
	emptyEvent.Request.Header.Set("Authorization", "Bearer "+agentToken)
	emptyEvent.Request.Header.Set("X-Kombify-Tenant-ID", "tenant-1")
	if err := handler.inventory(emptyEvent); err != nil {
		t.Fatalf("empty inventory returned router error: %v", err)
	}
	if emptyRecorder.Code != http.StatusOK {
		t.Fatalf("empty inventory status = %d body=%s, want 200", emptyRecorder.Code, emptyRecorder.Body.String())
	}
	server, err = store.GetServerRuntime(t.Context(), "tenant-1", "server-1")
	if err != nil || server.InventoryRevision != 2 {
		t.Fatalf("second server inventory revision = %#v err=%v, want 2", server, err)
	}
	// Absence of service evidence is not evidence of absence: the previously
	// observed service survives an empty manifest with its access unavailable.
	page, err = store.ListInventoryServices(t.Context(), scope, "server-1", controlplane.InventoryPageRequest{Limit: 10})
	if err != nil || len(page.Services) != 1 {
		t.Fatalf("canonical empty inventory services = %#v err=%v, want one retained service", page.Services, err)
	}
	if got := fmt.Sprint(page.Services[0].Access["mode"]); got != "unavailable" {
		t.Fatalf("retained service access mode = %q, want unavailable", got)
	}

	// An exact retry after the enrollment promotion is a verified no-op for the
	// authoritative projection, while still allowing satellite repair.
	replayEvent, replayRecorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/runtime-1/inventory", emptyInventoryBody)
	replayEvent.Request.SetPathValue("id", "runtime-1")
	replayEvent.Request.Header.Set("Authorization", "Bearer "+agentToken)
	replayEvent.Request.Header.Set("X-Kombify-Tenant-ID", "tenant-1")
	if err := handler.inventory(replayEvent); err != nil {
		t.Fatalf("exact replay returned router error: %v", err)
	}
	if replayRecorder.Code != http.StatusOK {
		t.Fatalf("exact replay status = %d body=%s, want 200", replayRecorder.Code, replayRecorder.Body.String())
	}
	server, err = store.GetServerRuntime(t.Context(), "tenant-1", "server-1")
	if err != nil || server.InventoryRevision != 2 {
		t.Fatalf("exact replay advanced inventory revision: server=%#v err=%v", server, err)
	}

	// The same Guard position with a changed body is not an idempotent replay.
	changedBody := strings.Replace(emptyInventoryBody, `"services":[]`, `"services":[{"service_id":"changed","status":"healthy"}]`, 1)
	changedEvent, changedRecorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/runtime-1/inventory", changedBody)
	changedEvent.Request.SetPathValue("id", "runtime-1")
	changedEvent.Request.Header.Set("Authorization", "Bearer "+agentToken)
	changedEvent.Request.Header.Set("X-Kombify-Tenant-ID", "tenant-1")
	if err := handler.inventory(changedEvent); err != nil {
		t.Fatalf("changed replay returned router error: %v", err)
	}
	if changedRecorder.Code != http.StatusConflict {
		t.Fatalf("changed replay status = %d body=%s, want 409", changedRecorder.Code, changedRecorder.Body.String())
	}
	page, err = store.ListInventoryServices(t.Context(), scope, "server-1", controlplane.InventoryPageRequest{Limit: 10})
	if err != nil || len(page.Services) != 1 {
		t.Fatalf("changed replay mutated services = %#v err=%v", page.Services, err)
	}

	// A superseded event is accepted as stale without rolling the worker or
	// service projection back to its older host observation.
	lateEvent, lateRecorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/runtime-1/inventory", inventoryBody)
	lateEvent.Request.SetPathValue("id", "runtime-1")
	lateEvent.Request.Header.Set("Authorization", "Bearer "+agentToken)
	lateEvent.Request.Header.Set("X-Kombify-Tenant-ID", "tenant-1")
	if err := handler.inventory(lateEvent); err != nil {
		t.Fatalf("late inventory returned router error: %v", err)
	}
	if lateRecorder.Code != http.StatusAccepted {
		t.Fatalf("late inventory status = %d body=%s, want 202", lateRecorder.Code, lateRecorder.Body.String())
	}
	worker, err = store.GetWorker(t.Context(), "tenant-1", "runtime-1")
	if err != nil || worker.IP != "203.0.113.11" {
		t.Fatalf("late inventory rolled worker back: worker=%#v err=%v", worker, err)
	}
}

func TestWorkerInventoryUsesLeaseStableServerIdentity(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "worker-secret")
	store := controlplane.NewMemoryStore()
	handler := workerRouteHandlers{wst: store, registryStore: store}
	const leaseID = "lease-centron-1"
	for _, agentID := range []string{"agent-first", "agent-retry"} {
		token, err := workerauth.Issue(workerauth.SecretFromEnv(), workerauth.Claims{
			TenantID: "tenant-1", OwnerID: "owner-1", StackID: "stack-1", LeaseID: leaseID,
			ServerID: runtimeidentity.LeaseServerID(leaseID), RuntimeAgentID: agentID,
		}, time.Now().UTC(), time.Hour)
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if _, upsertErr := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
			ID: agentID, TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1",
			Status: "approved", Approved: true,
			Resources: map[string]any{"agent_token_sha256": workerauth.SHA256Hex(token)},
			Capabilities: map[string]any{
				"server_id": runtimeidentity.LeaseServerID(leaseID), runtimeLeaseIDKey: leaseID,
			},
		}); upsertErr != nil {
			t.Fatalf("seed enrolled worker: %v", upsertErr)
		}
		event, recorder := workerRouteTestEvent(http.MethodPost, "/api/v1/workers/"+agentID+"/inventory", `{"source_epoch":"`+agentID+`-epoch","source_sequence":1,"observed_at":"2026-07-21T00:00:00Z","server_id":"`+runtimeidentity.LeaseServerID(leaseID)+`","runtime_agent_id":"`+agentID+`","host":{"hostname":"node-1"}}`)
		event.Request.SetPathValue("id", agentID)
		event.Request.Header.Set("Authorization", "Bearer "+token)
		if err := handler.inventory(event); err != nil {
			t.Fatalf("inventory: %v", err)
		}
		if recorder.Code != http.StatusOK {
			t.Fatalf("inventory status = %d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	nodes, err := store.ListNodesByStack(t.Context(), "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("ListNodesByStack: %v", err)
	}
	if len(nodes) != 1 || nodes[0].ID != runtimeidentity.LeaseServerID(leaseID) || nodes[0].Metadata["lease_id"] != leaseID {
		t.Fatalf("lease retries must share one registry node: %#v", nodes)
	}
}

func workerRouteTestEvent(method, target, body string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if strings.HasPrefix(target, "/v1/ril/servers/connect") {
		req.Header.Set("Idempotency-Key", "worker-route-test-connect-v1")
	}
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

type fakeWorkerMetricWriter struct {
	samples []monitoring.MetricSample
}

func (f *fakeWorkerMetricWriter) Write(samples []monitoring.MetricSample) error {
	f.samples = append(f.samples, samples...)
	return nil
}

func ensureWorkerRouteTestCollections(t *testing.T, app core.App) {
	t.Helper()
	ensureDriftRouteTestCollection(t, app, "pairing_tokens",
		&core.TextField{Name: "user"},
		&core.TextField{Name: "name"},
		&core.TextField{Name: "token_hash"},
		&core.TextField{Name: "stack_id"},
		&core.JSONField{Name: "metadata"},
		&core.BoolField{Name: "used"},
		&core.DateField{Name: "expires_at"},
		&core.DateField{Name: "used_at"},
	)
	ensureDriftRouteTestCollection(t, app, "workers",
		&core.TextField{Name: "hostname"},
		&core.TextField{Name: "ip"},
		&core.TextField{Name: "os"},
		&core.TextField{Name: "arch"},
		&core.TextField{Name: "token_hash"},
		&core.SelectField{Name: "status", Values: []string{"pending", "approved", "rejected"}},
		&core.BoolField{Name: "approved"},
		&core.DateField{Name: "approved_at"},
		&core.DateField{Name: "last_seen"},
		&core.NumberField{Name: "cpu_cores"},
		&core.NumberField{Name: "ram_mb"},
		&core.NumberField{Name: "disk_gb"},
		&core.TextField{Name: "gpu"},
		&core.BoolField{Name: "has_nvme"},
		&core.BoolField{Name: "has_hw_transcode"},
		&core.TextField{Name: "docker_version"},
		&core.SelectField{Name: "type", Values: []string{"main", "worker", "storage"}},
		&core.TextField{Name: "provider"},
		&core.TextField{Name: "tags"},
		&core.TextField{Name: "owner_id"},
		&core.TextField{Name: "tenant_id"},
		&core.TextField{Name: "stack_id"},
		&core.AutodateField{Name: "created", OnCreate: true},
		&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true},
	)
}

func TestInstallScriptPSServesPowerShellWorkerBootstrap(t *testing.T) {
	event, recorder := workerRouteTestEvent(http.MethodGet, "/install.ps1", "")

	if err := (workerRouteHandlers{}).installScriptPS(event); err != nil {
		t.Fatalf("installScriptPS returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if ct := recorder.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("content-type = %q, want text/plain", ct)
	}
	// From the package dir no on-disk install.ps1 exists, so the embedded
	// fallback is served. It must be a PowerShell worker bootstrap keyed on
	// KOMBI_SERVER/KOMBI_TOKEN that POSTs to the worker register endpoint.
	body := recorder.Body.String()
	for _, want := range []string{"KOMBI_SERVER", "KOMBI_TOKEN", "workers/register", "Invoke-RestMethod"} {
		if !strings.Contains(body, want) {
			t.Fatalf("served install.ps1 missing %q; got:\n%s", want, body)
		}
	}
}

func TestInstallScriptPSServesOnDiskInstallerWhenPresent(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)
	const installer = "# kombify Techstack Windows Installer\nWrite-Host 'full installer'\n"
	if err := os.WriteFile("install.ps1", []byte(installer), 0o600); err != nil {
		t.Fatalf("write temp install.ps1: %v", err)
	}

	event, recorder := workerRouteTestEvent(http.MethodGet, "/install.ps1", "")

	if err := (workerRouteHandlers{}).installScriptPS(event); err != nil {
		t.Fatalf("installScriptPS returned router error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if ct := recorder.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("content-type = %q, want text/plain", ct)
	}
	if cd := recorder.Header().Get("Content-Disposition"); !strings.Contains(cd, "install.ps1") {
		t.Fatalf("content-disposition = %q, want install.ps1", cd)
	}
	body := recorder.Body.String()
	if body != installer {
		t.Fatalf("served body = %q, want on-disk installer", body)
	}
	if strings.Contains(body, "workers/register") {
		t.Fatalf("served embedded fallback instead of on-disk installer:\n%s", body)
	}
}
