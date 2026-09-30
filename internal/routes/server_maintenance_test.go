package routes

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	commonedgeauth "github.com/kombifyio/techstack/internal/gocommon/edgeauth"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/middleware"
	"github.com/kombifyio/techstack/pkg/servermaintenance"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

const maintenanceTestPlanDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

var maintenanceTestControlPlane = servermaintenance.MachineIDDigest("control-plane-machine")

type inertMaintenanceDispatcher struct{}

func (inertMaintenanceDispatcher) Plan(context.Context, servermaintenance.Target) (servermaintenance.PlanResult, error) {
	return servermaintenance.PlanResult{}, servermaintenance.ErrDispatchUnavailable
}

func (inertMaintenanceDispatcher) Apply(context.Context, servermaintenance.Target, string) (servermaintenance.ApplyResult, error) {
	return servermaintenance.ApplyResult{}, servermaintenance.ErrDispatchUnavailable
}

func (inertMaintenanceDispatcher) Reboot(context.Context, servermaintenance.Target, time.Duration) (servermaintenance.RebootResult, error) {
	return servermaintenance.RebootResult{}, servermaintenance.ErrDispatchUnavailable
}

type maintenanceFixture struct {
	t     *testing.T
	store *controlplane.MemoryStore
	now   time.Time
	h     serverRuntimeHandlers
}

// newMaintenanceFixture is one active, connected BYO server whose agent
// advertises host maintenance, behind the hosted (SaaS) entitlement gate.
func newMaintenanceFixture(t *testing.T, dispatcher servermaintenance.Dispatcher) *maintenanceFixture {
	t.Helper()
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	f := &maintenanceFixture{t: t, store: store, now: now, h: maintenanceTestHandlers(store, now, dispatcher)}
	f.setServer("owner-1", 7, nil)
	return f
}

func maintenanceTestHandlers(store *controlplane.MemoryStore, now time.Time, dispatcher servermaintenance.Dispatcher) serverRuntimeHandlers {
	return serverRuntimeHandlers{
		store: store, policy: NewSelfHostedInventoryPolicy(), now: func() time.Time { return now },
		maintenance: newServerMaintenanceHandlers(&ServerMaintenanceRouteConfig{
			Jobs: store, Services: store, Events: store,
			Entitlements: EntitlementGateForDeployment(true), Dispatcher: dispatcher,
			AgentCapabilities:           func(controlplane.ServerRuntime) []string { return []string{servermaintenance.AgentCapability} },
			ControlPlaneMachineIDDigest: maintenanceTestControlPlane,
		}),
	}
}

func (f *maintenanceFixture) setServer(ownerID string, revision int64, metadata map[string]any) {
	f.t.Helper()
	heartbeat := f.now.Add(-10 * time.Second)
	if metadata == nil {
		metadata = map[string]any{}
	}
	if _, reported := metadata[servermaintenance.MetadataBootID]; !reported {
		metadata[servermaintenance.MetadataBootID] = "0f5d2c1e-7a3b-4c2d-9e8f-1a2b3c4d5e6f"
	}
	if _, err := f.store.UpsertServerRuntime(f.t.Context(), controlplane.ServerRuntime{
		ID: "server-1", TenantID: "tenant-1", OwnerSubjectID: ownerID, WorkerID: "agent-1",
		LifecycleState: string(serverregistry.LifecycleActive), DesiredState: string(serverregistry.DesiredRunning),
		ConnectionState: string(serverregistry.ConnectionConnected), HealthState: string(serverregistry.HealthHealthy),
		InventoryRevision: revision, LastHeartbeatAt: &heartbeat, Metadata: metadata,
	}); err != nil {
		f.t.Fatal(err)
	}
}

// maintenanceCaller is the verified edge context of a request: where its
// techstack.servers.maintenance grant comes from ("signed", "membership" or
// none) and how old its multi-factor sign-in is (0: no step-up claims). It
// holds no inventory entitlement: ownership and the tier grant suffice.
type maintenanceCaller struct {
	grant   string
	authAge time.Duration
}

var fullyAuthorizedCaller = maintenanceCaller{grant: "signed", authAge: time.Minute}

