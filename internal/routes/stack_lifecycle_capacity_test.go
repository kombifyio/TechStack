package routes

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/edgeauth"
	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	jobruntime "github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/middleware"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

type keyedLifecycleFeatureChecker struct {
	enabled map[string]bool
}

type signedLifecycleFeatureChecker struct{}

func (signedLifecycleFeatureChecker) IsEnabled(ctx context.Context, key, _ string) (bool, error) {
	grants, ok := middleware.SignedEntitlementsFromContext(ctx)
	return ok && grants.Has(key), nil
}

type capacityCheckingManagedLeaseManager struct {
	policy        monthlyruntime.CapacityPolicyResolver
	checks        []monthlyruntime.CapacityPolicyRequest
	checkErrs     []error
	preflights    int
	preflightErr  error
	preflightErrs []error
	admissionErr  error
	admitted      stackLifecycleManagedLeaseManager
}

func (m *capacityCheckingManagedLeaseManager) ResolveManagedRuntimeSlotGeneration(
	ctx context.Context,
	req jobruntime.ManagedRuntimeSlotGenerationRequest,
) (jobruntime.ManagedRuntimeSlotGeneration, error) {
	return m.admitted.ResolveManagedRuntimeSlotGeneration(ctx, req)
}

func (m *capacityCheckingManagedLeaseManager) check(ctx context.Context, req jobruntime.ManagedLeaseRequest) error {
	request := monthlyruntime.CapacityPolicyRequest{
		TenantID: req.TenantID, OwnerSubjectID: req.OwnerID, ProviderID: req.Provider,
	}
	m.checks = append(m.checks, request)
	_, err := m.policy.ResolveCapacity(ctx, request)
	m.checkErrs = append(m.checkErrs, err)
	return err
}

func (m *capacityCheckingManagedLeaseManager) PreflightCreateOrBindLease(ctx context.Context, req jobruntime.ManagedLeaseRequest) error {
	m.preflights++
	if err := m.check(ctx, req); err != nil {
		return err
	}
	if len(m.preflightErrs) > 0 {
		err := m.preflightErrs[0]
		m.preflightErrs = m.preflightErrs[1:]
		return err
	}
	return m.preflightErr
}

func (m *capacityCheckingManagedLeaseManager) CreateOrBindLease(ctx context.Context, req jobruntime.ManagedLeaseRequest) (*jobruntime.ManagedLeaseResult, error) {
	if err := m.check(ctx, req); err != nil {
		return nil, err
	}
	if m.admissionErr != nil {
		return nil, m.admissionErr
	}
	return m.admitted.CreateOrBindLease(ctx, req)
}

func (f keyedLifecycleFeatureChecker) IsEnabled(_ context.Context, key string, _ string) (bool, error) {
	return f.enabled[key], nil
}

func allProviderFeaturesEnabled() keyedLifecycleFeatureChecker {
	return keyedLifecycleFeatureChecker{enabled: map[string]bool{
		monthlyruntime.FeatureTechStackManagedRuntime:         true,
		monthlyruntime.FeatureTechStackManagedRuntimeCloudKit: true,
		monthlyruntime.FeatureTechStackManagedRuntimeCentron:  true,
		monthlyruntime.FeatureTechStackManagedRuntimeIONOS:    true,
	}}
}

func seedCapacityTestLease(t *testing.T, leases *vmleases.Service, now time.Time, id string) {
	t.Helper()
	lease := vmlease.Lease{
		ID:      vmlease.LeaseID(id),
		Subject: vmlease.Subject{Kind: vmlease.SubjectUser, ID: "owner-1", OrgID: "owner-1"},
		Resource: vmlease.ResourceRef{
			ProviderID: monthlyruntime.ProviderCentron,
			EngineVMID: "engine-" + id,
		},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyManual,
		ValidFrom:      now.Add(-time.Hour),
		ValidUntil:     now.Add(time.Hour),
		RenewedAt:      now,
		Metadata:       monthlyruntime.NormalizeMetadata(map[string]string{"stack_id": "stack-seed"}, serverruntime.RuntimeOfferingStandard),
	}
	if _, err := leases.CreateOrUpdate(context.Background(), vmleases.CreateRequest{Lease: lease}); err != nil {
		t.Fatalf("seed lease %s: %v", id, err)
	}
}

