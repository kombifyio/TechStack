package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/guardposture"
)

type posturePolicyGate struct {
	EntitlementGate
	entitled, fresh bool
}

func (g posturePolicyGate) SignedGrant(context.Context, string) bool { return g.entitled }
func (g posturePolicyGate) FreshMultiFactor(context.Context, time.Time, time.Duration) bool {
	return g.fresh
}

type gatedDispatcher struct {
	release chan struct{}
	mu      sync.Mutex
	targets []guardposture.RepairTarget
}

func (d *gatedDispatcher) Repair(_ context.Context, target guardposture.RepairTarget) (guardposture.RepairResult, error) {
	d.mu.Lock()
	d.targets = append(d.targets, target)
	d.mu.Unlock()
	<-d.release
	return guardposture.RepairResult{Outcome: "applied", Steps: []guardposture.RepairStep{{Control: target.Controls[0], Status: "applied"}}}, nil
}

func postureEvidenceBody(t *testing.T, now time.Time, seq int64, states map[string]guardposture.State) []byte {
	t.Helper()
	evidence := guardposture.Evidence{
		SchemaVersion: guardposture.EvidenceSchema, Mode: "advanced", SiteKind: "home", BaselineVersion: "stackkit.host-security-baseline/1.0.0",
		ObservedAt: now, ExpiresAt: now.Add(45 * time.Minute), FreshnessSecs: 2700,
	}
	for _, id := range []string{
		guardposture.ControlFirewall, guardposture.ControlSSHPassword, guardposture.ControlSSHRootLogin, guardposture.ControlSSHPort,
		guardposture.ControlBruteForce, guardposture.ControlUnattended, guardposture.ControlSysctl, guardposture.ControlPendingSecurity, guardposture.ControlRebootRequired,
	} {
		state := guardposture.StateCompliant
		if s, ok := states[id]; ok {
			state = s
		}
		evidence.Controls = append(evidence.Controls, guardposture.Control{
			ID: id, State: state, Expected: "e", Observed: "o", ObservedAt: now, Remediation: guardposture.Remediation{Capability: guardposture.RemediationAutomatic},
		})
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(guardposture.Report{
		SchemaVersion: guardposture.ReportSchema, SourceEpoch: "e1", Sequence: seq, ObservedAt: now, GuardVersion: "v1",
		Security: &guardposture.SecurityReport{Status: guardposture.ObservationObserved, Evidence: raw},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func postureTestServer(t *testing.T, now time.Time) (*controlplane.MemoryStore, *guardposture.Service, *gatedDispatcher) {
	t.Helper()
	store := controlplane.NewMemoryStore()
	heartbeat := now.Add(-10 * time.Second)
	for _, server := range []controlplane.ServerRuntime{
		{ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", WorkerID: "worker-1", Name: "Node", LifecycleState: "active", ConnectionState: "connected", HealthState: "healthy", LastHeartbeatAt: &heartbeat},
		{ID: "server-nowr", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", Name: "No Guard", LifecycleState: "active", ConnectionState: "connected", HealthState: "healthy"},
	} {
		if _, err := store.UpsertServerRuntime(t.Context(), server); err != nil {
			t.Fatal(err)
		}
	}
	dispatcher := &gatedDispatcher{release: make(chan struct{})}
	service, err := guardposture.NewService(guardposture.ServiceConfig{
		Store: guardposture.NewMemoryStore(nil), Dispatcher: dispatcher, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		select {
		case <-dispatcher.release:
		default:
			close(dispatcher.release)
		}
		service.Close()
	})
	return store, service, dispatcher
}

var postureIdentity = guardposture.Identity{TenantID: "tenant-1", WorkerID: "worker-1", ServerID: "server-1", OwnerSubjectID: "owner-1"}

// A repair changes the host's firewall, ssh or update configuration, so it needs
// the server's owner, the maintenance entitlement and a fresh second factor, and
// it names a control the fresh evidence reports drifted. The bounds answer with
// structured guidance: one repair at a time, then a cooldown with a retry time.
func TestSecurityRepairNeedsTheOwnerEntitlementStepUpAndADriftedControl(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	store, service, dispatcher := postureTestServer(t, now)
	if _, err := service.Ingest(t.Context(), postureIdentity, "node", postureEvidenceBody(t, now, 1, map[string]guardposture.State{guardposture.ControlFirewall: guardposture.StateDrifted})); err != nil {
		t.Fatal(err)
	}
	call := func(owner string, gate posturePolicyGate, body map[string]any) (int, map[string]any, http.Header) {
		h := serverRuntimeHandlers{store: store, now: func() time.Time { return now }, policy: NewSelfHostedInventoryPolicy(), posture: service, postureEntitlements: gate}
		event, recorder := registryRouteStoreTestEvent(http.MethodPost, "/api/v1/servers/server-1/security/repairs", owner, "tenant-1", body)
		event.Request.SetPathValue("serverId", "server-1")
		_ = h.postureRequestRepair(event)
		var envelope map[string]any
		_ = json.Unmarshal(recorder.Body.Bytes(), &envelope)
		return recorder.Code, envelope, recorder.Header()
	}
	entitled := posturePolicyGate{entitled: true, fresh: true}
	repair := map[string]any{"controls": []string{guardposture.ControlFirewall}}

	if code, _, _ := call("owner-2", entitled, repair); code != http.StatusNotFound {
		t.Fatalf("another owner got %d", code)
	}
	if code, _, _ := call("owner-1", posturePolicyGate{entitled: false, fresh: true}, repair); code != http.StatusForbidden {
		t.Fatalf("without the entitlement got %d", code)
	}
	if code, body, _ := call("owner-1", posturePolicyGate{entitled: true, fresh: false}, repair); code != http.StatusForbidden || !strings.Contains(toJSON(body), postureReasonStepUp) {
		t.Fatalf("without a fresh second factor got %d %v", code, body)
	}
	if code, _, _ := call("owner-1", entitled, map[string]any{"controls": []string{guardposture.ControlFirewall}, "actor": "someone-else"}); code != http.StatusBadRequest {
		t.Fatalf("an unknown field got %d", code)
	}
	if code, body, _ := call("owner-1", entitled, map[string]any{"controls": []string{guardposture.ControlSysctl}}); code != http.StatusConflict || !strings.Contains(toJSON(body), guardposture.DenyNotDrifted) {
		t.Fatalf("a compliant control got %d %v", code, body)
	}
	dispatcher.mu.Lock()
	sent := len(dispatcher.targets)
	dispatcher.mu.Unlock()
	if sent != 0 {
		t.Fatalf("a refused request dispatched %d repairs", sent)
	}

	if code, body, _ := call("owner-1", entitled, repair); code != http.StatusAccepted {
		t.Fatalf("an admitted repair got %d %v", code, body)
	}
	if code, body, _ := call("owner-1", entitled, repair); code != http.StatusConflict || !strings.Contains(toJSON(body), guardposture.DenyInProgress) {
		t.Fatalf("a second repair while one runs got %d %v", code, body)
	}
	waitUntil(t, func() bool {
		dispatcher.mu.Lock()
		defer dispatcher.mu.Unlock()
		return len(dispatcher.targets) == 1
	})
	dispatcher.mu.Lock()
	target := dispatcher.targets[0]
	dispatcher.mu.Unlock()
	if target.TenantID != "tenant-1" || target.AgentID != "worker-1" || len(target.Controls) != 1 || target.Controls[0] != guardposture.ControlFirewall {
		t.Fatalf("the Guard was sent %+v", target)
	}
	close(dispatcher.release)
	waitUntil(t, func() bool {
		repairs, _ := service.Repairs(t.Context(), "tenant-1", "worker-1", 10)
		return len(repairs) == 1 && repairs[0].Status == guardposture.RepairSucceeded && repairs[0].RequestedBy == "owner-1"
	})
	code, body, header := call("owner-1", entitled, repair)
	if code != http.StatusTooManyRequests || !strings.Contains(toJSON(body), guardposture.DenyCooldown) || header.Get("Retry-After") == "" {
		t.Fatalf("a repeat inside the cooldown got %d %v (Retry-After %q)", code, body, header.Get("Retry-After"))
	}
}

func toJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

func waitUntil(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The server read model carries the persisted posture, never the heartbeat: a
// connected, healthy server whose Guard has reported nothing is unknown, fresh
// evidence makes it compliant, and another tenant reads none of it.
func TestServerReadModelCarriesPersistedPostureNotTheHeartbeat(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	store, service, _ := postureTestServer(t, now)
	list := func(owner, tenant string) []serverRuntimeResponse {
		t.Helper()
		h := serverRuntimeHandlers{store: store, now: func() time.Time { return now }, posture: service}
		event, recorder := registryRouteStoreTestEvent(http.MethodGet, "/api/v1/servers", owner, tenant, nil)
		if err := h.list(event); err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Data []serverRuntimeResponse `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data
	}
	find := func(items []serverRuntimeResponse, id string) serverRuntimeResponse {
		for _, item := range items {
			if item.ID == id {
				return item
			}
		}
		t.Fatalf("server %s not listed", id)
		return serverRuntimeResponse{}
	}

	item := find(list("owner-1", "tenant-1"), "server-1")
	if item.Connection.State != "connected" || item.Security == nil || item.Security.State != guardposture.SecurityUnknown || item.Security.ReasonCode != guardposture.ReasonNotObserved {
		t.Fatalf("a connected Guard that never reported: %+v %+v", item.Connection, item.Security)
	}
	if item.Recovery == nil || item.Recovery.Backup.State != guardposture.RecoveryUnknown || item.Updates == nil {
		t.Fatalf("recovery and updates are unknown, not absent: %+v %+v", item.Recovery, item.Updates)
	}
	if noGuard := find(list("owner-1", "tenant-1"), "server-nowr"); noGuard.Security != nil || noGuard.Recovery != nil || noGuard.Updates != nil {
		t.Fatalf("a server without a Guard carries posture: %+v", noGuard)
	}

	if _, err := service.Ingest(t.Context(), postureIdentity, "node", postureEvidenceBody(t, now, 1, nil)); err != nil {
		t.Fatal(err)
	}
	if item := find(list("owner-1", "tenant-1"), "server-1"); item.Security == nil || item.Security.State != guardposture.SecurityCompliant || item.Security.BaselineVersion == "" {
		t.Fatalf("fresh evidence: %+v", item.Security)
	}
	if other := list("owner-2", "tenant-2"); len(other) != 0 {
		t.Fatalf("another tenant listed %d servers", len(other))
	}
}
