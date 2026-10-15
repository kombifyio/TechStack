package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/managedstackkit"
	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// A converged Operations binary must be adopted by a new canonical generation,
// while a different plan/release/runtime may never publish replacement Inventory.
func TestManagedGenerationAdoptsConvergedOperationsBeforeApply(t *testing.T) {
	for _, scenario := range []string{"admitted", "historical", "cache-prepared-history", "cache-denied", "cache-release-substitution", "cache-failed-plan", "cache-dry-run", "cache-offline", "cache-candidate", "unproven-history", "foreign-plan", "failed-plan", "different-release", "different-lease", "changed-runtime", "custody-denied", "missing-producer", "standalone", "old-payload"} {
		t.Run(scenario, func(t *testing.T) {
			version := "v0.51.3"
			if strings.HasPrefix(scenario, "cache-") {
				version = stackkitcommand.MinSourceGenerationCachePreparationRelease
			}
			release := releaseWithAdvancedCatalogVersion(t, version)
			actual := "sha256:" + strings.Repeat("2", 64)
			producer, err := managedstackkit.NewRolloutInventory(actual)
			if err != nil {
				t.Fatal(err)
			}
			sender := &generationInventorySender{t: t, scenario: scenario, actual: actual, admitted: "sha256:" + strings.Repeat("6", 64)}
			reads := 0
			cfg := StackKitLifecycleConfig{Sender: sender, ManagedStackKitInventory: generationInventoryProducer{producer, scenario == "custody-denied"}, releaseResolver: func() (*stackkitrelease.Release, error) { return &release, nil }, ManagedAddressAuthority: func(_ context.Context, req StackKitLifecycleRequest) (ManagedAddressRuntime, error) {
				if req.TenantID != "tenant-1" || req.OwnerID != "owner-1" || req.StackID != "deployment-1" || req.AgentID != "agent-1" {
					t.Fatal("runtime lookup lost admitted identity")
				}
				reads++
				current := ManagedAddressRuntime{LeaseID: "lease-1", ServerID: "server-1", Generation: 1, Rollout: StackKitRolloutBinding{StackKit: "cloud-kit", SpecPath: "bound.json"}}
				if scenario == "changed-runtime" && reads > 1 {
					current.Generation++
				}
				if scenario == "different-lease" {
					current.LeaseID = "other-lease"
				}
				return current, nil
			}}
			req := StackKitLifecycleRequest{StackID: "deployment-1", StackKitInstanceID: "cloud-stack", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", NodeID: "cloud-main", ManagedRuntimeLeaseID: "lease-1", ManagedRuntimeServerID: "server-1", Operation: StackKitLifecycleGenerate, StackKit: "cloud-kit", SpecPath: "bound.json"}
			if scenario == "cache-dry-run" {
				req.DryRun = true
			}
			if scenario == "cache-offline" {
				req.Offline = true
			}
			if scenario == "cache-candidate" {
				req.CandidateSpecJSON = []byte(`{"metadata":{"name":"replacement"}}`)
			}
			if scenario == "missing-producer" {
				cfg.ManagedStackKitInventory = nil
			}
			if scenario == "standalone" || scenario == "old-payload" {
				req.ManagedRuntimeLeaseID = ""
				cfg.ManagedAddressAuthority, cfg.ManagedStackKitInventory = nil, nil
			}
			handler := StackKitLifecycleHandler(cfg)
			job := &Job{ID: "job-generate-1", TargetID: req.StackID, Payload: StackKitLifecyclePayload(req)}
			if scenario == "old-payload" {
				delete(job.Payload, "managed_runtime_lease_id")
				delete(job.Payload, "managed_runtime_server_id")
			}
			err = handler(t.Context(), job, NewQueue(0, nil))
			if scenario == "standalone" || scenario == "old-payload" {
				if err != nil || !sender.generated || sender.admitted == actual {
					t.Fatal("ordinary generation changed or adopted unproven Inventory")
				}
				req.Operation, req.OwnerApproved = StackKitLifecycleApply, true
				if err := handler(t.Context(), &Job{ID: "job-unrefreshed-apply", TargetID: req.StackID, Payload: StackKitLifecyclePayload(req)}, NewQueue(0, nil)); err == nil || sender.applied {
					t.Fatal("unproven old Inventory bypassed Apply digest validation")
				}
				return
			}
			if scenario != "admitted" && scenario != "historical" && scenario != "cache-prepared-history" {
				if err == nil || sender.generated || sender.admitted == actual {
					t.Fatal("invalid generation authority adopted a new process pin")
				}
				if scenario == "cache-dry-run" || scenario == "cache-offline" || scenario == "cache-candidate" {
					if sender.cachePrepared {
						t.Fatal("cache preparation changed custody for a dry or replacement request")
					}
				}
				if scenario == "cache-denied" {
					var failure *typedStackKitOperationError
					denial, _ := job.Snapshot().Result["managed_source_cache_preparation"].(map[string]interface{})
					if !errors.As(err, &failure) || failure.Operation != stackKitStepPrepareSourceGeneration || failure.Failure.ReasonCode != "generation_invalid" || denial["success"] != false || sender.planned {
						t.Fatal("failed source cache preparation lost its typed denial or continued to PLAN")
					}
				}
				if scenario == "cache-release-substitution" && sender.planned {
					t.Fatal("substituted cache release continued to PLAN")
				}
				if scenario == "failed-plan" || scenario == "cache-failed-plan" {
					var failure *typedStackKitOperationError
					denial, _ := job.Snapshot().Result["managed_inventory_plan"].(map[string]interface{})
					if !errors.As(err, &failure) || failure.Operation != StackKitLifecyclePlan || failure.Failure.ReasonCode != "generation_invalid" || denial["operation"] != StackKitLifecyclePlan || denial["success"] != false || denial["exit_code"] != int32(1) {
						t.Fatal("failed prerequisite plan lost its typed denial evidence")
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			req.Operation, req.OwnerApproved = StackKitLifecycleApply, true
			apply := &Job{ID: "job-apply-1", TargetID: req.StackID, Payload: StackKitLifecyclePayload(req)}
			if err := handler(t.Context(), apply, NewQueue(0, nil)); err != nil {
				t.Fatal(err)
			}
			if !sender.applied || !sender.generated {
				t.Fatal("converged process never reached admitted Apply")
			}
			proof, _ := job.Snapshot().Result[managedInventoryRefreshResultField].(map[string]interface{})
			if proof["lease_id"] != "lease-1" || proof["server_id"] != "server-1" || proof["source_plan_hash"] != testResolvedPlanHash {
				t.Fatal("generation lost its runtime and source-plan custody")
			}
		})
	}
}

type generationInventoryProducer struct {
	builder *managedstackkit.RolloutInventory
	denied  bool
}

func (p generationInventoryProducer) Build(ctx context.Context, req ManagedStackKitInventoryRequest) ([]byte, error) {
	if p.denied {
		return nil, fmt.Errorf("custody unavailable")
	}
	if req.TenantID != "tenant-1" || req.StackID != "deployment-1" {
		return nil, fmt.Errorf("different custody identity")
	}
	return p.builder.Build(ctx, managedstackkit.RolloutInventoryRequest{TenantID: req.TenantID, StackID: req.StackID, ResolvedPlan: req.ResolvedPlan})
}

type generationInventorySender struct {
	t                          *testing.T
	scenario, actual, admitted string
	generated, applied         bool
	cachePrepared, planned     bool
}

func (s *generationInventorySender) SendStackKitCommand(_ context.Context, agent string, c *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	if agent != "agent-1" || c.SpecPath != "bound.json" || c.LocalExecutionChannelRef != "host-channel-cloud-main" {
		s.t.Fatal("command escaped its admitted node document/channel")
	}
	r := &agentpb.StackKitResult{CommandId: c.CommandId, Success: true, Release: c.Release, CommandResultJson: typedStackKitFakeSuccessResult(c)}
	switch c.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_PREPARE_SOURCE_GENERATION:
		if err := stackkitcommand.ValidateCommand(c); err != nil {
			return nil, err
		}
		if c.SourceGenerationInspection || s.generated {
			return nil, fmt.Errorf("cache preparation used inspection or followed generation")
		}
		if s.scenario == "cache-denied" {
			r.Success, r.ExitCode = false, 1
			r.CommandResultJson, _ = json.Marshal(map[string]any{"schemaVersion": "stackkit.command-result/v1", "command": "stackkit kit prepare-source-generation", "status": "failed", "data": map[string]any{"schemaVersion": "stackkit.actionable-error/v1", "reasonCode": "generation_invalid", "retryable": false}})
			return r, nil
		}
		s.cachePrepared = true
		if s.scenario == "cache-release-substitution" {
			r.Release = nil
		}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN:
		s.planned = true
		if strings.HasPrefix(s.scenario, "cache-") && !s.generated && !s.cachePrepared {
			return nil, fmt.Errorf("historical PLAN ran without explicitly prepared source cache")
		}
		var envelope map[string]any
		_ = json.Unmarshal(typedPlanCommandResult(testResolvedPlanHash), &envelope)
		var inspection map[string]any
		data := envelope["data"].(map[string]any)
		_ = json.Unmarshal([]byte(data["output"].(string)), &inspection)
		binding := inspection["binding"].(map[string]any)
		version := strings.TrimPrefix(c.Release.Version, "v")
		binding["compilerVersion"] = "stackkits-resolver/" + version
		binding["renderer"] = map[string]any{"version": version}
		if (s.scenario == "historical" || s.scenario == "cache-prepared-history" || s.scenario == "unproven-history") && !s.generated {
			if !c.SourceGenerationInspection {
				return nil, fmt.Errorf("historical inspection was not explicitly admitted for preparation")
			}
			binding["compilerVersion"] = "stackkits-resolver/0.50.6"
			binding["renderer"] = map[string]any{"version": "0.50.6"}
			if s.scenario == "historical" || s.scenario == "cache-prepared-history" {
				data["source_generation_authority"] = map[string]any{
					"apiVersion": "stackkit.source-generation-inspection/v1", "inspection": inspection,
					"sourceRelease":   map[string]any{"schemaVersion": "stackkit.release-receipt/v1", "kit": "cloud-kit", "version": "v0.50.6", "channel": "stable", "archiveSha256": strings.Repeat("a", 64), "indexSha256": strings.Repeat("b", 64)},
					"owner":           map[string]any{"ownerRef": "owner-1", "keyId": "key-1", "pocketIdSubject": "subject-1", "ownerBindingDigest": "sha256:" + strings.Repeat("c", 64)},
					"applyResultHash": "sha256:" + strings.Repeat("d", 64), "evidenceBundleHash": "sha256:" + strings.Repeat("e", 64),
				}
			}
		}
		output, _ := json.Marshal(inspection)
		data["output"] = string(output)
		instance := "cloud-stack"
		if s.scenario == "foreign-plan" {
			instance = "other-stack"
		}
		envelope["data"].(map[string]any)["managed_backup_plan"] = map[string]any{"apiVersion": "stackkit.resolved-plan/v1", "kind": "ResolvedPlan", "stackId": instance, "planHash": testResolvedPlanHash}
		r.CommandResultJson, _ = json.Marshal(envelope)
		if s.scenario == "different-release" {
			r.Release = nil
		}
		if s.scenario == "failed-plan" || s.scenario == "cache-failed-plan" {
			r.Success = false
			r.ExitCode = 1
			r.Stderr = "generation receipt validation refused"
			r.CommandResultJson, _ = json.Marshal(map[string]any{"schemaVersion": "stackkit.command-result/v1", "command": "stackkit plan", "status": "failed", "data": map[string]any{"schemaVersion": "stackkit.actionable-error/v1", "reasonCode": "generation_invalid", "retryable": false}})
		}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_GENERATE:
		if s.scenario == "standalone" || s.scenario == "old-payload" {
			if len(c.InventoryJson) != 0 {
				s.t.Fatal("standalone generation received managed Inventory")
			}
			s.generated = true
			return r, nil
		}
		var inventory struct {
			Channels map[string]struct {
				Process struct {
					Digest string `json:"executableSha256"`
				} `json:"operationsProcess"`
			} `json:"executionChannels"`
		}
		if json.Unmarshal(c.InventoryJson, &inventory) != nil || inventory.Channels["host-channel-cloud-main"].Process.Digest != s.actual {
			return nil, fmt.Errorf("generation retained stale executable custody")
		}
		s.admitted, s.generated = s.actual, true
	case agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY:
		if s.admitted != s.actual || c.ExpectedPlanHash != testResolvedPlanHash || len(c.InventoryJson) != 0 || c.SourceGenerationInspection {
			return nil, fmt.Errorf("Apply is not bound to the regenerated process authority")
		}
		s.applied = true
	default:
		s.t.Fatalf("unexpected lifecycle operation %s", c.Operation)
	}
	return r, nil
}