func TestAddManagedRuntimeServerDefersCapacityToNativeAdmission(t *testing.T) {
	t.Setenv("TECHSTACK_MANAGED_RUNTIME_MAX_SERVERS", "1")
	store := controlplane.NewMemoryStore()
	stack, err := store.CreateStack(context.Background(), controlplane.CreateStackRequest{
		ID: "stack-capacity", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Status: "pending",
		Config: map[string]any{"server_provisioning_mode": "kombify-cloud", "lease_provider": "centron-managed"},
	})
	if err != nil {
		t.Fatalf("seed stack: %v", err)
	}
	now := time.Date(2026, 7, 5, 9, 0, 0, 0, time.UTC)
	leaseService := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{
		Now:            func() time.Time { return now },
		SnapshotSecret: []byte("secret"),
	})
	leases := &nativeTestLeaseService{Service: leaseService}
	for _, id := range []string{"lease-seed-1", "lease-seed-2", "lease-seed-3"} {
		seedCapacityTestLease(t, leaseService, now, id)
	}

	managedLeases := &stackLifecycleManagedLeaseManager{}
	handlers := stackLifecycleRouteHandlers{
		leases: leases, features: allProviderFeaturesEnabled(), stacks: store,
		jobs: store, managedLeases: managedLeases,
	}
	body := `{"node_role":"worker","provider_id":"centron"}`
	event, recorder := stackLifecycleRouteTestEvent(http.MethodPost, "/api/v1/stacks/"+stack.ID+"/managed-runtimes", body, "owner-1", "tenant-1")
	event.Request.SetPathValue("id", stack.ID)
	if err := handlers.addManagedRuntimeServer(event); err != nil {
		t.Fatalf("addManagedRuntimeServer returned router error: %v", err)
	}
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s, want 202 from native admission", recorder.Code, recorder.Body.String())
	}
	if len(managedLeases.requests) != 1 {
		t.Fatalf("native admission calls = %d, want 1", len(managedLeases.requests))
	}
}