// post sends one maintenance request and returns the status, the accepted
// receipt and the denial reason.
func (f *maintenanceFixture) post(caller maintenanceCaller, key string, body map[string]any) (int, serverMaintenanceResponse, string) {
	f.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		f.t.Fatal(err)
	}
	return f.postRaw(caller, key, string(raw))
}

func (f *maintenanceFixture) postRaw(caller maintenanceCaller, key, raw string) (int, serverMaintenanceResponse, string) {
	f.t.Helper()
	ctx := identity.NewContext(context.Background(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"})
	switch caller.grant {
	case "signed":
		ctx = middleware.WithSignedEntitlements(ctx, ServerMaintenanceEntitlement)
	case "membership":
		ctx = middleware.WithMembershipEntitlements(ctx, ServerMaintenanceEntitlement)
	}
	if caller.authAge > 0 {
		ctx = middleware.WithVerifiedStepUp(ctx, middleware.StepUpClaims{
			AMR: []string{"pwd", "mfa"}, AuthTime: f.now.Add(-caller.authAge), PrincipalType: middleware.PrincipalTypeUser,
		})
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/servers/server-1/actions", strings.NewReader(raw)).WithContext(ctx)
	request.SetPathValue("serverId", "server-1")
	request.Header.Set("Idempotency-Key", key)
	recorder := httptest.NewRecorder()
	if err := f.h.maintenanceAction(&httpx.Event{Request: request, Response: recorder}); err != nil && !errors.Is(err, httpx.ErrResponseWritten) {
		f.t.Fatalf("maintenance action: %v", err)
	}
	var payload struct {
		Data       serverMaintenanceResponse `json:"data"`
		ReasonCode string                    `json:"reason_code"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
	return recorder.Code, payload.Data, payload.ReasonCode
}

const maintenanceEdgeSecret = "maintenance-edge-secret"

// postThroughEdge sends one maintenance request as the Gateway would: a signed
// identity envelope of the given version carrying the signed maintenance
// entitlement and principal type and, when authAge > 0, step-up headers for
// an MFA sign-in that old. Only the edge identity middleware turns them into request context.
func (f *maintenanceFixture) postThroughEdge(version, principalType string, authAge time.Duration, key string, body map[string]any) (int, string) {
	f.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		f.t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/servers/server-1/actions", strings.NewReader(string(raw)))
	for name, value := range map[string]string{
		commonedgeauth.HeaderEdgeAuth:      commonedgeauth.EdgeAuthValueJWT,
		commonedgeauth.HeaderEdgeService:   "techstack",
		commonedgeauth.HeaderPublicPrefix:  "/v1/techstack",
		commonedgeauth.HeaderUserID:        "owner-1",
		commonedgeauth.HeaderOrgID:         "tenant-1",
		commonedgeauth.HeaderEntitlements:  ServerMaintenanceEntitlement,
		commonedgeauth.HeaderPrincipalType: principalType,
	} {
		if value != "" {
			request.Header.Set(name, value)
		}
	}
	if authAge > 0 {
		request.Header.Set(commonedgeauth.HeaderUserAMR, "pwd,mfa")
		request.Header.Set(commonedgeauth.HeaderUserAuthTime, strconv.FormatInt(f.now.Add(-authAge).Unix(), 10))
	}
	signMaintenanceEdgeEnvelope(request, version)
	request.Header.Set("Idempotency-Key", key)

	e := &httpx.Event{Request: request, Response: httptest.NewRecorder()}
	edge := middleware.EdgeIdentityMiddlewareWithConfig(middleware.EdgeIdentityConfig{
		Mode: config.ModeSaaS, EdgeAuthSecret: maintenanceEdgeSecret,
	})
	if err := edge(e); err != nil {
		f.t.Fatalf("edge identity middleware: %v", err)
	}
	recorder := e.Response.(*httptest.ResponseRecorder)
	if recorder.Code != http.StatusOK || recorder.Body.Len() > 0 {
		f.t.Fatalf("edge identity middleware refused the envelope: %d %s", recorder.Code, recorder.Body.String())
	}
	e.Request.SetPathValue("serverId", "server-1")
	if err := f.h.maintenanceAction(e); err != nil && !errors.Is(err, httpx.ErrResponseWritten) {
		f.t.Fatalf("maintenance action: %v", err)
	}
	var payload struct {
		ReasonCode string `json:"reason_code"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
	return recorder.Code, payload.ReasonCode
}

// signMaintenanceEdgeEnvelope signs the request the way the Gateway signer
// does for v2 and v7 (field order: kombify-go-common/edgeauth).
func signMaintenanceEdgeEnvelope(r *http.Request, version string) {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	header := func(name string) string { return strings.TrimSpace(r.Header.Get(name)) }
	fields := []string{version, "primary", r.Method, r.URL.RequestURI(),
		header(commonedgeauth.HeaderEdgeAuth), header(commonedgeauth.HeaderEdgeService), header(commonedgeauth.HeaderPublicPrefix)}
	if version == commonedgeauth.EdgeSignatureVersionV7 {
		fields = append(fields, header(commonedgeauth.HeaderUserIssuer))
	}
	for _, name := range []string{
		commonedgeauth.HeaderUserID, commonedgeauth.HeaderOrgID, commonedgeauth.HeaderUserEmail,
		commonedgeauth.HeaderUserTier, commonedgeauth.HeaderUserRoles, commonedgeauth.HeaderUserScope,
		commonedgeauth.HeaderEntitlements, commonedgeauth.HeaderKnowledgeTier,
	} {
		fields = append(fields, header(name))
	}
	if version == commonedgeauth.EdgeSignatureVersionV7 {
		for _, name := range []string{
			commonedgeauth.HeaderClientID, commonedgeauth.HeaderProductID, commonedgeauth.HeaderAIWorkload,
			commonedgeauth.HeaderResourceScope, commonedgeauth.HeaderPrincipalType, commonedgeauth.HeaderAgentID,
			commonedgeauth.HeaderAgentClass, commonedgeauth.HeaderAgentPerimeter, commonedgeauth.HeaderAgentPolicy,
			commonedgeauth.HeaderUserAMR, commonedgeauth.HeaderUserACR, commonedgeauth.HeaderUserAuthTime,
		} {
			fields = append(fields, header(name))
		}
	}
	fields = append(fields, timestamp, "nonce-maintenance")
	mac := hmac.New(sha256.New, []byte(maintenanceEdgeSecret))
	_, _ = mac.Write([]byte(strings.Join(fields, "\n")))
	r.Header.Set(commonedgeauth.HeaderEdgeKeyID, "primary")
	r.Header.Set(commonedgeauth.HeaderEdgeTimestamp, timestamp)
	r.Header.Set(commonedgeauth.HeaderEdgeNonce, "nonce-maintenance")
	r.Header.Set(commonedgeauth.HeaderEdgeSignedPath, r.URL.RequestURI())
	r.Header.Set(commonedgeauth.HeaderEdgeSignature, version+"="+base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))
}

func (f *maintenanceFixture) jobExists(key string) bool {
	_, err := f.store.GetServerMaintenanceJob(f.t.Context(), "tenant-1", serverMaintenanceJobID("tenant-1", "owner-1", key))
	return err == nil
}

func reboot(revision int64) map[string]any {
	return map[string]any{"action": "reboot", "expected_inventory_revision": revision, "owner_approved": true}
}

// Only the Edge-signed grant counts: the membership fallback is not a
// per-request Gateway decision and never admits a reboot.
func TestServerMaintenanceWithoutSignedEntitlementCreatesNoJob(t *testing.T) {
	for _, grant := range []string{"", "membership"} {
		t.Run("grant="+grant, func(t *testing.T) {
			f := newMaintenanceFixture(t, inertMaintenanceDispatcher{})
			status, _, reason := f.post(maintenanceCaller{grant: grant, authAge: time.Minute}, "reboot-1", reboot(7))
			if status != http.StatusForbidden || reason != maintenanceReasonEntitlement || f.jobExists("reboot-1") {
				t.Fatalf("status=%d reason=%q job=%v, want 403 entitlement_required and no job", status, reason, f.jobExists("reboot-1"))
			}
		})
	}
}

func TestServerMaintenanceHidesAnotherOwnersServer(t *testing.T) {
	f := newMaintenanceFixture(t, inertMaintenanceDispatcher{})
	f.setServer("owner-2", 7, nil)
	if status, _, _ := f.post(fullyAuthorizedCaller, "reboot-1", reboot(7)); status != http.StatusNotFound || f.jobExists("reboot-1") {
		t.Fatalf("status=%d, want 404 and no job", status)
	}
}

// A reboot needs a multi-factor sign-in at most 300 seconds old by a human
// user, bound by a verified v7 edge envelope. The request runs through the
// real edge identity middleware: stale or missing claims, claims in a v2
// envelope that does not sign them, or claims of a principal not signed as
// "user" are refused and create no job.
func TestServerMaintenanceStepUpFollowsVerifiedEdgeEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name          string
		version       string
		principalType string
		authAge       time.Duration
		wantStatus    int
	}{
		{name: "fresh v7 claims", version: "v7", principalType: "user", authAge: time.Minute, wantStatus: http.StatusAccepted},
		{name: "stale v7 claims", version: "v7", principalType: "user", authAge: 301 * time.Second, wantStatus: http.StatusForbidden},
		{name: "v7 without claims", version: "v7", principalType: "user", wantStatus: http.StatusForbidden},
		{name: "fresh claims under v2", version: "v2", principalType: "user", authAge: time.Minute, wantStatus: http.StatusForbidden},
		{name: "fresh v7 claims of a machine", version: "v7", principalType: "machine", authAge: time.Minute, wantStatus: http.StatusForbidden},
		{name: "fresh v7 claims without principal type", version: "v7", authAge: time.Minute, wantStatus: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMaintenanceFixture(t, inertMaintenanceDispatcher{})
			status, reason := f.postThroughEdge(tc.version, tc.principalType, tc.authAge, "reboot-1", reboot(7))
			if status != tc.wantStatus || f.jobExists("reboot-1") != (tc.wantStatus == http.StatusAccepted) {
				t.Fatalf("status=%d reason=%q job=%v, want %d", status, reason, f.jobExists("reboot-1"), tc.wantStatus)
			}
			if tc.wantStatus == http.StatusForbidden && reason != maintenanceReasonStepUpRequired {
				t.Fatalf("reason=%q, want %s", reason, maintenanceReasonStepUpRequired)
			}
		})
	}
}

