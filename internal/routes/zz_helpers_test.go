package routes

// Helpers of retired unit tests that the remaining boundary tests of this package still use.

import (
	"encoding/base64"
	"bytes"
	"context"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"encoding/json"
	"os"
	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"crypto/sha256"
	"strings"
	"testing"
	"time"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

const clientDiscoveryTestInstanceID = "11111111-2222-4333-8444-555555555555"

// handleMCP serves one request through the SDK-backed MCP handler without a
// router-registered stack operations handler.
func (h inventoryHandlers) handleMCP(e *httpx.Event) error {
	return h.handleMCPWithStackOperations(e, nil)
}

func (h inventoryHandlers) handleMCPWithStackOperations(e *httpx.Event, stackOperations httpx.HandlerFunc) error {
	setMCPTestHeaders(e.Request)
	routes := func(method, template string) (httpx.HandlerFunc, string, bool) {
		if stackOperations == nil || method != http.MethodGet || template != inventoryMCPStackOperationsPath {
			return nil, "", false
		}
		return stackOperations, inventoryMCPStackOperationsPath, true
	}
	return newInventoryMCPHandler(h, routes)(e)
}

func setMCPTestHeaders(request *http.Request) {
	if request.Header.Get("Content-Type") == "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if request.Header.Get("Accept") == "" {
		request.Header.Set("Accept", "application/json, text/event-stream")
	}
}

func registryRouteStoreTestEvent(method, target, ownerID, tenantID string, body map[string]any) (*httpx.Event, *httptest.ResponseRecorder) {
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(payload))
	if ownerID != "" || tenantID != "" {
		req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{UserID: ownerID, OrgID: tenantID}))
	}
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

type fakeManagedRuntimeLeaseLister struct {
	leases    []vmlease.Lease
	inventory []vmleases.LeaseInventoryRecord
}

func (f fakeManagedRuntimeLeaseLister) ListByTenant(_ context.Context, _ string) ([]vmlease.Lease, error) {
	out := make([]vmlease.Lease, len(f.leases))
	copy(out, f.leases)
	return out, nil
}

func (f fakeManagedRuntimeLeaseLister) ListInventoryByTenant(_ context.Context, _ string) ([]vmleases.LeaseInventoryRecord, error) {
	if f.inventory != nil {
		return append([]vmleases.LeaseInventoryRecord(nil), f.inventory...), nil
	}
	records := make([]vmleases.LeaseInventoryRecord, 0, len(f.leases))
	for _, lease := range f.leases {
		record := nativeActiveManagedRuntimeRecord(lease)
		if lease.CancelledAt != nil || lease.DesiredState != vmlease.DesiredStateRunning {
			record.AuthorityState = vmleases.LeaseAuthorityStateNativeInactive
		}
		records = append(records, record)
	}
	return records, nil
}

func nativeActiveManagedRuntimeRecord(lease vmlease.Lease) vmleases.LeaseInventoryRecord {
	return vmleases.LeaseInventoryRecord{
		Lease:              lease,
		ExecutionAuthority: vmleases.LeaseExecutionAuthorityTechStackProviderControl,
		AuthorityState:     vmleases.LeaseAuthorityStateNativeActive,
	}
}

func createStackOperationsTestLease(id, tenantID, ownerID, stackID, enrollment string) vmlease.Lease {
	now := time.Now().UTC()
	return vmlease.Lease{
		ID:      vmlease.LeaseID(id),
		Subject: vmlease.Subject{Kind: vmlease.SubjectUser, ID: ownerID, OrgID: tenantID},
		Resource: vmlease.ResourceRef{
			ProviderID: "centron-managed",
			Region:     "de-fra",
		},
		DesiredState:   vmlease.DesiredStateRunning,
		BillingMode:    vmlease.BillingModeSubscription,
		LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy:  vmlease.RestartPolicyOnUnexpectedStop,
		RecreatePolicy: vmlease.RecreatePolicyManual,
		ValidFrom:      now.Add(-time.Minute),
		ValidUntil:     now.Add(30 * 24 * time.Hour),
		RenewedAt:      now,
		Metadata: map[string]string{
			"stack_id":                  stackID,
			"stack_name":                "Ops Stack",
			"runtime_lane":              serverruntime.RuntimeLaneMonthly,
			"server_mode":               serverruntime.RuntimeLaneMonthly,
			"runtime_offering_id":       string(serverruntime.RuntimeOfferingStandard),
			"runtime_enrollment_status": enrollment,
			"lease_provider":            "centron-managed",
			"public_ip":                 "203.0.113.10",
			"runtime_cpu_percent":       "12.5",
			"runtime_memory_percent":    "34.5",
			"runtime_disk_percent":      "56.5",
			"runtime_uptime_seconds":    "789",
		},
	}
}