func TestAddManagedRuntimeServerConsumesRequestBoundCapacityDecision(t *testing.T) {
	for _, tc := range []struct {
		name             string
		providerID       string
		entitledProvider string
		decision         string
		wantStatus       int
		wantChecks       int
		wantAdmissions   int
		wantJobs         int
	}{
		{name: "valid IONOS v2 decision", providerID: monthlyruntime.ProviderIONOS, entitledProvider: monthlyruntime.ProviderIONOS, decision: "v2", wantStatus: http.StatusAccepted, wantChecks: 3, wantAdmissions: 1, wantJobs: 1},
		{name: "detached v1 budget", providerID: monthlyruntime.ProviderIONOS, entitledProvider: monthlyruntime.ProviderIONOS, decision: "v1", wantStatus: http.StatusUnauthorized},
		{name: "missing decision budget", providerID: monthlyruntime.ProviderIONOS, entitledProvider: monthlyruntime.ProviderIONOS, decision: "none", wantStatus: http.StatusServiceUnavailable, wantChecks: 1},
		{name: "IONOS entitlement cannot authorize Centron", providerID: monthlyruntime.ProviderCentron, entitledProvider: monthlyruntime.ProviderIONOS, decision: "v2", wantStatus: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stackID := "stack-edge-capacity"
			handlers, store, manager := newEdgeCapacityLifecycleHandlers(t, stackID)
			event, recorder := edgeCapacityLifecycleEvent(t, stackID, tc.providerID, tc.entitledProvider, tc.decision)

			serveEdgeProtectedAddServer(t, handlers, event, recorder)

			if recorder.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s, want %d", recorder.Code, recorder.Body.String(), tc.wantStatus)
			}
			if len(manager.checks) != tc.wantChecks || len(manager.admitted.requests) != tc.wantAdmissions {
				t.Fatalf("capacity checks/admissions=%d/%d errors=%v, want %d/%d", len(manager.checks), len(manager.admitted.requests), manager.checkErrs, tc.wantChecks, tc.wantAdmissions)
			}
			jobs, err := store.ListJobsByStack(context.Background(), "tenant-1", stackID, 10)
			if err != nil {
				t.Fatalf("ListJobsByStack: %v", err)
			}
			if len(jobs) != tc.wantJobs {
				t.Fatalf("jobs=%#v, want %d", jobs, tc.wantJobs)
			}
			if tc.wantAdmissions == 1 {
				if len(jobs) != 1 || manager.admitted.requests[0].OperationKey != jobs[0].ID ||
					manager.admitted.requests[0].Provider != tc.providerID {
					t.Fatalf("native request=%+v jobs=%+v, want exact job operation key/provider", manager.admitted.requests, jobs)
				}
			}
			if tc.decision == "none" {
				if manager.preflights != 1 || len(manager.admitted.preflightRequests) != 0 {
					t.Fatalf("missing decision preflights/downstream preflights=%d/%d, want 1/0", manager.preflights, len(manager.admitted.preflightRequests))
				}
				details := decodeErrorDetails(t, recorder)
				if details["reason_code"] != "managed_runtime_capacity_policy_unavailable" {
					t.Fatalf("missing decision details=%#v, want capacity-policy reason", details)
				}
			}
		})
	}
}

