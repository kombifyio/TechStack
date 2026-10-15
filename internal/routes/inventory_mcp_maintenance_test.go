package routes

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	commonedgeauth "github.com/kombifyio/techstack/internal/gocommon/edgeauth"
	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/guardreach"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/middleware"
)

// The public SDK/route journey admits one durable job only with both verified
// commercial identity and current delegated authority. A lost response or
// revoked grant must never turn a transport retry into another admission.
func TestInventoryMCPMaintenanceCurrentAuthorityControlsDurableAdmission(t *testing.T) {
	for _, scenario := range []string{"os_update_plan", "os_update", "reboot", "optional actor", "request id fallback", "malformed signed id", "missing commercial", "missing handler", "owner mismatch", "tenant mismatch", "target mismatch", "resource mismatch", "action mismatch", "expired", "malformed result", "lost reserve ack", "revoked before replay", "revoked before admission", "legacy agent", "partial markers", "marked direct leaf", "unrelated tool", "discovery"} {
		t.Run(scenario, func(t *testing.T) {
			f := newMaintenanceFixture(t, inertMaintenanceDispatcher{})
			f.now = time.Now().UTC()
			f.h.now = func() time.Time { return f.now }
			f.setServer("owner-1", 7, nil)
			var mu sync.Mutex
			usedNonces, usedJTIs := map[string]bool{}, map[string]bool{}
			checks := 0
			revoked := false
			broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				raw, err := io.ReadAll(io.LimitReader(r.Body, (128<<10)+1))
				var request struct {
					SchemaVersion string                            `json:"schemaVersion"`
					Operation     string                            `json:"operation"`
					Origin        guardreach.MaintenanceOriginProof `json:"origin"`
				}
				claims, authErr := servicecall.VerifyToken(r.Header.Get(servicecall.HeaderServiceAuth), "maintenance-service-secret", "")
				if err != nil || len(raw) > 128<<10 || json.Unmarshal(raw, &request) != nil || authErr != nil || claims.OnBehalfOf == nil ||
					claims.OnBehalfOf.Sub != "owner-1" || claims.OnBehalfOf.OrgID != "tenant-1" || claims.Aud != "kombify-gateway" || claims.Svc != "techstack" ||
					claims.Scope != "mcp-maintenance:"+request.Operation || claims.Exp-claims.Iat > 45 || claims.RequestBinding == nil {
					http.Error(w, "denied", http.StatusUnauthorized)
					return
				}
				binding, _ := servicecall.NewRequestBinding(r.Method, r.URL.RequestURI(), raw)
				parts := strings.Split(r.Header.Get(servicecall.HeaderServiceAuth), ".")
				payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
				var token struct {
					JTI string `json:"jti"`
				}
				_ = json.Unmarshal(payload, &token)
				if *claims.RequestBinding != binding || token.JTI == "" || usedJTIs[token.JTI] || r.URL.Path != "/v1/internal/mcp/maintenance-authority" || request.SchemaVersion != "kombify.mcp-maintenance-origin-check.v1" {
					http.Error(w, "denied", http.StatusForbidden)
					return
				}
				usedJTIs[token.JTI] = true
				contextBytes, _ := base64.RawURLEncoding.DecodeString(request.Origin.Context)
				var origin struct {
					Authority struct {
						Resource struct {
							ID string `json:"id"`
						} `json:"resource"`
						Action string `json:"action"`
					} `json:"delegated_authority"`
					Transport struct {
						ExpiresAt int64 `json:"exp"`
					} `json:"transport"`
				}
				_ = json.Unmarshal(contextBytes, &origin)
				if origin.Authority.Resource.ID != "server-1" || origin.Authority.Action != actionForMaintenanceScenario(scenario) {
					http.Error(w, "target denied", http.StatusForbidden)
					return
				}
				if request.Operation == "reserve" {
					if usedNonces[request.Origin.Nonce] {
						http.Error(w, "replayed", http.StatusConflict)
						return
					}
					usedNonces[request.Origin.Nonce] = true
					if scenario == "lost reserve ack" {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
				} else if request.Operation == "check" {
					checks++
					if revoked || (scenario == "revoked before replay" && checks >= 2) || (scenario == "revoked before admission" && checks >= 3) {
						http.Error(w, "revoked", http.StatusForbidden)
						return
					}
				} else {
					http.Error(w, "operation", http.StatusBadRequest)
					return
				}
				digest := request.Origin.Digest()
				if scenario == "malformed result" {
					digest = "sha256:" + strings.Repeat("0", 64)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"schemaVersion": "kombify.mcp-maintenance-origin-check-result.v1", "operation": request.Operation, "allowed": true, "requestDigest": digest, "expiresAt": origin.Transport.ExpiresAt})
			}))
			defer broker.Close()
			gateway, err := guardreach.NewGateway(guardreach.GatewayConfig{BaseURL: broker.URL, Secret: "maintenance-service-secret"})
			if err != nil {
				t.Fatal(err)
			}
			f.h.maintenance.originAuthority = gateway
			router := httpx.NewRouter()
			router.BindFunc(middleware.EdgeIdentityMiddlewareWithConfig(middleware.EdgeIdentityConfig{Mode: config.ModeSaaS, EdgeAuthSecret: maintenanceEdgeSecret}))
			router.POST("/api/v1/servers/{serverId}/actions", f.h.maintenanceAction)
			maintenanceAction := f.h.maintenanceAction
			if scenario == "missing handler" {
				maintenanceAction = nil
			}
			RegisterInventoryRoutes(router, InventoryRouteConfig{ReadStore: f.store, Policy: NewSelfHostedInventoryPolicy(), Now: f.h.now,
				MaintenanceAction: maintenanceAction,
				MaintenanceOrigin: MaintenanceOriginConfig{Keys: map[string]string{"primary": maintenanceEdgeSecret}, Authority: gateway, Entitlements: EntitlementGateForDeployment(true)}})
			action := actionForMaintenanceScenario(scenario)
			arguments := map[string]any{"server_id": "server-1", "action": action, "expected_inventory_revision": 7}
			if action != controlplane.ServerMaintenanceActionPlan {
				arguments["expected_impact_digest"] = f.impactDigest(action)
			}
			if action == "os_update" {
				arguments["expected_plan_digest"] = maintenanceTestPlanDigest
				_, err = f.store.CreateServerMaintenanceJob(t.Context(), controlplane.ServerMaintenanceJob{ID: "completed-plan", TenantID: "tenant-1", ServerID: "server-1", AgentID: "agent-1", OwnerSubjectID: "owner-1", Action: "os_update_plan", RequestDigest: "completed", State: controlplane.ServerMaintenanceStateQueued})
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.store.UpdateServerMaintenanceJob(t.Context(), "tenant-1", "completed-plan", controlplane.ServerMaintenanceUpdate{ExpectedState: controlplane.ServerMaintenanceStateQueued, State: controlplane.ServerMaintenanceStateCompleted, PlanDigest: maintenanceTestPlanDigest, At: f.now})
				if err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "target mismatch" {
				arguments["server_id"] = "server-foreign"
			}
			body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": maintenanceToolName, "arguments": arguments}})
			if scenario == "unrelated tool" {
				body, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "server_health", "arguments": map[string]any{"server_id": "server-1"}}})
			} else if scenario == "discovery" {
				body, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
			} else if scenario == "marked direct leaf" {
				body, _ = json.Marshal(map[string]any{"action": action, "expected_inventory_revision": 7})
			}
			owner, tenant := "owner-1", "tenant-1"
			if scenario == "owner mismatch" {
				owner = "owner-foreign"
			}
			if scenario == "tenant mismatch" {
				tenant = "tenant-foreign"
			}
			expires := time.Now().Add(45 * time.Second).Unix()
			if scenario == "expired" {
				expires = time.Now().Add(-time.Second).Unix()
			}
			version := 2
			if scenario == "legacy agent" {
				version = 1
			}
			call := func(nonce string) *httptest.ResponseRecorder {
				request := httptest.NewRequest(http.MethodPost, "/api/v1/mcp", bytes.NewReader(body))
				if scenario == "marked direct leaf" {
					request = httptest.NewRequest(http.MethodPost, "/api/v1/servers/server-1/actions", bytes.NewReader(body))
				}
				setMCPTestHeaders(request)
				request.Header.Set("Idempotency-Key", "unsigned-substitution")
				request.Header.Set("X-Request-ID", "signed-request")
				for name, value := range map[string]string{commonedgeauth.HeaderEdgeAuth: commonedgeauth.EdgeAuthValueJWT, commonedgeauth.HeaderEdgeService: "techstack", commonedgeauth.HeaderPublicPrefix: "/v1/techstack", commonedgeauth.HeaderUserID: "owner-1", commonedgeauth.HeaderOrgID: "tenant-1", commonedgeauth.HeaderPrincipalType: "user"} {
					request.Header.Set(name, value)
				}
				if scenario != "missing commercial" {
					request.Header.Set(commonedgeauth.HeaderEntitlements, ServerMaintenanceEntitlement)
				}
				signMaintenanceEdgeEnvelope(request, "v2")
				signMaintenanceMCPOrigin(t, request, body, arguments, owner, tenant, nonce, expires, version, scenario)
				if scenario == "partial markers" {
					request.Header.Del("X-Kombify-Mcp-Origin-Signature")
				}
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				return recorder
			}
			first := call("nonce-first")
			jobKey := "signed-maintenance-key"
			if scenario == "request id fallback" {
				jobKey = "signed-request"
			}
			admitted := f.jobExists(jobKey)
			valid := ordinaryMaintenanceAction(scenario) || scenario == "optional actor" || scenario == "request id fallback"
			if admitted != valid || f.jobExists("unsigned-substitution") {
				t.Fatalf("durable admission=%v, expected=%v, status=%d", admitted, valid, first.Code)
			}
			if scenario == "unrelated tool" || scenario == "discovery" {
				var ordinary struct {
					Error  json.RawMessage `json:"error"`
					Result struct {
						IsError           bool `json:"isError"`
						StructuredContent struct {
							ServerID string `json:"server_id"`
						} `json:"structuredContent"`
						CacheScope string `json:"cacheScope"`
					} `json:"result"`
				}
				if json.Unmarshal(first.Body.Bytes(), &ordinary) != nil || first.Code != http.StatusOK || len(ordinary.Error) != 0 || ordinary.Result.IsError {
					t.Fatalf("ordinary authenticated MCP operation rejected: HTTP %d", first.Code)
				}
				if scenario == "unrelated tool" && ordinary.Result.StructuredContent.ServerID != "server-1" {
					t.Fatal("unrelated tool did not return the owned server health")
				}
				if scenario == "discovery" && ordinary.Result.CacheScope != inventoryMCPPrivateCacheScope {
					t.Fatal("ordinary discovery lost its authenticated cache scope")
				}
				if checks != 0 || len(usedNonces) != 0 {
					t.Fatal("ordinary tool acquired maintenance authority or consumed a maintenance nonce")
				}
			}
			if valid {
				var result struct {
					Result struct {
						IsError bool `json:"isError"`
					} `json:"result"`
				}
				if json.Unmarshal(first.Body.Bytes(), &result) != nil || first.Code != http.StatusOK || result.Result.IsError {
					t.Fatalf("valid maintenance rejected: HTTP %d", first.Code)
				}
				original, _ := f.store.GetServerMaintenanceJob(t.Context(), "tenant-1", serverMaintenanceJobID("tenant-1", "owner-1", jobKey))
				f.setServer("owner-1", 8, nil)
				replay := call("nonce-fresh-retry")
				if json.Unmarshal(replay.Body.Bytes(), &result) != nil || result.Result.IsError || replay.Code != http.StatusOK {
					t.Fatalf("fresh transport durable replay rejected: HTTP %d", replay.Code)
				}
				if repeated := call("nonce-first"); repeated.Code != http.StatusUnauthorized {
					t.Fatalf("spent transport admitted: %d", repeated.Code)
				}
				mu.Lock()
				revoked = true
				mu.Unlock()
				deniedReplay := call("nonce-revoked-retry")
				result.Result.IsError = false
				_ = json.Unmarshal(deniedReplay.Body.Bytes(), &result)
				if deniedReplay.Code == http.StatusOK && !result.Result.IsError {
					t.Fatal("withdrawn authority returned a stored receipt")
				}
				retained, _ := f.store.GetServerMaintenanceJob(t.Context(), "tenant-1", original.ID)
				if retained == nil || retained.CreatedAt != original.CreatedAt || retained.InventoryRevision != original.InventoryRevision || retained.Action != original.Action {
					t.Fatal("retry changed durable admitted job")
				}
			}
		})
	}
}

