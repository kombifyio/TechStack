package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/middleware"
)

type providerResolutionAppFake struct {
	record        providercontrol.OperationRecord
	loadCalls     int
	discoverCalls int
	commitCalls   int
	discovery     providercontrol.ProvisionDiscoveryWorkflowRequest
	resolution    providercontrol.ProvisionResolutionWorkflowRequest
}

func (f *providerResolutionAppFake) LoadProvisionResolutionOperation(context.Context, string, string) (providercontrol.OperationRecord, error) {
	f.loadCalls++
	return f.record, nil
}

func (f *providerResolutionAppFake) DiscoverProvisionOperation(_ context.Context, request providercontrol.ProvisionDiscoveryWorkflowRequest) (providercontrol.ProvisionDiscoveryWorkflowResult, error) {
	f.discoverCalls++
	f.discovery = request
	return providercontrol.ProvisionDiscoveryWorkflowResult{Observation: providercontrol.ProvisionDiscoveryObservation{
		TenantID: request.TenantID, OperationID: request.OperationID, ObservationID: "observation-1",
		SnapshotDigest: "sha256:snapshot", ObservedResolutionRevision: 2,
		Candidates: []providercontrol.ProvisionCandidateGraph{{}},
	}, Created: true}, nil
}

func (f *providerResolutionAppFake) CommitProvisionOperation(_ context.Context, request providercontrol.ProvisionResolutionWorkflowRequest) (providercontrol.ProvisionResolutionWorkflowResult, error) {
	f.commitCalls++
	f.resolution = request
	return providercontrol.ProvisionResolutionWorkflowResult{
		Decision:  providercontrol.ProvisionResolutionDecision{Outcome: providercontrol.ProvisionResolutionAdoptedExactCandidate, DecisionDigest: "sha256:decision"},
		Operation: providercontrol.OperationRecord{Head: providerexecutor.Receipt{Phase: providerexecutor.PhasePresent}}, DecisionCreated: true,
	}, nil
}

type providerResolutionPolicyFake struct {
	err   error
	calls int
	auth  InventoryAuthorization
}

func (f *providerResolutionPolicyFake) AuthorizeInventory(_ context.Context, auth InventoryAuthorization) (InventoryDecision, error) {
	f.calls++
	f.auth = auth
	return InventoryDecision{}, f.err
}

func TestProviderResolutionRoutesEnforceAdminFGAAndProviderDerivedInput(t *testing.T) {
	t.Run("member is denied before application access", func(t *testing.T) {
		app := providerResolutionRouteApp()
		policy := &providerResolutionPolicyFake{}
		response := providerResolutionRequest(t, app, policy, "member", `/api/v1/provider-operations/op-1/provision-discovery`, `{}`)
		if response.Code != http.StatusForbidden || app.loadCalls != 0 || app.discoverCalls != 0 || policy.calls != 0 {
			t.Fatalf("member response=%d app=%+v policy=%+v", response.Code, app, policy)
		}
	})

	t.Run("FGA denial occurs before provider discovery", func(t *testing.T) {
		app := providerResolutionRouteApp()
		policy := &providerResolutionPolicyFake{err: ErrInventoryAccessDenied}
		response := providerResolutionRequest(t, app, policy, "admin", `/api/v1/provider-operations/op-1/provision-discovery`, `{}`)
		if response.Code != http.StatusForbidden || app.loadCalls != 1 || app.discoverCalls != 0 || policy.calls != 1 {
			t.Fatalf("denied response=%d app=%+v policy=%+v", response.Code, app, policy)
		}
		if policy.auth.ResourceID != "server-1" || policy.auth.Action != InventoryActionOperate || policy.auth.SubjectID != "operator-1" {
			t.Fatalf("authorization = %+v", policy.auth)
		}
	})

	t.Run("discovery accepts no caller-derived provider facts", func(t *testing.T) {
		app := providerResolutionRouteApp()
		policy := &providerResolutionPolicyFake{}
		response := providerResolutionRequest(t, app, policy, "admin", `/api/v1/provider-operations/op-1/provision-discovery`, `{}`)
		if response.Code != http.StatusOK || app.discoverCalls != 1 {
			t.Fatalf("discovery response=%d body=%s app=%+v", response.Code, response.Body.String(), app)
		}
		if app.discovery.TenantID != "tenant-1" || app.discovery.OperationID != "op-1" || app.discovery.OperatorSubjectID != "operator-1" {
			t.Fatalf("discovery request = %+v", app.discovery)
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		encoded := response.Body.String()
		if payload["data"] == nil || bytes.Contains([]byte(encoded), []byte("provider_handle")) {
			t.Fatalf("unsafe discovery response = %s", encoded)
		}
	})

	t.Run("resolution rejects caller supplied outcome fields", func(t *testing.T) {
		app := providerResolutionRouteApp()
		policy := &providerResolutionPolicyFake{}
		response := providerResolutionRequest(t, app, policy, "admin", `/api/v1/provider-operations/op-1/provision-resolution`, `{"confirmation":"adopt-provision:op-1","outcome":"adopted_exact_candidate"}`)
		if response.Code != http.StatusBadRequest || app.loadCalls != 0 || app.commitCalls != 0 || policy.calls != 0 {
			t.Fatalf("unknown field response=%d app=%+v policy=%+v", response.Code, app, policy)
		}
	})

	t.Run("confirmed resolution is bound to authenticated operator", func(t *testing.T) {
		app := providerResolutionRouteApp()
		policy := &providerResolutionPolicyFake{}
		response := providerResolutionRequest(t, app, policy, "admin", `/api/v1/provider-operations/op-1/provision-resolution`, `{"confirmation":"adopt-provision:op-1"}`)
		if response.Code != http.StatusOK || app.commitCalls != 1 {
			t.Fatalf("resolution response=%d body=%s app=%+v", response.Code, response.Body.String(), app)
		}
		if app.resolution.TenantID != "tenant-1" || app.resolution.OperationID != "op-1" || app.resolution.OperatorSubjectID != "operator-1" || app.resolution.Confirmation != "adopt-provision:op-1" {
			t.Fatalf("resolution request = %+v", app.resolution)
		}
	})
}

func providerResolutionRouteApp() *providerResolutionAppFake {
	return &providerResolutionAppFake{record: providercontrol.OperationRecord{Command: providerexecutor.Command{RuntimeServerID: "server-1"}}}
}

func providerResolutionRequest(t *testing.T, app *providerResolutionAppFake, policy *providerResolutionPolicyFake, role, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := httpx.NewRouter()
	RegisterProviderResolutionRoutes(router, ProviderResolutionRouteConfig{Application: app, Policy: policy})
	request := httptest.NewRequest(http.MethodPost, target, bytes.NewBufferString(body))
	ctx := identity.NewContext(request.Context(), &identity.Identity{UserID: "operator-1", OrgID: "tenant-1", Roles: []string{role}})
	ctx = middleware.WithSignedEntitlements(ctx, InventoryEntitlementOperate)
	request = request.WithContext(ctx)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

var _ providerResolutionApplication = (*providerResolutionAppFake)(nil)
var _ InventoryPolicy = (*providerResolutionPolicyFake)(nil)