func TestManagedRuntimeAuthorityPreflightProvesSignedBudgetWithoutAdmissionWrite(t *testing.T) {
	for _, providerID := range []string{monthlyruntime.ProviderCentron, monthlyruntime.ProviderIONOS} {
		t.Run(providerID, func(t *testing.T) {
			handlers, _, manager := newEdgeCapacityLifecycleHandlers(t, "stack-edge-authority")
			event, recorder := edgeCapacityAuthorityPreflightEvent(t, providerID, providerID, "v2")
			edgeMiddleware := middleware.EdgeIdentityMiddlewareWithConfig(middleware.EdgeIdentityConfig{
				Mode: config.ModeSaaS, EdgeAuthSecret: "edge-capacity-secret",
				EdgeFlagsSecret: "edge-capacity-flags-secret", EdgeFlagsKeyID: "flags-primary",
				EdgeSignatureWindow: 5 * time.Minute,
			})
			if err := edgeMiddleware(event); err != nil {
				t.Fatalf("edge middleware: %v", err)
			}
			if err := handlers.managedRuntimeAuthorityPreflight(event); err != nil {
				t.Fatalf("managed runtime authority preflight: %v", err)
			}
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s, want 200", recorder.Code, recorder.Body.String())
			}
			var response struct {
				Data struct {
					ProviderID         string `json:"provider_id"`
					BudgetKey          string `json:"budget_key"`
					DecisionSource     string `json:"decision_source"`
					RequestBound       bool   `json:"request_bound"`
					AdmissionPreflight bool   `json:"admission_preflight"`
				} `json:"data"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode authority preflight response: %v", err)
			}
			if response.Data.ProviderID != providerID ||
				response.Data.BudgetKey != monthlyruntime.CapacityBudgetCloudRuntimeCredits ||
				response.Data.DecisionSource != monthlyruntime.CapacityDecisionSourceSignedRuntimeBudget ||
				!response.Data.RequestBound || !response.Data.AdmissionPreflight {
				t.Fatalf("authority response=%+v, want verified request-bound %s budget", response.Data, providerID)
			}
			if manager.preflights != 1 || len(manager.admitted.requests) != 0 {
				t.Fatalf("preflights/admissions=%d/%d, want 1/0", manager.preflights, len(manager.admitted.requests))
			}
		})
	}
}

func TestManagedRuntimeAuthorityPreflightRejectsMissingSignedBudgetWithoutAdmissionWrite(t *testing.T) {
	handlers, _, manager := newEdgeCapacityLifecycleHandlers(t, "stack-edge-authority")
	event, recorder := edgeCapacityAuthorityPreflightEvent(t, monthlyruntime.ProviderIONOS, monthlyruntime.ProviderIONOS, "none")
	edgeMiddleware := middleware.EdgeIdentityMiddlewareWithConfig(middleware.EdgeIdentityConfig{
		Mode: config.ModeSaaS, EdgeAuthSecret: "edge-capacity-secret",
		EdgeFlagsSecret: "edge-capacity-flags-secret", EdgeFlagsKeyID: "flags-primary",
		EdgeSignatureWindow: 5 * time.Minute,
	})
	if err := edgeMiddleware(event); err != nil {
		t.Fatalf("edge middleware: %v", err)
	}
	if err := handlers.managedRuntimeAuthorityPreflight(event); err != nil {
		t.Fatalf("managed runtime authority preflight: %v", err)
	}
	details := decodeErrorDetails(t, recorder)
	if recorder.Code != http.StatusServiceUnavailable || details["reason_code"] != "managed_runtime_capacity_policy_unavailable" {
		t.Fatalf("status=%d details=%#v, want signed-budget denial", recorder.Code, details)
	}
	if manager.preflights != 1 || len(manager.admitted.requests) != 0 {
		t.Fatalf("preflights/admissions=%d/%d, want 1/0", manager.preflights, len(manager.admitted.requests))
	}
}

func TestManagedRuntimeAuthorityPreflightRejectsDirectUnboundRequestWithoutAdmissionWrite(t *testing.T) {
	handlers, _, manager := newEdgeCapacityLifecycleHandlers(t, "stack-edge-authority")
	event, recorder := stackLifecycleRouteTestEvent(
		http.MethodGet, "/api/v1/managed-runtimes/authority?provider_id=ionos", "", "owner-1", "tenant-1",
	)
	if err := handlers.managedRuntimeAuthorityPreflight(event); err != nil {
		t.Fatalf("managed runtime authority preflight: %v", err)
	}
	details := decodeErrorDetails(t, recorder)
	if recorder.Code != http.StatusServiceUnavailable || details["reason_code"] != "request_bound_edge_authority_required" {
		t.Fatalf("status=%d details=%#v, want request-bound Gateway denial", recorder.Code, details)
	}
	if manager.preflights != 0 || len(manager.admitted.requests) != 0 {
		t.Fatalf("preflights/admissions=%d/%d, want 0/0", manager.preflights, len(manager.admitted.requests))
	}
}

func TestAddManagedRuntimeServerPreflightDenialsCreateNoJob(t *testing.T) {
	for _, tc := range []struct {
		name       string
		preflight  error
		reasonCode string
	}{
		{name: "closed native activation", preflight: providercontrol.MutationActivationBlockedError{}, reasonCode: "provider_control_not_activated"},
		{name: "unclassified provider custody", preflight: providercontrol.ManagedRuntimeUnclassifiedCustodyError{ProviderID: monthlyruntime.ProviderIONOS, StackID: "stack-edge-preflight-denial"}, reasonCode: "provider_custody_reconciliation_required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stackID := "stack-edge-preflight-denial"
			handlers, store, manager := newEdgeCapacityLifecycleHandlers(t, stackID)
			manager.preflightErr = tc.preflight
			event, recorder := edgeCapacityLifecycleEvent(
				t, stackID, monthlyruntime.ProviderIONOS, monthlyruntime.ProviderIONOS, "v2",
			)

			serveEdgeProtectedAddServer(t, handlers, event, recorder)

			details := decodeErrorDetails(t, recorder)
			jobs, err := store.ListJobsByStack(context.Background(), "tenant-1", stackID, 10)
			if err != nil {
				t.Fatalf("ListJobsByStack: %v", err)
			}
			if recorder.Code != http.StatusServiceUnavailable || details["reason_code"] != tc.reasonCode ||
				len(jobs) != 0 || len(manager.admitted.requests) != 0 || manager.preflights != 1 {
				t.Fatalf("status=%d details=%#v jobs/admissions/preflights=%d/%d/%d, want fail-closed no-write %s",
					recorder.Code, details, len(jobs), len(manager.admitted.requests), manager.preflights, tc.reasonCode)
			}
		})
	}
}

func TestAddManagedRuntimeServerSecondPreflightDenialReportsPendingJobHonestly(t *testing.T) {
	stackID := "stack-edge-preflight-race"
	handlers, store, manager := newEdgeCapacityLifecycleHandlers(t, stackID)
	manager.preflightErrs = []error{nil, providercontrol.MutationActivationBlockedError{}}
	event, recorder := edgeCapacityLifecycleEvent(
		t, stackID, monthlyruntime.ProviderIONOS, monthlyruntime.ProviderIONOS, "v2",
	)

	serveEdgeProtectedAddServer(t, handlers, event, recorder)

	jobs, err := store.ListJobsByStack(context.Background(), "tenant-1", stackID, 10)
	if err != nil {
		t.Fatalf("ListJobsByStack: %v", err)
	}
	details := decodeErrorDetails(t, recorder)
	if len(jobs) != 1 || jobs[0].State != monthlyRuntimeEnrollmentStatusPending ||
		recorder.Code != http.StatusServiceUnavailable || details["reason_code"] != "provider_control_not_activated" ||
		details[managedRuntimeDetailsJobIDKey] != jobs[0].ID ||
		len(manager.admitted.requests) != 0 || manager.preflights != 2 {
		t.Fatalf("status=%d details=%#v jobs=%+v admissions/preflights=%d/%d, want one named pending job and no admission",
			recorder.Code, details, jobs, len(manager.admitted.requests), manager.preflights)
	}
}

func TestAddManagedRuntimeServerTypedAdmissionRecheckDenialRetainsRunningSameKeyFence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		admission  error
		reasonCode string
	}{
		{
			name: "request-bound policy became unavailable", admission: providercontrol.ErrManagedRuntimeCapacityPolicyUnavailable,
			reasonCode: "managed_runtime_capacity_policy_unavailable",
		},
		{
			name: "native activation closed", admission: providercontrol.MutationActivationBlockedError{},
			reasonCode: "provider_control_not_activated",
		},
		{
			name: "unclassified provider custody", admission: providercontrol.ManagedRuntimeUnclassifiedCustodyError{
				ProviderID: monthlyruntime.ProviderIONOS, StackID: "stack-edge-admission-recheck",
			},
			reasonCode: "provider_custody_reconciliation_required",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stackID := "stack-edge-admission-recheck"
			handlers, store, manager := newEdgeCapacityLifecycleHandlers(t, stackID)
			manager.admissionErr = tc.admission
			event, recorder := edgeCapacityLifecycleEvent(
				t, stackID, monthlyruntime.ProviderIONOS, monthlyruntime.ProviderIONOS, "v2",
			)

			serveEdgeProtectedAddServer(t, handlers, event, recorder)

			jobs, err := store.ListJobsByStack(context.Background(), "tenant-1", stackID, 10)
			if err != nil {
				t.Fatalf("ListJobsByStack: %v", err)
			}
			details := decodeErrorDetails(t, recorder)
			if recorder.Code != http.StatusServiceUnavailable || len(jobs) != 1 || jobs[0].State != "running" ||
				details["reason_code"] != tc.reasonCode || details[managedRuntimeDetailsJobIDKey] != jobs[0].ID ||
				details["retry_scope"] != "exact_same_idempotency_key" || details["job_state"] != "running" ||
				details["attempt_outcome"] != "rejected_before_native_write" {
				t.Fatalf("status=%d jobs=%+v details=%#v, want typed attempt denial on exact running same-key fence",
					recorder.Code, jobs, details)
			}
			if len(manager.admitted.requests) != 0 || manager.preflights != 2 || len(manager.checks) != 3 {
				t.Fatalf("admissions/preflights/checks=%d/%d/%d, want 0/2/3",
					len(manager.admitted.requests), manager.preflights, len(manager.checks))
			}
		})
	}
}

func newEdgeCapacityLifecycleHandlers(
	t *testing.T,
	stackID string,
) (stackLifecycleRouteHandlers, *controlplane.MemoryStore, *capacityCheckingManagedLeaseManager) {
	t.Helper()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(context.Background(), controlplane.CreateStackRequest{
		ID: stackID, TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Edge Capacity",
		Status: "running", Config: map[string]any{
			"server_provisioning_mode": "kombify-cloud",
			"runtime_lane":             "monthly-runtime",
			"runtime_offering_id":      "monthly-runtime-standard",
			"stackkit_catalog_ref":     "cloud-kit",
		},
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	policy, err := monthlyruntime.NewManagedRuntimeCapacityPolicy(
		monthlyruntime.CapacityAuthoritySignedEdge,
		func(ctx context.Context, entitlement string) (bool, error) {
			grants, ok := middleware.SignedEntitlementsFromContext(ctx)
			return ok && grants.Has(entitlement), nil
		},
	)
	if err != nil {
		t.Fatalf("NewManagedRuntimeCapacityPolicy: %v", err)
	}
	manager := &capacityCheckingManagedLeaseManager{policy: policy}
	leases := vmleases.NewService(vmleases.NewMemoryStore(), vmleases.ServiceConfig{
		Now: func() time.Time { return time.Date(2026, 7, 22, 15, 0, 0, 0, time.UTC) }, SnapshotSecret: []byte("secret"),
	})
	return stackLifecycleRouteHandlers{
		leases: leases, features: signedLifecycleFeatureChecker{}, stacks: store, jobs: store, managedLeases: manager,
	}, store, manager
}

func edgeCapacityLifecycleEvent(
	t *testing.T,
	stackID string,
	providerID string,
	entitledProvider string,
	decision string,
) (*httpx.Event, *httptest.ResponseRecorder) {
	t.Helper()
	body := `{"node_role":"worker","provider_id":"` + providerID + `","provider_region":"de-txl","stackkit":"cloud-kit"}`
	event, recorder := stackLifecycleRouteTestEvent(
		http.MethodPost, "/api/v1/stacks/"+stackID+"/managed-runtimes", body, "", "",
	)
	event.Request.SetPathValue("id", stackID)
	event.Request.Header.Set(edgeauth.HeaderEdgeAuth, edgeauth.EdgeAuthValueJWT)
	event.Request.Header.Set(edgeauth.HeaderEdgeService, "techstack")
	event.Request.Header.Set(edgeauth.HeaderPublicPrefix, "/v1/techstack")
	event.Request.Header.Set(edgeauth.HeaderUserID, "owner-1")
	event.Request.Header.Set(edgeauth.HeaderOrgID, "tenant-1")
	event.Request.Header.Set(edgeauth.HeaderRequestID, "request-edge-capacity")
	providerEntitlement := monthlyruntime.FeatureTechStackManagedRuntimeIONOS
	if entitledProvider == monthlyruntime.ProviderCentron {
		providerEntitlement = monthlyruntime.FeatureTechStackManagedRuntimeCentron
	}
	event.Request.Header.Set(edgeauth.HeaderEntitlements, strings.Join([]string{
		monthlyruntime.FeatureTechStackManagedRuntime,
		monthlyruntime.FeatureTechStackManagedRuntimeCloudKit,
		providerEntitlement,
	}, ","))
	signEdgeCapacityIdentityV2(event.Request, "edge-capacity-secret")

	switch decision {
	case "v2":
		headers, err := edgeauth.SignDecisionHeaders(edgeauth.DecisionSignInput{
			Secret: "edge-capacity-flags-secret", KeyID: "flags-primary",
			Method: event.Request.Method, SignedPath: event.Request.URL.RequestURI(),
			Audience: "techstack", PublicPrefix: "/v1/techstack",
			SubjectID: "owner-1", TenantID: "tenant-1", RequestID: "request-edge-capacity",
			EdgeKeyID:     event.Request.Header.Get(edgeauth.HeaderEdgeKeyID),
			EdgeTimestamp: event.Request.Header.Get(edgeauth.HeaderEdgeTimestamp),
			EdgeNonce:     event.Request.Header.Get(edgeauth.HeaderEdgeNonce),
			EdgeSignature: event.Request.Header.Get(edgeauth.HeaderEdgeSignature),
			Flags: map[string]bool{
				monthlyruntime.FeatureTechStackManagedRuntime:         true,
				monthlyruntime.FeatureTechStackManagedRuntimeCloudKit: true,
				providerEntitlement: true,
			},
			Budgets: map[string]any{edgeauth.CloudRuntimeCreditsBudgetName: map[string]any{
				"managed_servers": map[string]any{"mode": "limited", "limit": 3},
			}},
		})
		if err != nil {
			t.Fatalf("SignDecisionHeaders: %v", err)
		}
		for header, values := range headers {
			for _, value := range values {
				event.Request.Header.Set(header, value)
			}
		}
	case "v1":
		headers, err := edgeauth.SignFlagHeaders(
			"edge-capacity-flags-secret", "flags-primary",
			map[string]bool{monthlyruntime.FeatureTechStackManagedRuntime: true},
			map[string]any{edgeauth.CloudRuntimeCreditsBudgetName: map[string]any{
				"managed_servers": map[string]any{"mode": "unlimited"},
			}},
			time.Now(),
		)
		if err != nil {
			t.Fatalf("SignFlagHeaders: %v", err)
		}
		for header, values := range headers {
			for _, value := range values {
				event.Request.Header.Set(header, value)
			}
		}
	case "none":
	default:
		t.Fatalf("unsupported decision fixture %q", decision)
	}
	return event, recorder
}

func edgeCapacityAuthorityPreflightEvent(
	t *testing.T,
	providerID string,
	entitledProvider string,
	decision string,
) (*httpx.Event, *httptest.ResponseRecorder) {
	t.Helper()
	event, recorder := stackLifecycleRouteTestEvent(
		http.MethodGet, "/api/v1/managed-runtimes/authority?provider_id="+providerID, "", "", "",
	)
	event.Request.Header.Set(edgeauth.HeaderEdgeAuth, edgeauth.EdgeAuthValueJWT)
	event.Request.Header.Set(edgeauth.HeaderEdgeService, "techstack")
	event.Request.Header.Set(edgeauth.HeaderPublicPrefix, "/v1/techstack")
	event.Request.Header.Set(edgeauth.HeaderUserID, "owner-1")
	event.Request.Header.Set(edgeauth.HeaderOrgID, "tenant-1")
	event.Request.Header.Set(edgeauth.HeaderRequestID, "request-edge-capacity")
	providerEntitlement := monthlyruntime.FeatureTechStackManagedRuntimeIONOS
	if entitledProvider == monthlyruntime.ProviderCentron {
		providerEntitlement = monthlyruntime.FeatureTechStackManagedRuntimeCentron
	}
	event.Request.Header.Set(edgeauth.HeaderEntitlements, strings.Join([]string{
		monthlyruntime.FeatureTechStackManagedRuntime,
		monthlyruntime.FeatureTechStackManagedRuntimeCloudKit,
		providerEntitlement,
	}, ","))
	signEdgeCapacityIdentityV2(event.Request, "edge-capacity-secret")

	switch decision {
	case "v2":
		headers, err := edgeauth.SignDecisionHeaders(edgeauth.DecisionSignInput{
			Secret: "edge-capacity-flags-secret", KeyID: "flags-primary",
			Method: event.Request.Method, SignedPath: event.Request.URL.RequestURI(),
			Audience: "techstack", PublicPrefix: "/v1/techstack",
			SubjectID: "owner-1", TenantID: "tenant-1", RequestID: "request-edge-capacity",
			EdgeKeyID:     event.Request.Header.Get(edgeauth.HeaderEdgeKeyID),
			EdgeTimestamp: event.Request.Header.Get(edgeauth.HeaderEdgeTimestamp),
			EdgeNonce:     event.Request.Header.Get(edgeauth.HeaderEdgeNonce),
			EdgeSignature: event.Request.Header.Get(edgeauth.HeaderEdgeSignature),
			Flags: map[string]bool{
				monthlyruntime.FeatureTechStackManagedRuntime:         true,
				monthlyruntime.FeatureTechStackManagedRuntimeCloudKit: true,
				providerEntitlement: true,
			},
			Budgets: map[string]any{edgeauth.CloudRuntimeCreditsBudgetName: map[string]any{
				"managed_servers": map[string]any{"mode": "limited", "limit": 3},
			}},
		})
		if err != nil {
			t.Fatalf("SignDecisionHeaders: %v", err)
		}
		for header, values := range headers {
			for _, value := range values {
				event.Request.Header.Set(header, value)
			}
		}
	case "none":
	default:
		t.Fatalf("unsupported decision fixture %q", decision)
	}
	return event, recorder
}

func serveEdgeProtectedAddServer(
	t *testing.T,
	handlers stackLifecycleRouteHandlers,
	event *httpx.Event,
	recorder *httptest.ResponseRecorder,
) {
	t.Helper()
	mw := middleware.EdgeIdentityMiddlewareWithConfig(middleware.EdgeIdentityConfig{
		Mode: config.ModeSaaS, EdgeAuthSecret: "edge-capacity-secret",
		EdgeFlagsSecret: "edge-capacity-flags-secret", EdgeFlagsKeyID: "flags-primary",
		EdgeSignatureWindow: 5 * time.Minute,
	})
	if err := mw(event); err != nil {
		t.Fatalf("edge middleware: %v", err)
	}
	if recorder.Code == http.StatusUnauthorized {
		return
	}
	if err := handlers.addManagedRuntimeServer(event); err != nil {
		t.Fatalf("addManagedRuntimeServer: %v", err)
	}
}

func signEdgeCapacityIdentityV2(request *http.Request, secret string) {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "edge-capacity-nonce"
	signedPath := request.URL.RequestURI()
	payload := strings.Join([]string{
		"v2", "primary", request.Method, signedPath,
		request.Header.Get(edgeauth.HeaderEdgeAuth),
		request.Header.Get(edgeauth.HeaderEdgeService),
		request.Header.Get(edgeauth.HeaderPublicPrefix),
		request.Header.Get(edgeauth.HeaderUserID),
		request.Header.Get(edgeauth.HeaderOrgID),
		request.Header.Get(edgeauth.HeaderUserEmail),
		request.Header.Get(edgeauth.HeaderUserTier),
		request.Header.Get(edgeauth.HeaderUserRoles),
		request.Header.Get(edgeauth.HeaderUserScope),
		request.Header.Get(edgeauth.HeaderEntitlements),
		request.Header.Get(edgeauth.HeaderKnowledgeTier),
		timestamp, nonce,
	}, "\n")
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	request.Header.Set(edgeauth.HeaderEdgeTimestamp, timestamp)
	request.Header.Set(edgeauth.HeaderEdgeNonce, nonce)
	request.Header.Set(edgeauth.HeaderEdgeSignedPath, signedPath)
	request.Header.Set(edgeauth.HeaderEdgeKeyID, "primary")
	request.Header.Set(edgeauth.HeaderEdgeSignature, "v2="+base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
}