type fakeWizardRemoteSSHTester struct {
	lastHost     string
	lastPort     int
	lastUser     string
	lastPassword string
	lastKey      string
	err          error
}

func (f *fakeWizardRemoteSSHTester) TestSSH(_ context.Context, host string, port int, username, password string) error {
	f.lastHost = host
	f.lastPort = port
	f.lastUser = username
	f.lastPassword = password
	return f.err
}

func (f *fakeWizardRemoteSSHTester) TestSSHWithKey(_ context.Context, host string, port int, username, privateKey string) error {
	f.lastHost = host
	f.lastPort = port
	f.lastUser = username
	f.lastKey = privateKey
	return f.err
}

type fakeWizardFeatureChecker struct {
	enabled bool
}

func (f fakeWizardFeatureChecker) IsEnabled(context.Context, string, string) (bool, error) {
	return f.enabled, nil
}

func wizardRemoteSSHTestEvent(body string) (*httpx.Event, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/wizard/remote/test-ssh", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{
		UserID: "owner-1",
		OrgID:  "tenant-1",
	}))
	event := &httpx.Event{
		Request:  req,
		Response: rec,
		Auth:     &httpx.Principal{Id: "owner-1"},
	}
	return event, rec
}

func newAgentBinaryTestHandler(t *testing.T, body, arch string) (workerRouteHandlers, *controlplane.MemoryStore) {
	t.Helper()
	path := t.TempDir() + "/techstack"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	artifact, err := agentBinaryArtifactForPath(path, "linux", arch)
	if err != nil {
		t.Fatalf("agentBinaryArtifactForPath: %v", err)
	}
	digest := sha256.Sum256([]byte(agentBinaryTestCredential()))
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertPairingToken(t.Context(), controlplane.PairingToken{
		ID:             "pairing-1",
		TenantID:       "tenant-1",
		OwnerSubjectID: "owner-1",
		TokenHash:      hex.EncodeToString(digest[:]),
		Status:         "active",
		ExpiresAt:      timePointer(time.Now().UTC().Add(15 * time.Minute)),
	}); err != nil {
		t.Fatalf("UpsertPairingToken: %v", err)
	}
	return workerRouteHandlers{
		wst: store,
		binaryArtifact: func() (agentBinaryArtifact, error) {
			return artifact, nil
		},
		binaryDownloadGuard: newAgentBinaryDownloadGuard(),
	}, store
}

func agentBinaryTestRouter(handler workerRouteHandlers) *httpx.Router {
	router := httpx.NewRouter()
	router.POST("/api/v1/agent/binary/{os}/{arch}", handler.agentBinary)
	return router
}

func performAgentBinaryTestRequest(t *testing.T, router *httpx.Router, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, nil)
	req.Header.Set("Authorization", "Bearer "+agentBinaryTestCredential())
	req.Header.Set("Content-Type", "application/octet-stream")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func timePointer(value time.Time) *time.Time { return &value }

func agentBinaryTestCredential() string {
	return strings.Join([]string{
		"kpt1",
		base64.RawURLEncoding.EncodeToString([]byte("tenant-1")),
		base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
	}, ".")
}

type stepUpGate struct {
	EntitlementGate
	fresh bool
}

func (g stepUpGate) FreshMultiFactor(context.Context, time.Time, time.Duration) bool { return g.fresh }

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func workerRouteTestEvent(method, target, body string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if strings.HasPrefix(target, "/v1/ril/servers/connect") {
		req.Header.Set("Idempotency-Key", "worker-route-test-connect-v1")
	}
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

