package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"google.golang.org/protobuf/proto"
)

func TestNormalizeStackKitServiceLifecycleRequest(t *testing.T) {
	req, err := NormalizeStackKitLifecycleRequest(StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1",
		Operation: StackKitLifecycleServiceLogs, ServiceKey: "auth",
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.LogTail != 100 {
		t.Fatalf("default log tail = %d, want 100", req.LogTail)
	}
	if _, err := NormalizeStackKitLifecycleRequest(StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1",
		Operation: StackKitLifecycleServiceStop, ServiceKey: "auth",
	}); err == nil {
		t.Fatal("expected mutation without Owner approval to fail")
	}
}

// Intentional Stop is verified by the exact controller's post-action
// observation, while Start/Restart still require healthy verification.
func TestServiceStopAdmitsScopedStoppedEffectAndProjectsIntent(t *testing.T) {
	for _, scenario := range []string{"stopped", "observation-churn", "wrong-service", "still-running", "wrong-command", "wrong-release", "missing-observation", "bad-evidence", "changed-intent", "intent-aba", "changed-inventory", "foreign-runtime", "locked-group", "start", "unhealthy-start", "unhealthy-restart"} {
		t.Run(scenario, func(t *testing.T) {
			store := controlplane.NewMemoryStore()
			for _, row := range []controlplane.ServiceRuntime{
				{ID: "control-1", ServiceKey: "vault"},
				{ID: "component-1", ServiceKey: "vaultwarden", Metadata: map[string]any{"application_key": "vault", "runtime_identity": map[string]any{"container_name": "stackkit-vault-cloud-main-vaultwarden-1"}}},
				{ID: "other-server", ServiceKey: "vault", ServerID: "server-2"},
			} {
				row.TenantID, row.StackID, row.Source, row.ManagementState = "tenant-1", "stack-1", "stackkits-inventory", "managed"
				if row.ServerID == "" {
					row.ServerID = "server-1"
				}
				row.DesiredState, row.ObservedState, row.HealthState = "running", "running", "healthy"
				if row.Metadata == nil {
					row.Metadata = map[string]any{}
				}
				row.Metadata["inventory_revision"] = 1
				row.Metadata["application_intent_revision"] = 1
				if scenario == "start" || scenario == "unhealthy-start" {
					row.DesiredState = "stopped"
				}
				if _, err := store.UpsertServiceRuntime(t.Context(), row); err != nil {
					t.Fatal(err)
				}
			}
			operation := StackKitLifecycleServiceStop
			if scenario == "start" || scenario == "unhealthy-start" {
				operation = StackKitLifecycleServiceStart
			}
			if scenario == "unhealthy-restart" {
				operation = StackKitLifecycleServiceRestart
			}
			release := releaseWithAdvancedCatalog(t)
			sender := &serviceEffectSender{t: t, scenario: scenario, store: store}
			handler := StackKitLifecycleHandler(StackKitLifecycleConfig{Sender: sender, Services: store, releaseResolver: func() (*stackkitrelease.Release, error) { return &release, nil }, ServiceRuntimeAuthorize: func(_ context.Context, req StackKitLifecycleRequest, serverID string) error {
				if req.AgentID != "agent-1" || req.StackKitInstanceID != "home-stack" || serverID != "server-1" || scenario == "foreign-runtime" {
					return fmt.Errorf("runtime worker binding changed")
				}
				return nil
			}})
			req := StackKitLifecycleRequest{StackID: "stack-1", StackKitInstanceID: "home-stack", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", Operation: operation, OwnerApproved: true, ServiceID: "component-1", ServiceKey: "vault", StackKit: "cloud-kit", SpecPath: "bound.json"}
			req.DurableJobID, req.ServiceActionDigest = StackKitServiceActionJobID(req.TenantID, req.OwnerID, "service-effect-1"), strings.Repeat("a", 64)
			job := &Job{ID: req.DurableJobID, TargetID: req.StackID, Payload: StackKitLifecyclePayload(req)}
			err := handler(t.Context(), job, NewQueue(0, nil))
			wantSuccess := scenario == "stopped" || scenario == "observation-churn" || scenario == "start"
			if (err == nil) != wantSuccess {
				t.Fatalf("service effect acceptance: %v", err)
			}
			if err != nil && IsRetryable(err) {
				t.Fatal("failed service effect could replay its host mutation")
			}
			for _, id := range []string{"control-1", "component-1", "other-server"} {
				row, loadErr := store.GetServiceRuntime(t.Context(), "tenant-1", id)
				if loadErr != nil {
					t.Fatal(loadErr)
				}
				want := "running"
				if (scenario == "stopped" || scenario == "observation-churn") && id != "other-server" {
					want = "stopped"
				}
				if scenario == "unhealthy-start" || (scenario == "start" && id == "other-server") {
					want = "stopped"
				}
				if scenario == "changed-intent" && id == "component-1" {
					want = "stopped"
				}
				if row.DesiredState != want || row.ObservedState != "running" || row.HealthState != "healthy" {
					t.Fatalf("service %s authority changed incorrectly: %+v", id, row)
				}
			}
		})
	}
}

type serviceEffectSender struct {
	t        *testing.T
	scenario string
	store    *controlplane.MemoryStore
}

func (s *serviceEffectSender) SendStackKitCommand(ctx context.Context, agent string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	if agent != "agent-1" || command.StackkitInstanceId != "home-stack" || command.SpecPath != "bound.json" {
		s.t.Fatal("service dispatch escaped the admitted instance")
	}
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_VERIFY {
		if s.scenario != "start" && s.scenario != "unhealthy-start" && s.scenario != "unhealthy-restart" {
			s.t.Fatal("intentional Stop dispatched a whole-kit health verifier")
		}
		ok := s.scenario == "start"
		exit := int32(1)
		if ok {
			exit = 0
		}
		return &agentpb.StackKitResult{Success: ok, ExitCode: exit, CommandResultJson: []byte(`{}`)}, nil
	}
	state, action := "stopped", "stop"
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_START {
		state, action = "running", "start"
	}
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_RESTART {
		state, action = "running", "restart"
	}
	evidence := map[string]any{"apiVersion": "stackkit.service-action-result/v1", "action": action, "serviceKey": "vault", "desiredState": state, "observedState": state, "revision": 1, "evidenceRef": "stackkit-evidence://service-control/sha256:" + strings.Repeat("b", 64), "checks": []any{map[string]any{"name": "owner_approval", "status": "ok"}, map[string]any{"name": "runtime_action", "status": "ok"}, map[string]any{"name": "runtime_observation", "status": "ok"}}}
	if s.scenario == "wrong-service" {
		evidence["serviceKey"] = "other"
	}
	if s.scenario == "still-running" {
		evidence["observedState"] = "running"
	}
	if s.scenario == "missing-observation" {
		evidence["checks"] = []any{}
	}
	if s.scenario == "bad-evidence" {
		evidence["evidenceRef"] = "unbound"
	}
	if s.scenario == "changed-intent" || s.scenario == "locked-group" {
		patch := controlplane.ServiceRuntime{DesiredState: "stopped"}
		if s.scenario == "locked-group" {
			patch = controlplane.ServiceRuntime{MutationLock: controlplane.ServiceMutationLock{State: "locked"}}
		}
		if _, err := s.store.ApplyServiceEvent(ctx, controlplane.ServiceEvent{TenantID: "tenant-1", ServiceID: "component-1", Authority: controlplane.ServiceEventAuthorityControlPlane, Runtime: patch}); err != nil {
			s.t.Fatal(err)
		}
	}
	if s.scenario == "intent-aba" {
		for _, state := range []string{"stopped", "running"} {
			if _, err := s.store.ApplyServiceEvent(ctx, controlplane.ServiceEvent{TenantID: "tenant-1", ServiceID: "control-1", Authority: controlplane.ServiceEventAuthorityControlPlane, Runtime: controlplane.ServiceRuntime{DesiredState: state}}); err != nil {
				s.t.Fatal(err)
			}
		}
	}
	if s.scenario == "changed-inventory" {
		row, _ := s.store.GetServiceRuntime(ctx, "tenant-1", "component-1")
		row.Metadata["inventory_revision"] = 2
		if _, err := s.store.UpsertServiceRuntime(ctx, *row); err != nil {
			s.t.Fatal(err)
		}
	}
	if s.scenario == "observation-churn" || s.scenario == "intent-aba" {
		for _, id := range []string{"control-1", "component-1"} {
			row, _ := s.store.GetServiceRuntime(ctx, "tenant-1", id)
			row.Metadata["inventory_revision"] = 2
			// Guard cannot forge or clear the stored control intent marker.
			row.Metadata["application_intent_revision"] = 999
			row.DesiredState = ""
			row.Placement = serviceregistry.Placement{}
			row.MutationLock = controlplane.ServiceMutationLock{}
			if _, err := s.store.ApplyServiceEvent(ctx, controlplane.ServiceEvent{TenantID: "tenant-1", ServiceID: id, Authority: controlplane.ServiceEventAuthorityGuard, Runtime: *row}); err != nil {
				s.t.Fatal(err)
			}
		}
	}
	output, _ := json.Marshal(evidence)
	envelope, _ := json.Marshal(map[string]any{"schemaVersion": stackkitcommand.CommandResultVersion, "command": stackkitcommand.ResultCommandName(command.Operation), "status": "success", "data": map[string]any{"output": string(output)}})
	r := &agentpb.StackKitResult{CommandId: command.CommandId, Success: true, CommandResultJson: envelope, CommandResultSchemaVersion: stackkitcommand.CommandResultVersion, EventsSchemaVersion: stackkitcommand.RolloutEventVersion, Release: proto.Clone(command.Release).(*agentpb.StackKitReleasePin)}
	if s.scenario == "wrong-command" {
		r.CommandId = "different-command"
	}
	if s.scenario == "wrong-release" {
		r.Release.ArchiveSha256 = strings.Repeat("f", 64)
	}
	return r, nil
}

func TestStackKitServiceActionDurableIdentityAndLogProjection(t *testing.T) {
	digest := strings.Repeat("a", 64)
	req, err := NormalizeStackKitLifecycleRequest(StackKitLifecycleRequest{
		StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1",
		Operation: StackKitLifecycleServiceLogs, ServiceKey: "auth", ServiceID: "service-1",
		DurableJobID: StackKitServiceActionJobID("tenant-1", "owner-1", "logs-1"), ServiceActionDigest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.DurableJobID != StackKitServiceActionJobID("tenant-1", "owner-1", "logs-1") || strings.Contains(req.DurableJobID, "logs-1") {
		t.Fatalf("durable job ID leaked or drifted: %q", req.DurableJobID)
	}
	page, _ := json.Marshal(map[string]any{
		"apiVersion": "stackkit.service-logs/v1", "serviceKey": "auth",
		"entries": []any{map[string]any{"timestamp": "2026-08-10T12:00:00Z", "message": "ready"}},
	})
	envelope, _ := json.Marshal(map[string]any{
		"schemaVersion": "stackkit.command-result/v1", "command": "service_logs", "status": "success",
		"data": map[string]any{"output": string(page)},
	})
	result, err := normalizeStackKitLifecycleResult(req, &agentpb.StackKitResult{Success: true, CommandResultJson: envelope})
	if err != nil {
		t.Fatal(err)
	}
	if !MatchesStackKitServiceActionReceipt(result, req) {
		t.Fatalf("durable receipt missing: %#v", result)
	}
	logs, _ := result["service_logs"].(map[string]any)
	if stringFromInterface(logs["serviceKey"]) != "auth" {
		t.Fatalf("service logs missing: %#v", result)
	}
}

func TestNormalizeStackKitRemoveRejectsMissingTerminalEvidence(t *testing.T) {
	req := StackKitLifecycleRequest{Operation: StackKitLifecycleRemove, WorkloadRef: "photos"}
	envelope := []byte(`{"schemaVersion":"stackkit.command-result/v1","command":"stackkit remove","status":"success","data":{}}`)
	if _, err := normalizeStackKitLifecycleResult(req, &agentpb.StackKitResult{Success: true, CommandResultJson: envelope}); err == nil {
		t.Fatal("normalizeStackKitLifecycleResult() accepted remove without terminal evidence")
	}
}
