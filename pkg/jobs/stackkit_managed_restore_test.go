package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

type renewalInventory struct {
	plan   []byte
	failed bool
}

func (*renewalInventory) Build(context.Context, ManagedStackKitInventoryRequest) ([]byte, error) {
	return nil, fmt.Errorf("drill must never replace rollout Inventory")
}
func (builder *renewalInventory) AttestBackupRenewal(_ context.Context, req ManagedStackKitInventoryRequest) ([]byte, error) {
	if builder.failed {
		return nil, fmt.Errorf("attestation denied")
	}
	builder.plan = append([]byte(nil), req.ResolvedPlan...)
	return []byte(`{"externalBackupTargetBindings":{"cloud":{"offsite-object-backup":{"bindingRef":"backup-target-binding://sha256/` + strings.Repeat("b", 64) + `","backupTargetRef":"backup-target://sha256/` + strings.Repeat("b", 64) + `","custodyAttestationRef":"backup-custody-attestation://sha256/` + strings.Repeat("c", 64) + `"}}}}`), nil
}

type renewedDrillSender struct {
	issuer       *recordingAdvancedIssuer
	staleReceipt bool
	drilled      bool
	wrongPlan    bool
	t            *testing.T
}

func (sender *renewedDrillSender) SendStackKitCommand(_ context.Context, _ string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	if len(command.InventoryJson) != 0 {
		sender.t.Fatal("renewal replaced the applied Inventory")
	}
	result := &agentpb.StackKitResult{Success: true, CommandId: command.CommandId, Release: command.Release, CommandResultSchemaVersion: stackkitcommand.CommandResultVersion}
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN:
		var envelope map[string]any
		_ = json.Unmarshal(typedPlanCommandResult(testResolvedPlanHash), &envelope)
		instance := "cloud-stack"
		if sender.wrongPlan {
			instance = "other-stack"
		}
		envelope["data"].(map[string]any)["managed_backup_plan"] = map[string]any{"apiVersion": "stackkit.resolved-plan/v1", "kind": "ResolvedPlan", "stackId": instance, "planHash": testResolvedPlanHash, "backupPolicy": map[string]any{"coverage": "config", "schedule": map[string]any{"cadence": "daily"}}, "externalBackupTargetBindings": map[string]any{"cloud": map[string]any{"offsite-object-backup": map[string]any{"bindingHash": testResolvedPlanHash, "bindingRef": "backup-target-binding://sha256/" + strings.Repeat("b", 64), "backupTargetRef": "backup-target://sha256/" + strings.Repeat("b", 64), "custodyAttestationRef": "backup-custody-attestation://sha256/" + strings.Repeat("b", 64)}}}}
		result.CommandResultJson, _ = json.Marshal(envelope)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_RESTORE_DRILL:
		if sender.drilled {
			sender.t.Fatal("one job dispatched two drills")
		}
		sender.drilled = true
		if command.CommandId != "job-drill-1" || len(command.AdvancedCapability) == 0 {
			sender.t.Fatal("drill lost its operation identity or signed capability")
		}

		drillID := "job-drill-1"
		if sender.staleReceipt {
			drillID = "rollout-1"
		}
		result.CommandResultJson, _ = json.Marshal(map[string]any{"schemaVersion": "stackkit.command-result/v1", "command": "stackkit advanced restore-drill run", "status": "success", "data": map[string]any{"schemaVersion": "stackkit.restore-drill-report/v1", "status": "succeeded", "drillId": drillID, "activated": false, "stagingRemoved": true, "anchorId": testResolvedPlanHash, "restoreResultId": testResolvedPlanHash, "capabilityId": "capability-1", "backupRenewal": sender.issuer.issued[0].BackupRenewal}})

	default:
		sender.t.Fatalf("drill dispatched forbidden generation/apply/configuration operation: %s", command.Operation)
	}
	return result, nil
}

func TestManagedDrillRenewsOnlyTheCurrentAppliedOperation(t *testing.T) {
	for _, scenario := range []string{"admitted", "other-plan", "unhealthy-target", "stale-receipt"} {
		t.Run(scenario, func(t *testing.T) {
			sender := &renewedDrillSender{t: t, wrongPlan: scenario == "other-plan"}
			builder := &renewalInventory{failed: scenario == "unhealthy-target"}
			issuer := &recordingAdvancedIssuer{}
			sender.issuer = issuer
			sender.staleReceipt = scenario == "stale-receipt"
			release := releaseWithAdvancedCatalog(t)
			req := StackKitLifecycleRequest{StackID: "deployment-1", StackKitInstanceID: "cloud-stack", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", Operation: StackKitLifecycleRestoreDrill, OwnerApproved: true, StackKit: "cloud-kit", SpecPath: "stack-spec.bound.json"}
			job := &Job{ID: "job-drill-1", TargetID: req.StackID, Payload: StackKitLifecyclePayload(req)}
			job.Payload[ManagedBackupAdmissionField] = map[string]interface{}{"tenant_id": req.TenantID, "stack_id": req.StackID, "owner_id": req.OwnerID, "agent_id": req.AgentID, "include_content": false, "quota_bytes": int64(100), "used_bytes": int64(20), "measured_at": time.Now().UTC().Format(time.RFC3339Nano)}
			err := StackKitLifecycleHandler(StackKitLifecycleConfig{Sender: sender, AdvancedIssuer: issuer, ManagedStackKitInventory: builder, releaseResolver: func() (*stackkitrelease.Release, error) { return &release, nil }})(t.Context(), job, NewQueue(0, nil))
			if scenario == "stale-receipt" {
				if err == nil {
					t.Fatal("reused rollout receipt admitted")
				}
				return
			}
			if scenario != "admitted" {
				if err == nil || sender.drilled || len(issuer.issued) != 0 {
					t.Fatal("unattested operation reached signing or drill dispatch")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !sender.drilled || len(issuer.issued) != 1 || issuer.issued[0].BackupRenewal == nil || issuer.issued[0].BackupRenewal.PlanHash != testResolvedPlanHash || issuer.issued[0].BackupRenewal.JobID != job.ID {
				t.Fatal("drill lacks exact signed operation renewal")
			}
			proof, _ := job.Result[ManagedBackupAdmissionField].(map[string]interface{})
			if proof["plan_hash"] != testResolvedPlanHash || proof["source_plan_hash"] != testResolvedPlanHash || proof["restore_scope"] != "local-staging" {
				t.Fatal("final durable result lost admission or claimed changed/activated data")
			}
		})
	}
}