func actionForMaintenanceScenario(scenario string) string {
	if ordinaryMaintenanceAction(scenario) {
		return scenario
	}
	return "reboot"
}

func signMaintenanceMCPOrigin(t *testing.T, r *http.Request, body []byte, arguments map[string]any, owner, tenant, nonce string, expires int64, version int, scenario string) {
	t.Helper()
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	issuedAt, _ := strconv.ParseInt(timestamp, 10, 64)
	args, _ := json.Marshal(arguments)
	bodySum, argsSum := sha256.Sum256(body), sha256.Sum256(args)
	bodyDigest := base64.RawURLEncoding.EncodeToString(bodySum[:])
	envelope := map[string]any{"v": version, "iss": "kombify-gateway", "aud": "mcp-use:techstack", "sub": owner, "org_id": tenant,
		"actor_sub": "agent-actor", "agent_class": "pilot", "agent_instance_id": "instance:maintenance-test", "principal_type": "agent",
		"request_id": "signed-request", "server_key": "techstack", "tool_name": maintenanceToolName, "operation_ids": []string{"techstack." + maintenanceOperationID},
		"arguments_sha256": base64.RawURLEncoding.EncodeToString(argsSum[:]), "idempotency_key": "signed-maintenance-key",
		"transport": map[string]any{"method": r.Method, "path": r.URL.RequestURI(), "body_sha256": bodyDigest, "nonce": nonce, "iat": issuedAt, "exp": expires}}
	if version == 2 {
		envelope["catalog_revision"] = "sha256:" + strings.Repeat("1", 64)
		resourceID, authorityAction := "server-1", actionForMaintenanceScenario(scenario)
		if scenario == "resource mismatch" {
			resourceID = "server-foreign"
		}
		if scenario == "action mismatch" {
			authorityAction = "os_update_plan"
		}
		envelope["delegated_authority"] = map[string]any{"schemaVersion": "test-current-standing-authority", "resource": map[string]any{"id": resourceID}, "action": authorityAction}
	}
	if scenario == "optional actor" {
		delete(envelope, "actor_sub")
	}
	if scenario == "request id fallback" {
		delete(envelope, "idempotency_key")
	}
	if scenario == "malformed signed id" {
		envelope["idempotency_key"] = " invalid "
	}
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	signingVersion := fmt.Sprintf("v%d", version)
	mac := hmac.New(sha256.New, []byte(maintenanceEdgeSecret))
	_, _ = mac.Write([]byte(strings.Join([]string{signingVersion, r.Method, r.URL.RequestURI(), timestamp, nonce, bodyDigest, encoded}, "\n")))
	for name, value := range map[string]string{"X-Kombify-Mcp-Origin-Context": encoded, "X-Kombify-Mcp-Origin-Signature": signingVersion + "=" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), "X-Kombify-Mcp-Origin-Key-Id": "primary", "X-Kombify-Mcp-Origin-Timestamp": timestamp, "X-Kombify-Mcp-Origin-Nonce": nonce, "X-Kombify-Mcp-Origin-Body-Sha256": bodyDigest} {
		r.Header.Set(name, value)
	}
}
