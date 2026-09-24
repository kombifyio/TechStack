package providercontroljobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"

	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
)

type allowGate struct{}

func (allowGate) Require(context.Context) error { return nil }

type recordingAdmission struct {
	preflightCalls int
	preflightErr   error
	calls          int
	last           providercontrol.NativeProvisionAdmissionRequest
	requests       []providercontrol.NativeProvisionAdmissionRequest
	replay         bool
	head           providerexecutor.Receipt
}

func (a *recordingAdmission) ResolveManagedRuntimeSlotGeneration(
	_ context.Context,
	tenantID, stackID, slotKey string,
) (providercontrol.ManagedRuntimeSlotGenerationResolution, error) {
	return providercontrol.ManagedRuntimeSlotGenerationResolution{
		RuntimeSlotID:     providercontrol.DeriveManagedRuntimeSlotID(tenantID, stackID, slotKey),
		GenerationOrdinal: 1,
	}, nil
}

func (a *recordingAdmission) PreflightProvision(
	_ context.Context,
	_ providercontrol.NativeProvisionAdmissionRequest,
) error {
	a.preflightCalls++
	return a.preflightErr
}

func (a *recordingAdmission) AdmitProvision(
	_ context.Context,
	request providercontrol.NativeProvisionAdmissionRequest,
) (providercontrol.NativeProvisionAdmissionResult, error) {
	a.calls++
	a.last = request
	a.requests = append(a.requests, request)
	return providercontrol.NativeProvisionAdmissionResult{
		RuntimeSlotKey:        request.RuntimeSlotKey,
		RuntimeSlotID:         request.RuntimeSlotID,
		RuntimeSlotGeneration: request.RuntimeSlotGeneration,
		LeaseID:               string(request.Lease.ID),
		RuntimeServerID:       request.RuntimeServerID,
		ResourceGenerationID:  "11111111-1111-4111-8111-111111111111",
		Operation: providercontrol.OperationRecord{Command: providerexecutor.Command{
			OperationID: "operation-1",
		}, Head: a.head},
		Created: !a.replay,
	}, nil
}

type recordingDecommissioner struct {
	calls int
}

func (d *recordingDecommissioner) DecommissionManagedLeases(
	_ context.Context,
	_ jobs.ManagedLeaseDecommissionRequest,
) (*jobs.ManagedLeaseDecommissionResult, error) {
	d.calls++
	return &jobs.ManagedLeaseDecommissionResult{Decommissioned: 1}, nil
}