// The Gateway exempts os_update_plan from step-up by reading `action`. A body
// that could mean something else to Go (a case-variant duplicate key, or an
// extra field) must be refused.
func TestServerMaintenanceRefusesAmbiguousBodies(t *testing.T) {
	for name, raw := range map[string]string{
		"case variant key": `{"action":"os_update_plan","ACTION":"reboot","expected_inventory_revision":7,"owner_approved":true}`,
		"duplicate key":    `{"action":"os_update_plan","action":"reboot","expected_inventory_revision":7,"owner_approved":true}`,
		"unknown field":    `{"action":"os_update_plan","reboot":true,"expected_inventory_revision":7}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newMaintenanceFixture(t, inertMaintenanceDispatcher{})
			if status, _, _ := f.postRaw(maintenanceCaller{grant: "signed"}, "plan-1", raw); status != http.StatusBadRequest || f.jobExists("plan-1") {
				t.Fatalf("status=%d, want 400 and no job", status)
			}
		})
	}
}

// A client that lost the response retries with the same key: the stored
// receipt comes back even after the reboot moved the inventory revision. A
// different body under the same key is a conflict.
func TestServerMaintenanceReplaysReceiptAfterRevisionMoved(t *testing.T) {
	f := newMaintenanceFixture(t, inertMaintenanceDispatcher{})
	status, first, _ := f.post(fullyAuthorizedCaller, "reboot-1", reboot(7))
	if status != http.StatusAccepted || first.JobID == "" || first.Status != "queued" {
		t.Fatalf("first status=%d receipt=%+v", status, first)
	}
	f.setServer("owner-1", 8, nil)
	if status, replay, _ := f.post(fullyAuthorizedCaller, "reboot-1", reboot(7)); status != http.StatusAccepted || replay.JobID != first.JobID {
		t.Fatalf("replay status=%d receipt=%+v, want the first receipt", status, replay)
	}
	if status, _, reason := f.post(fullyAuthorizedCaller, "reboot-1", reboot(8)); status != http.StatusConflict || reason != maintenanceReasonIdempotencyReused {
		t.Fatalf("different body status=%d reason=%q, want 409 %s", status, reason, maintenanceReasonIdempotencyReused)
	}
}

func TestServerMaintenanceRefusesSubstrateAndControlPlaneHost(t *testing.T) {
	for _, tc := range []struct {
		metadata map[string]any
		reason   string
	}{
		{metadata: map[string]any{"server_node_role": "substrate"}, reason: maintenanceReasonSubstrate},
		{metadata: map[string]any{servermaintenance.MetadataMachineIDDigest: maintenanceTestControlPlane}, reason: maintenanceReasonControlPlaneHost},
		// Without a reported boot id a reboot could never be confirmed.
		{metadata: map[string]any{servermaintenance.MetadataBootID: ""}, reason: maintenanceReasonBootIDUnknown},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			f := newMaintenanceFixture(t, inertMaintenanceDispatcher{})
			f.setServer("owner-1", 7, tc.metadata)
			status, _, reason := f.post(fullyAuthorizedCaller, "reboot-1", reboot(7))
			if status != http.StatusConflict || reason != tc.reason || f.jobExists("reboot-1") {
				t.Fatalf("status=%d reason=%q, want 409 %s and no job", status, reason, tc.reason)
			}
			server, _ := f.store.GetServerRuntime(t.Context(), "tenant-1", "server-1")
			if slices.Contains(f.h.response(*server).AllowedActions, controlplane.ServerMaintenanceActionReboot) {
				t.Fatalf("a refused node still advertises reboot")
			}
		})
	}
}

// Until the agent protocol wires a Dispatcher the feature is inert: nothing is
// advertised and every request is refused before a job exists.
func TestServerMaintenanceShipsInertWithoutDispatcher(t *testing.T) {
	wired := newMaintenanceFixture(t, inertMaintenanceDispatcher{})
	server, _ := wired.store.GetServerRuntime(t.Context(), "tenant-1", "server-1")
	if !slices.Contains(wired.h.response(*server).AllowedActions, controlplane.ServerMaintenanceActionReboot) {
		t.Fatalf("an eligible node with a wired dispatcher does not advertise reboot")
	}
	inert := newMaintenanceFixture(t, nil)
	if slices.Contains(inert.h.response(*server).AllowedActions, controlplane.ServerMaintenanceActionReboot) {
		t.Fatalf("reboot advertised without a dispatcher")
	}
	status, _, reason := inert.post(fullyAuthorizedCaller, "reboot-1", reboot(7))
	if status != http.StatusConflict || reason != maintenanceReasonCapabilityMissing || inert.jobExists("reboot-1") {
		t.Fatalf("status=%d reason=%q, want 409 %s and no job", status, reason, maintenanceReasonCapabilityMissing)
	}
}

func TestServerMaintenanceLockedServiceVetoes(t *testing.T) {
	f := newMaintenanceFixture(t, inertMaintenanceDispatcher{})
	if _, err := f.store.UpsertServiceRuntime(t.Context(), controlplane.ServiceRuntime{
		ID: "service-1", TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1", ServiceKey: "auth",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SetServiceMutationLock(t.Context(), "tenant-1", "service-1", controlplane.ServiceMutationLock{
		State: serviceregistry.MutationLocked, ReasonCode: "owner_locked", Actor: "owner-1",
	}, "owner_locked"); err != nil {
		t.Fatal(err)
	}
	status, _, reason := f.post(fullyAuthorizedCaller, "reboot-1", reboot(7))
	if status != http.StatusConflict || reason != maintenanceReasonServiceLocked || f.jobExists("reboot-1") {
		t.Fatalf("status=%d reason=%q, want 409 service_locked and no job", status, reason)
	}
}

// One reboot or update holds the node; a second one is refused. Update plans
// hold a separate slot, so a running plan cannot starve a reboot.
func TestServerMaintenanceFenceRefusesConcurrentMaintenance(t *testing.T) {
	f := newMaintenanceFixture(t, inertMaintenanceDispatcher{})
	plan := map[string]any{"action": "os_update_plan", "expected_inventory_revision": 7}
	if status, _, reason := f.post(fullyAuthorizedCaller, "plan-1", plan); status != http.StatusAccepted {
		t.Fatalf("plan status=%d reason=%q", status, reason)
	}
	if status, _, reason := f.post(fullyAuthorizedCaller, "reboot-1", reboot(7)); status != http.StatusAccepted {
		t.Fatalf("reboot next to a running plan status=%d reason=%q", status, reason)
	}
	status, _, reason := f.post(fullyAuthorizedCaller, "reboot-2", reboot(7))
	if status != http.StatusConflict || reason != maintenanceReasonActive || f.jobExists("reboot-2") {
		t.Fatalf("second reboot status=%d reason=%q, want 409 maintenance_active and no job", status, reason)
	}
}

func TestServerMaintenanceUpdateRefusesStalePlan(t *testing.T) {
	f := newMaintenanceFixture(t, inertMaintenanceDispatcher{})
	update := map[string]any{"action": "os_update", "expected_inventory_revision": 7, "owner_approved": true, "expected_plan_digest": maintenanceTestPlanDigest}
	completePlan := func(key string, completedAt time.Time) {
		t.Helper()
		id := serverMaintenanceJobID("tenant-1", "owner-1", key)
		if _, err := f.store.CreateServerMaintenanceJob(t.Context(), controlplane.ServerMaintenanceJob{
			ID: id, TenantID: "tenant-1", ServerID: "server-1", AgentID: "agent-1", OwnerSubjectID: "owner-1",
			Action: controlplane.ServerMaintenanceActionPlan, RequestDigest: "digest-" + key, State: controlplane.ServerMaintenanceStateQueued,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.UpdateServerMaintenanceJob(t.Context(), "tenant-1", id, controlplane.ServerMaintenanceUpdate{
			ExpectedState: controlplane.ServerMaintenanceStateQueued, State: controlplane.ServerMaintenanceStateCompleted,
			PlanDigest: maintenanceTestPlanDigest, At: completedAt,
		}); err != nil {
			t.Fatal(err)
		}
	}
	completePlan("old-plan", f.now.Add(-16*time.Minute))
	if status, _, reason := f.post(fullyAuthorizedCaller, "update-1", update); status != http.StatusConflict || reason != maintenanceReasonPlanStale {
		t.Fatalf("update with a 16 minute old plan status=%d reason=%q, want 409 plan_stale", status, reason)
	}
	completePlan("fresh-plan", f.now.Add(-time.Minute))
	if status, _, reason := f.post(fullyAuthorizedCaller, "update-2", update); status != http.StatusAccepted {
		t.Fatalf("update with a fresh plan status=%d reason=%q", status, reason)
	}
}

// An owner confirms against the inventory revision they were shown. Guard
// inventory posts that change nothing material (every 30 seconds, with fresh
// resource usage and a new source position) must not invalidate it.
func TestServerMaintenanceApprovalSurvivesANoOpInventoryPost(t *testing.T) {
	store := controlplane.NewMemoryStore()
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	guard := workerRouteHandlers{serverStore: store}
	worker := controlplane.Worker{ID: "agent-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1"}
	if err := guard.projectServerEnrollment(t.Context(), worker, "server-1", "", now.Add(-time.Hour), "pairing-redemption"); err != nil {
		t.Fatal(err)
	}
	post := func(sequence int64, at time.Time, cpu float64) {
		t.Helper()
		req := workerInventoryRequest{
			SourceEpoch: "epoch-a", SourceSequence: sequence, ObservedAt: at, Hostname: "runtime-1",
			Host:     workerInventoryHost{Hostname: "runtime-1", OS: "ubuntu", CPUPercent: cpu, UptimeSeconds: float64(sequence) * 30},
			Services: []workerInventoryService{{ServiceID: "db", Status: "healthy"}},
		}
		req.HostBootID, req.HostMaintenance = "0f5d2c1e-7a3b-4c2d-9e8f-1a2b3c4d5e6f", true
		if _, err := guard.projectServerInventory(t.Context(), worker, "server-1", "", req, at); err != nil {
			t.Fatal(err)
		}
	}
	post(1, now.Add(-time.Minute), 12)
	shown, err := store.GetServerRuntime(t.Context(), "tenant-1", "server-1")
	if err != nil {
		t.Fatal(err)
	}
	post(2, now.Add(-30*time.Second), 57)
	f := &maintenanceFixture{t: t, store: store, now: now, h: maintenanceTestHandlers(store, now, inertMaintenanceDispatcher{})}
	if status, _, reason := f.post(fullyAuthorizedCaller, "reboot-1", reboot(shown.InventoryRevision)); status != http.StatusAccepted {
		t.Fatalf("approval of revision %d after a no-op post: status=%d reason=%q", shown.InventoryRevision, status, reason)
	}
}
