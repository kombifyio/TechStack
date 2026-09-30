package stacks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/edgeauth"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	jobruntime "github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/middleware"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/orchestrator"
)

type deniedQualificationLeaseManager struct {
	mu             sync.Mutex
	policy         monthlyruntime.CapacityPolicyResolver
	policyChecks   int
	providerWrites int
}

func (m *deniedQualificationLeaseManager) CreateOrBindLease(
	ctx context.Context,
	req jobruntime.ManagedLeaseRequest,
) (*jobruntime.ManagedLeaseResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.policyChecks++
	if _, err := m.policy.ResolveCapacity(ctx, monthlyruntime.CapacityPolicyRequest{
		TenantID: req.TenantID, OwnerSubjectID: req.OwnerID, ProviderID: req.Provider,
	}); err != nil {
		return nil, err
	}
	m.providerWrites++
	return nil, fmt.Errorf("provider write fixture must not be reached")
}

func TestOperatorQualificationDeniedSignedManualProvisionNeverReachesProviderWrite(t *testing.T) {
	const (
		stackID = "stack-operator-qualification-denied"
		ownerID = "owner-1"
		tenant  = "tenant-1"
	)
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(t.Context(), controlplane.CreateStackRequest{
		ID: stackID, TenantID: tenant, OwnerSubjectID: ownerID, Name: "Denied qualification",
		Status: "draft", Config: map[string]any{"user_config": map[string]any{
			"name": "denied-qualification", "mode": "easy", "stackkit": "cloud-kit", "kit": "cloud-kit",
			"provider_id": monthlyruntime.ProviderIONOS, "provider_region": "de-fra",
			"server_provisioning_mode": "kombify-cloud", "server_mode": "monthly-runtime",
			"runtime_lane": "monthly-runtime", "runtime_offering_id": "monthly-runtime-standard",
			"services": []any{"photos", "files", "vault"},
		}},
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
	manager := &deniedQualificationLeaseManager{policy: policy}
	orch := orchestrator.New(&orchestrator.Config{
		Workers: 1, WorkDir: t.TempDir(), StackStore: store, JobStore: store,
		RuntimeActions: jobruntime.RuntimeActions{LeaseManager: manager},
	}, nil)
	orch.Start()
	defer orch.Stop()

	request := httptest.NewRequest(http.MethodPost, "/api/v1/stacks/"+stackID+"/provision", nil)
	request.SetPathValue("id", stackID)
	request.Header.Set("Idempotency-Key", "standalone-cloud-denied-attempt")
	signDeniedQualificationRequest(t, request, ownerID, tenant)
	recorder := httptest.NewRecorder()
	event := &httpx.Event{Request: request, Response: recorder}
	mw := middleware.EdgeIdentityMiddlewareWithConfig(middleware.EdgeIdentityConfig{
		Mode: config.ModeSaaS, EdgeAuthSecret: "qualification-edge-secret",
		EdgeFlagsSecret: "qualification-flags-secret", EdgeFlagsKeyID: "flags-primary",
		EdgeSignatureWindow: 5 * time.Minute,
	})
	if err := mw(event); err != nil {
		t.Fatalf("edge middleware: %v", err)
	}
	if err := (crudRouteHandlers{orch: orch, stackStore: store, jobStore: store}).provisionStack(event); err != nil {
		t.Fatalf("provisionStack: %v", err)
	}
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s, want durable asynchronous acceptance", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data struct {
			JobID string `json:"job_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Data.JobID == "" {
		t.Fatalf("decode accepted job: err=%v body=%s", err, recorder.Body.String())
	}

	deadline := time.Now().Add(5 * time.Second)
	var durable *controlplane.Job
	for time.Now().Before(deadline) {
		durable, err = store.GetJob(t.Context(), tenant, response.Data.JobID)
		if err == nil && durable.State == "failed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	jobs, listErr := store.ListJobsByStack(t.Context(), tenant, stackID, 10)
	if listErr != nil {
		t.Fatalf("ListJobsByStack: %v", listErr)
	}
	manager.mu.Lock()
	policyChecks, providerWrites := manager.policyChecks, manager.providerWrites
	manager.mu.Unlock()
	if durable == nil || durable.State != "failed" || policyChecks != 1 || providerWrites != 0 || len(jobs) != 1 {
		t.Fatalf("job=%+v checks=%d providerWrites=%d jobs=%d, want one failed denial record and no provider write",
			durable, policyChecks, providerWrites, len(jobs))
	}
}

func signDeniedQualificationRequest(t *testing.T, request *http.Request, ownerID, tenantID string) {
	t.Helper()
	request.Header.Set(edgeauth.HeaderEdgeAuth, edgeauth.EdgeAuthValueJWT)
	request.Header.Set(edgeauth.HeaderEdgeService, "techstack")
	request.Header.Set(edgeauth.HeaderPublicPrefix, "/v1/techstack")
	request.Header.Set(edgeauth.HeaderUserID, ownerID)
	request.Header.Set(edgeauth.HeaderOrgID, tenantID)
	request.Header.Set(edgeauth.HeaderUserEmail, "qualification-owner@example.test")
	request.Header.Set(edgeauth.HeaderUserRoles, "owner")
	request.Header.Set(edgeauth.HeaderRequestID, "request-qualification-denied")
	request.Header.Set(edgeauth.HeaderEntitlements, strings.Join([]string{
		monthlyruntime.FeatureTechStackManagedRuntime,
		monthlyruntime.FeatureTechStackManagedRuntimeCloudKit,
		monthlyruntime.FeatureTechStackManagedRuntimeIONOS,
	}, ","))

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "qualification-edge-nonce"
	signedPath := request.URL.RequestURI()
	payload := strings.Join([]string{
		"v2", "primary", request.Method, signedPath,
		request.Header.Get(edgeauth.HeaderEdgeAuth), request.Header.Get(edgeauth.HeaderEdgeService),
		request.Header.Get(edgeauth.HeaderPublicPrefix), request.Header.Get(edgeauth.HeaderUserID),
		request.Header.Get(edgeauth.HeaderOrgID), request.Header.Get(edgeauth.HeaderUserEmail),
		request.Header.Get(edgeauth.HeaderUserTier), request.Header.Get(edgeauth.HeaderUserRoles),
		request.Header.Get(edgeauth.HeaderUserScope), request.Header.Get(edgeauth.HeaderEntitlements),
		request.Header.Get(edgeauth.HeaderKnowledgeTier), timestamp, nonce,
	}, "\n")
	mac := hmac.New(sha256.New, []byte("qualification-edge-secret"))
	_, _ = mac.Write([]byte(payload))
	request.Header.Set(edgeauth.HeaderEdgeTimestamp, timestamp)
	request.Header.Set(edgeauth.HeaderEdgeNonce, nonce)
	request.Header.Set(edgeauth.HeaderEdgeSignedPath, signedPath)
	request.Header.Set(edgeauth.HeaderEdgeKeyID, "primary")
	request.Header.Set(edgeauth.HeaderEdgeSignature, "v2="+base64.RawURLEncoding.EncodeToString(mac.Sum(nil)))

	headers, err := edgeauth.SignDecisionHeaders(edgeauth.DecisionSignInput{
		Secret: "qualification-flags-secret", KeyID: "flags-primary",
		Method: request.Method, SignedPath: signedPath, Audience: "techstack", PublicPrefix: "/v1/techstack",
		SubjectID: ownerID, TenantID: tenantID, RequestID: request.Header.Get(edgeauth.HeaderRequestID),
		EdgeKeyID: request.Header.Get(edgeauth.HeaderEdgeKeyID), EdgeTimestamp: timestamp, EdgeNonce: nonce,
		EdgeSignature: request.Header.Get(edgeauth.HeaderEdgeSignature),
		Flags: map[string]bool{
			monthlyruntime.FeatureTechStackManagedRuntime:         false,
			monthlyruntime.FeatureTechStackManagedRuntimeCloudKit: false,
			monthlyruntime.FeatureTechStackManagedRuntimeIONOS:    false,
		},
		Budgets: map[string]any{edgeauth.CloudRuntimeCreditsBudgetName: map[string]any{
			"managed_servers": map[string]any{"mode": "limited", "limit": 0},
		}},
	})
	if err != nil {
		t.Fatalf("SignDecisionHeaders: %v", err)
	}
	for name, values := range headers {
		for _, value := range values {
			request.Header.Set(name, value)
		}
	}
}