func TestManagerRejectsTerminalFailedProvisionReplay(t *testing.T) {
	admission := &recordingAdmission{
		replay: true,
		head: providerexecutor.Receipt{
			Status: providerexecutor.StatusFailed,
			Phase:  providerexecutor.PhaseFailed,
			Reason: &providerexecutor.Reason{
				Code: providerexecutor.ReasonCodeProviderPartialCreate,
			},
		},
	}
	manager, err := NewManager(ManagerConfig{
		Admission: admission, ActivationGate: allowGate{},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	result, err := manager.CreateOrBindLease(t.Context(), validManagedLeaseRequest())
	if !errors.Is(err, ErrNativeProvisionReplayFailed) ||
		!strings.Contains(err.Error(), providerexecutor.ReasonCodeProviderPartialCreate) {
		t.Fatalf("CreateOrBindLease error = %v, want terminal provider reason", err)
	}
	if result != nil {
		t.Fatalf("terminal replay result = %+v, want nil", result)
	}
}

func TestManagerClosedOuterGateAllowsNativeAdmissionReplay(t *testing.T) {
	admission := &recordingAdmission{replay: true}
	manager, err := NewManager(ManagerConfig{
		Admission: admission, ActivationGate: providercontrol.ProductionBlockedGate{},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	result, err := manager.CreateOrBindLease(t.Context(), validManagedLeaseRequest())
	if err != nil {
		t.Fatalf("CreateOrBindLease replay: %v", err)
	}
	if admission.calls != 1 || result == nil || !result.IdempotentReplay {
		t.Fatalf("admission calls/result = %d/%+v, want one durable replay", admission.calls, result)
	}
}

func TestManagerPreflightNormalizesAndDelegatesWithoutAdmission(t *testing.T) {
	admission := &recordingAdmission{}
	manager, err := NewManager(ManagerConfig{Admission: admission, ActivationGate: allowGate{}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := manager.PreflightCreateOrBindLease(t.Context(), validManagedLeaseRequest()); err != nil {
		t.Fatalf("PreflightCreateOrBindLease: %v", err)
	}
	if admission.preflightCalls != 1 || admission.calls != 0 {
		t.Fatalf("preflight/admission calls = %d/%d, want 1/0", admission.preflightCalls, admission.calls)
	}
}

func TestNewManagerRejectsTypedNilDependencies(t *testing.T) {
	var nilAdmission *recordingAdmission
	if _, err := NewManager(ManagerConfig{Admission: nilAdmission, ActivationGate: allowGate{}}); err == nil {
		t.Fatal("NewManager accepted typed-nil admission")
	}
	var nilGate *allowGate
	if _, err := NewManager(ManagerConfig{Admission: &recordingAdmission{}, ActivationGate: nilGate}); err == nil {
		t.Fatal("NewManager accepted typed-nil activation gate")
	}
}

func TestManagerProductionBlockedGateBlocksDecommissionBeforeDelegate(t *testing.T) {
	admission := &recordingAdmission{}
	decommissioner := &recordingDecommissioner{}
	manager, err := NewManager(ManagerConfig{
		Admission: admission, ActivationGate: providercontrol.ProductionBlockedGate{},
		Decommissioner: decommissioner,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	_, err = manager.DecommissionManagedLeases(t.Context(), jobs.ManagedLeaseDecommissionRequest{
		TenantID: "tenant-1", StackID: "stack-1", OwnerID: "owner-1", LeaseID: "lease-1",
	})
	if !errors.Is(err, providercontrol.ErrMutationActivationBlocked) {
		t.Fatalf("DecommissionManagedLeases error = %v, want ErrMutationActivationBlocked", err)
	}
	if decommissioner.calls != 0 {
		t.Fatalf("decommissioner calls = %d, want 0", decommissioner.calls)
	}
	if admission.calls != 0 {
		t.Fatalf("admission calls = %d, want 0", admission.calls)
	}
}

func TestManagerCreateForwardsCanonicalSecretFreeAdmission(t *testing.T) {
	admission := &recordingAdmission{}
	manager, err := NewManager(ManagerConfig{Admission: admission, ActivationGate: allowGate{}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	request := validManagedLeaseRequest()
	request.Metadata["api_token"] = "must-not-cross-providercontroljobs"

	result, err := manager.CreateOrBindLease(t.Context(), request)
	if err != nil {
		t.Fatalf("CreateOrBindLease: %v", err)
	}
	if admission.calls != 1 {
		t.Fatalf("admission calls = %d, want 1", admission.calls)
	}
	got := admission.last
	if got.TenantID != "tenant-1" || got.OwnerSubjectID != "owner-1" ||
		got.RuntimeServerID == "" || got.IdempotencyKey == "" {
		t.Fatalf("authority identity was not forwarded: %+v", got)
	}
	if got.Lease.ID == "" || got.Lease.Resource.ProviderID != "ionos" ||
		got.Lease.Subject != (vmlease.Subject{Kind: vmlease.SubjectUser, ID: "owner-1", OrgID: "tenant-1"}) ||
		got.Lease.DesiredState != vmlease.DesiredStateRunning ||
		got.Lease.BillingMode != vmlease.BillingModeSubscription ||
		got.Lease.LifecycleClass != vmlease.LifecycleClassSubscription {
		t.Fatalf("canonical lease was not forwarded: %+v", got.Lease)
	}
	if got.Lease.ValidFrom.IsZero() == false || got.Lease.ValidUntil.IsZero() == false ||
		got.Lease.CancelledAt != nil {
		t.Fatalf("caller supplied lease authority time: %+v", got.Lease)
	}
	if got.Lease.Metadata[monthlyruntime.MetadataKeyRuntimeLane] != serverruntime.RuntimeLaneMonthly ||
		got.Lease.Metadata[monthlyruntime.MetadataKeyServerMode] != serverruntime.RuntimeLaneMonthly ||
		got.Lease.Metadata[monthlyruntime.MetadataKeyBillingMode] != monthlyruntime.BillingSubscription {
		t.Fatalf("managed runtime lease is not visible to product inventory: %+v", got.Lease.Metadata)
	}
	if got.Server.StackID != "stack-1" || got.Server.Name != "Production Stack" ||
		got.Server.Metadata["provider_id"] != "ionos" ||
		got.Server.Metadata["offering_id"] != "monthly-runtime-premium" ||
		got.Server.Metadata["stackkit"] != "cloud-kit" ||
		got.Server.Metadata["node_role"] != "worker" ||
		!reflect.DeepEqual(got.Server.Metadata["services"], []string{"pocket_id", "vaultwarden"}) {
		t.Fatalf("canonical server data was not forwarded: %+v", got.Server)
	}
	if _, leaked := got.Server.Metadata["api_token"]; leaked {
		t.Fatalf("server metadata contains caller credential field: %+v", got.Server.Metadata)
	}
	if got.DesiredSpecRef != "desired-spec://techstack/leases/"+string(got.Lease.ID)+"/revisions/1" {
		t.Fatalf("desired spec ref = %q", got.DesiredSpecRef)
	}
	if bytes.Contains(got.DesiredSpec, []byte("must-not-cross")) ||
		bytes.Contains(bytes.ToLower(got.DesiredSpec), []byte("token")) {
		t.Fatalf("desired spec leaked credential input: %s", got.DesiredSpec)
	}
	var desired map[string]any
	if err := json.Unmarshal(got.DesiredSpec, &desired); err != nil {
		t.Fatalf("decode desired spec: %v", err)
	}
	wantDesired := map[string]any{
		"schema_version":          desiredSpecSchemaVersion,
		"provider_id":             "ionos",
		"offering_id":             "monthly-runtime-premium",
		"region":                  "de-fra",
		"stack_id":                "stack-1",
		"stack_name":              "Production Stack",
		"stackkit":                "cloud-kit",
		"node_role":               "worker",
		"runtime_slot_key":        jobs.PrimaryManagedRuntimeSlotKey,
		"runtime_slot_generation": float64(1),
		"services":                []any{"pocket_id", "vaultwarden"},
	}
	if !reflect.DeepEqual(desired, wantDesired) {
		t.Fatalf("desired spec = %#v, want %#v", desired, wantDesired)
	}
	if result == nil || result.LeaseID != string(got.Lease.ID) || result.Provider != "ionos" ||
		result.RuntimeSlotKey != jobs.PrimaryManagedRuntimeSlotKey || result.RuntimeSlotID != got.RuntimeSlotID ||
		result.RuntimeServerID != got.RuntimeServerID || result.ResourceGenerationID == "" || result.OperationID != "operation-1" ||
		result.DesiredState != string(vmlease.DesiredStateRunning) ||
		result.BillingMode != string(vmlease.BillingModeSubscription) ||
		result.Phase != jobs.RuntimePhaseLeasePending {
		t.Fatalf("managed lease result = %+v", result)
	}
}

func TestManagerDefaultsRuntimeSlotToPrimaryIndependentOfNodeRole(t *testing.T) {
	for _, role := range []string{"foundation", "worker", "storage"} {
		t.Run(role, func(t *testing.T) {
			request := validManagedLeaseRequest()
			request.NodeRole = role
			request.RuntimeSlotKey = ""

			normalized, err := normalizeManagedLeaseRequest(request)
			if err != nil {
				t.Fatalf("normalizeManagedLeaseRequest: %v", err)
			}
			if normalized.admission.RuntimeSlotKey != jobs.PrimaryManagedRuntimeSlotKey ||
				normalized.admission.Lease.Metadata["node_role"] != role {
				t.Fatalf("slot/role = %q/%q, want primary/%q",
					normalized.admission.RuntimeSlotKey,
					normalized.admission.Lease.Metadata["node_role"],
					role,
				)
			}
		})
	}
}

func TestManagerCreateRejectsLegacyCompositeProviderIDs(t *testing.T) {
	for _, providerID := range []string{"centron-managed", "ionos-managed"} {
		t.Run(providerID, func(t *testing.T) {
			admission := &recordingAdmission{}
			manager, err := NewManager(ManagerConfig{Admission: admission, ActivationGate: allowGate{}})
			if err != nil {
				t.Fatalf("NewManager: %v", err)
			}
			request := validManagedLeaseRequest()
			request.Provider = providerID

			_, err = manager.CreateOrBindLease(t.Context(), request)
			if err == nil || !strings.Contains(err.Error(), "canonical provider_id") {
				t.Fatalf("CreateOrBindLease error = %v, want canonical provider rejection", err)
			}
			if admission.calls != 0 {
				t.Fatalf("admission calls = %d, want 0", admission.calls)
			}
		})
	}
}

func TestManagerRuntimeSlotSeparatesServersAndTransportKeysDoNot(t *testing.T) {
	admission := &recordingAdmission{}
	manager, err := NewManager(ManagerConfig{Admission: admission, ActivationGate: allowGate{}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	first := validManagedLeaseRequest()
	first.OperationKey = "add-server-job-a"
	second := validManagedLeaseRequest()
	second.OperationKey = "add-server-job-b"
	if _, err = manager.CreateOrBindLease(t.Context(), first); err != nil {
		t.Fatalf("CreateOrBindLease(first): %v", err)
	}
	if _, err = manager.CreateOrBindLease(t.Context(), second); err != nil {
		t.Fatalf("CreateOrBindLease(second): %v", err)
	}
	if len(admission.requests) != 2 {
		t.Fatalf("admission requests = %d, want 2", len(admission.requests))
	}
	left, right := admission.requests[0], admission.requests[1]
	if left.Lease.ID != right.Lease.ID || left.RuntimeServerID != right.RuntimeServerID ||
		left.IdempotencyKey != right.IdempotencyKey || left.RuntimeSlotID != right.RuntimeSlotID {
		t.Fatalf("transport retry keys changed slot identity: left=%+v right=%+v", left, right)
	}
	for _, admitted := range []providercontrol.NativeProvisionAdmissionRequest{left, right} {
		encoded, marshalErr := json.Marshal(admitted)
		if marshalErr != nil {
			t.Fatalf("marshal admission: %v", marshalErr)
		}
		if bytes.Contains(encoded, []byte("add-server-job-")) {
			t.Fatalf("raw operation key crossed the native boundary: %s", encoded)
		}
	}

	replay := validManagedLeaseRequest()
	replay.OperationKey = "a-third-transport-key"
	replay.Services = []string{"pocket_id", "vaultwarden", "Pocket-ID"}
	admission.replay = true
	replayResult, replayErr := manager.CreateOrBindLease(t.Context(), replay)
	if replayErr != nil {
		t.Fatalf("CreateOrBindLease(replay): %v", replayErr)
	}
	if replayResult == nil || !replayResult.IdempotentReplay {
		t.Fatalf("replay result = %+v, want idempotent replay", replayResult)
	}
	replayed := admission.requests[2]
	if replayed.Lease.ID != left.Lease.ID || replayed.RuntimeServerID != left.RuntimeServerID ||
		replayed.IdempotencyKey != left.IdempotencyKey || !bytes.Equal(replayed.DesiredSpec, left.DesiredSpec) {
		t.Fatalf("same slot intent was not stable: first=%+v replay=%+v", left, replayed)
	}

	differentSlot := validManagedLeaseRequest()
	differentSlot.OperationKey = "new-transport-key"
	differentSlot.RuntimeSlotKey = "worker-2"
	if _, err = manager.CreateOrBindLease(t.Context(), differentSlot); err != nil {
		t.Fatalf("CreateOrBindLease(different slot): %v", err)
	}
	other := admission.requests[3]
	if other.RuntimeSlotID == left.RuntimeSlotID || other.Lease.ID == left.Lease.ID ||
		other.RuntimeServerID == left.RuntimeServerID || other.IdempotencyKey == left.IdempotencyKey {
		t.Fatalf("distinct explicit slot reused resource identity: first=%+v other=%+v", left, other)
	}
}

func TestManagerReleasedRuntimeSlotUsesDistinctGenerationIdentities(t *testing.T) {
	admission := &recordingAdmission{}
	manager, err := NewManager(ManagerConfig{Admission: admission, ActivationGate: allowGate{}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	first := validManagedLeaseRequest()
	first.OperationKey = "generation-one-job"
	first.RuntimeSlotGeneration = 1
	second := first
	second.OperationKey = "generation-two-job"
	second.RuntimeSlotGeneration = 2
	if _, err = manager.CreateOrBindLease(t.Context(), first); err != nil {
		t.Fatalf("CreateOrBindLease(generation one): %v", err)
	}
	if _, err = manager.CreateOrBindLease(t.Context(), second); err != nil {
		t.Fatalf("CreateOrBindLease(generation two): %v", err)
	}

	left, right := admission.requests[0], admission.requests[1]
	if left.RuntimeSlotID != right.RuntimeSlotID || left.RuntimeSlotGeneration != 1 ||
		right.RuntimeSlotGeneration != 2 {
		t.Fatalf("slot/generation mismatch: first=%+v second=%+v", left, right)
	}
	if left.Lease.ID == right.Lease.ID || left.RuntimeServerID == right.RuntimeServerID ||
		left.IdempotencyKey == right.IdempotencyKey || left.DesiredSpecRef == right.DesiredSpecRef {
		t.Fatalf("released slot reused generation identity: first=%+v second=%+v", left, right)
	}
	if bytes.Equal(left.DesiredSpec, right.DesiredSpec) {
		t.Fatalf("generation is absent from desired spec: first=%s second=%s", left.DesiredSpec, right.DesiredSpec)
	}
}

func TestManagerRejectsMissingOperationKeyBeforeAdmission(t *testing.T) {
	admission := &recordingAdmission{}
	manager, err := NewManager(ManagerConfig{Admission: admission, ActivationGate: allowGate{}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	request := validManagedLeaseRequest()
	request.OperationKey = ""
	if _, err = manager.CreateOrBindLease(t.Context(), request); err == nil || !strings.Contains(err.Error(), "operation key") {
		t.Fatalf("CreateOrBindLease error = %v, want operation-key rejection", err)
	}
	if admission.calls != 0 {
		t.Fatalf("admission calls = %d, want 0", admission.calls)
	}
}

func TestManagerDecommissionWithoutDelegateFailsClosed(t *testing.T) {
	manager, err := NewManager(ManagerConfig{
		Admission: &recordingAdmission{}, ActivationGate: allowGate{},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	result, err := manager.DecommissionManagedLeases(t.Context(), jobs.ManagedLeaseDecommissionRequest{
		TenantID: "tenant-1", StackID: "stack-1", OwnerID: "owner-1", LeaseID: "lease-1",
	})
	if !errors.Is(err, ErrNativeDecommissionUnavailable) {
		t.Fatalf("DecommissionManagedLeases error = %v, want ErrNativeDecommissionUnavailable", err)
	}
	if result != nil {
		t.Fatalf("DecommissionManagedLeases result = %+v, want nil", result)
	}
}

func validManagedLeaseRequest() jobs.ManagedLeaseRequest {
	return jobs.ManagedLeaseRequest{
		TenantID: "tenant-1", OwnerID: "owner-1", StackID: "stack-1",
		StackName: "Production Stack", StackKit: "cloud-kit", Provider: "ionos",
		OperationKey: "add-server-operation-1", NodeRole: "worker",
		Services: []string{"Vaultwarden", "pocket-id", "vaultwarden"},
		Metadata: map[string]string{
			"runtime_offering_id": "monthly-runtime-premium",
			"provider_region":     "de-fra",
		},
	}
}

var (
	_ providercontrol.MutationActivationGate = allowGate{}
	_ ProvisionAdmission                     = (*recordingAdmission)(nil)
	_ jobs.ManagedLeaseDecommissioner        = (*recordingDecommissioner)(nil)
)
