package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
)

type repairSpecValidator func(context.Context, map[string]any) error

func (f repairSpecValidator) ValidateSpec(ctx context.Context, spec map[string]any) error {
	return f(ctx, spec)
}

// Recovery may repair the missing release-owned dependencies of the original
// workload, but cannot expand intent, bypass current runtime custody, lose a
// concurrent config winner, or replay a different candidate under one receipt.
func TestRetryRolloutRepairsOnlyAdmittedIntentAndBindsCandidateReplay(t *testing.T) {
	for _, scenario := range []string{"admitted", "partial INIT", "partial cross-job proof", "partial runtime", "partial release", "partial missing history", "partial missing hash", "partial malformed hash", "misplaced hash", "bootstrap interruption", "workload replacement", "unrelated binding", "validator refusal", "concurrent winner", "missing approval", "missing Guard", "applied installation"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			store := controlplane.NewMemoryStore()
			baseline := map[string]any{"apiVersion": "stackkit/v2alpha2", "kind": "StackSpec", "kit": map[string]any{"slug": "cloud-kit"}, "metadata": map[string]any{"name": "owned"}, "modules": map[string]any{"vault-module": map[string]any{"computeProfile": "standard"}}, "workloads": map[string]any{"vault": map[string]any{"enabled": true}}, "network": map[string]any{"domain": map[string]any{"base": "kombify.me"}}, "routes": map[string]any{"auth": map[string]any{"serviceRef": "auth", "host": "auth.kombify.me"}}}
			config := map[string]any{"runtime_lane": "monthly-runtime", "server_provisioning_mode": "kombify-cloud", "stack_spec_v2": baseline}
			if scenario == "applied installation" {
				config[jobs.StackKitRolloutBindingResultField] = jobs.StackKitRolloutBinding{StackKit: "cloud-kit", SpecPath: "existing.json"}.Map()
			}
			if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{ID: "stack-failed", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Owned", Status: "error", Config: config}); err != nil {
				t.Fatal(err)
			}
			sourceResult := map[string]any{"lease_id": "lease-failed"}
			if scenario == "admitted" || strings.HasPrefix(scenario, "partial ") || scenario == "misplaced hash" {
				sourceResult["typed_stackkit_rollout"] = map[string]any{"operation": "apply", "status": "failed", "command_id": "job-failed-apply", "release": "v0.37.1"}
				sourceResult["e2e_proof"] = map[string]any{"lease_id": "lease-failed", "server_id": runtimeidentity.LeaseServerID("lease-failed"),
					"runtime_agent_id": runtimeidentity.LeaseRuntimeAgentID("tenant-1", "lease-failed"), "stackkit_ref": "cloud-kit",
					"stack_spec_path": "/data/stacks/stack-failed/stack-spec.address-bound.v2.json"}
			}
			if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{ID: "job-failed", TenantID: "tenant-1", StackID: "stack-failed", Type: "deploy", State: "failed", Result: sourceResult}); err != nil {
				t.Fatal(err)
			}
			if scenario != "missing Guard" {
				seedManagedDeployEligibleServerRuntime(t, store, "tenant-1", "owner-1", "stack-failed", "lease-failed", time.Now().UTC())
			}
			candidate := map[string]any{}
			// Round-trip copying avoids mutating the stored baseline through map aliases.
			data, _ := json.Marshal(baseline)
			if err := json.Unmarshal(data, &candidate); err != nil {
				t.Fatal(err)
			}
			candidate["routes"].(map[string]any)["vault"] = map[string]any{"moduleRef": "vault-module", "serviceRef": "vault-store", "accessPolicyRef": "vault-policy", "host": "vault.kombify.me"}
			candidate["access"] = map[string]any{"vault-policy": map[string]any{"mode": "identity"}}
			candidate["data"] = map[string]any{"bindings": map[string]any{"vault-store": map[string]any{"classification": "private"}}}
			if scenario == "workload replacement" {
				candidate["workloads"] = map[string]any{"other": map[string]any{"enabled": true}}
			}
			if scenario == "unrelated binding" {
				candidate["data"].(map[string]any)["bindings"].(map[string]any)["scratch"] = map[string]any{"classification": "private"}
			}
			validator := repairSpecValidator(func(_ context.Context, spec map[string]any) error {
				if !reflect.DeepEqual(spec, candidate) {
					t.Fatal("validator did not receive the requested canonical candidate")
				}
				if scenario == "validator refusal" {
					return errors.New("canonical dependency refused")
				}
				if scenario == "concurrent winner" {
					current, _ := store.GetStack(ctx, "tenant-1", "stack-failed")
					winner := cloneProvisionSpec(current.Config)
					winner["owner_setting"] = "winner"
					if _, err := store.CompareAndSwapStackConfig(ctx, controlplane.StackConfigCAS{TenantID: "tenant-1", StackID: "stack-failed", ExpectedUpdatedAt: current.UpdatedAt, Config: winner}); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			})
			orch := New(&Config{Workers: 1, StackStore: store, JobStore: store, WorkerStore: store, RolloutRetrySpecValidator: validator, LeaseLister: fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{enrollmentResumeTestLease("lease-failed", "tenant-1", "owner-1", "stack-failed")}}}, nil)
			defer orch.Stop()
			req := RolloutRetryRequest{RequestContext: ctx, StackID: "stack-failed", TenantID: "tenant-1", OwnerID: "owner-1", SourceJobID: "job-failed", LeaseID: "lease-failed", CandidateSpec: candidate, OwnerApproved: scenario != "missing approval"}
			if strings.HasPrefix(scenario, "partial ") {
				original, _ := store.GetJob(ctx, "tenant-1", "job-failed")
				intent, err := rolloutRetryCurrentIntent(*original, req.LeaseID, runtimeidentity.LeaseServerID(req.LeaseID))
				if err != nil {
					t.Fatal(err)
				}
				priorID := original.ID
				if scenario == "partial missing history" {
					priorID = "unavailable-history"
				}
				key := exactRolloutRetryKey(req.StackID, priorID, req.LeaseID)
				id := exactRolloutRetryJobID(key)
				digest, _ := rolloutCandidateDigest(candidate)
				sealed := rolloutRetryDispatchResult(ProvisionStackOptions{rolloutRetryJobID: priorID, rolloutRetryKey: key,
					rolloutRetryNewID: id, RequiredLeaseID: req.LeaseID, RequiredServerID: runtimeidentity.LeaseServerID(req.LeaseID),
					rolloutRetryCandidateSHA: digest, rolloutRetryIntent: intent})
				sealed["typed_stackkit_rollout"] = map[string]any{"operation": "init", "status": "failed", "command_id": id + "-init", "release": "v0.37.1"}
				switch scenario {
				case "partial cross-job proof":
					sealed[jobs.RolloutRetryCurrentIntentField].(map[string]any)["source_job_id"] = "unrelated-job"
				case "partial runtime":
					sealed[rolloutRetryServerField] = "foreign-server"
				case "partial release":
					sealed["typed_stackkit_rollout"].(map[string]any)["release"] = "v0.38.0"
				}
				if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{ID: id, TenantID: req.TenantID, StackID: req.StackID, Type: "deploy", State: "failed", Result: sealed}); err != nil {
					t.Fatal(err)
				}
				req.SourceJobID = id
				req.CurrentSpecHash = "sha256:" + strings.Repeat("b", 64)
				if scenario == "partial missing hash" {
					req.CurrentSpecHash = ""
				}
				if scenario == "partial malformed hash" {
					req.CurrentSpecHash = "raw-file-hash"
				}
			}
			if scenario == "misplaced hash" {
				req.CurrentSpecHash = "sha256:" + strings.Repeat("b", 64)
			}
			if scenario == "bootstrap interruption" {
				req.IssueOwnerSpecBootstrap = func() (*jobs.OwnerSpecBootstrap, error) { return nil, errors.New("custody unavailable after CAS") }
				if _, err := orch.RetryRollout(req); err == nil {
					t.Fatal("bootstrap interruption dispatched")
				}
				omitted := req
				omitted.CandidateSpec = nil
				if _, err := orch.RetryRollout(omitted); err == nil {
					t.Fatal("interrupted repair lost its candidate binding")
				}
				changed := req
				changed.CandidateSpec = cloneProvisionSpec(candidate)
				changed.CandidateSpec["metadata"] = map[string]any{"name": "changed"}
				if _, err := orch.RetryRollout(changed); err == nil {
					t.Fatal("interrupted repair allowed another candidate")
				}
				req.IssueOwnerSpecBootstrap = nil
			}
			result, err := orch.RetryRollout(req)
			current, _ := store.GetStack(ctx, "tenant-1", "stack-failed")
			if scenario != "admitted" && scenario != "partial INIT" && scenario != "bootstrap interruption" {
				if err == nil {
					t.Fatal("invalid repair was admitted")
				}
				if !reflect.DeepEqual(current.Config["stack_spec_v2"], baseline) {
					t.Fatal("denied repair replaced original canonical intent")
				}
				if scenario == "concurrent winner" && current.Config["owner_setting"] != "winner" {
					t.Fatal("repair lost the concurrent winner")
				}
				if _, err := store.GetJob(ctx, "tenant-1", exactRolloutRetryJobID(exactRolloutRetryKey(req.StackID, req.SourceJobID, req.LeaseID))); !errors.Is(err, controlplane.ErrNotFound) {
					t.Fatal("denied repair dispatched a rollout")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			job, err := orch.GetJobStatus(result.JobID)
			digest, digestErr := rolloutCandidateDigest(candidate)
			if err != nil || digestErr != nil || job.Payload[jobs.RolloutRetryCandidateSHA256Field] != digest || !reflect.DeepEqual(job.Payload["stack_spec_v2"], candidate) || !reflect.DeepEqual(current.Config["stack_spec_v2"], candidate) {
				t.Fatalf("canonical repaired rollout was not dispatched: %v", err)
			}
			if scenario == "admitted" {
				binding, _ := providerIdentityMap(job.Payload[jobs.RolloutRetryCurrentIntentField])
				if stringFromAny(binding["source_job_id"]) != "job-failed" || stringFromAny(binding["agent_id"]) != runtimeidentity.LeaseRuntimeAgentID("tenant-1", "lease-failed") {
					t.Fatal("retry lost its validated failed Apply intent binding")
				}
			}
			if scenario == "partial INIT" {
				binding, _ := providerIdentityMap(job.Payload[jobs.RolloutRetryCurrentIntentField])
				if stringFromAny(binding["source_job_id"]) != req.SourceJobID || stringFromAny(binding["apply_source_job_id"]) != "job-failed" || job.Payload[jobs.RolloutRetryCurrentSpecHashField] != req.CurrentSpecHash {
					t.Fatal("partial INIT lost its proven APPLY lineage or caller CAS precondition")
				}
				changed := req
				changed.CurrentSpecHash = "sha256:" + strings.Repeat("c", 64)
				if _, err := orch.RetryRollout(changed); err == nil {
					t.Fatal("changed CAS precondition reused the durable dispatch")
				}
			}
			replay, err := orch.RetryRollout(req)
			if err != nil || replay.JobID != result.JobID || !replay.IdempotentReplay {
				t.Fatalf("exact candidate replay failed: %v", err)
			}
			if scenario == "partial INIT" {
				// Simulate the admitting process ending with a durable pending job.
				// Boot recovery must execute its seal, never a changed precondition
				// or lineage reconstructed from mutable caller payload.
				orch.cancel()
				orch.Stop()
				pending, err := store.GetJob(ctx, req.TenantID, result.JobID)
				if err != nil {
					t.Fatal(err)
				}
				for _, mutation := range []string{"valid", "hash", "lineage", "historical proof"} {
					t.Run("restart "+mutation, func(t *testing.T) {
						payload := cloneProvisionSpec(pending.Payload)
						receipt := cloneProvisionSpec(pending.Result)
						switch mutation {
						case "hash":
							payload[jobs.RolloutRetryCurrentSpecHashField] = "sha256:" + strings.Repeat("c", 64)
						case "lineage":
							payload[jobs.RolloutRetryCurrentIntentField].(map[string]any)["agent_id"] = "foreign-agent"
							receipt[jobs.RolloutRetryCurrentIntentField].(map[string]any)["agent_id"] = "foreign-agent"
						case "historical proof":
							original, _ := store.GetJob(ctx, req.TenantID, "job-failed")
							original.Result["e2e_proof"].(map[string]any)["server_id"] = "foreign-server"
							if _, err := store.UpsertJob(ctx, controlplane.UpsertJobRequest{ID: original.ID, TenantID: original.TenantID, StackID: original.StackID, Type: original.Type, State: original.State, Result: original.Result}); err != nil {
								t.Fatal(err)
							}
						}
						if _, err := store.UpdateStackRuntime(ctx, req.TenantID, req.StackID, controlplane.RuntimeUpdate{Status: "provisioning"}); err != nil {
							t.Fatal(err)
						}
						if _, err := store.UpsertJob(ctx, controlplane.UpsertJobRequest{ID: pending.ID, TenantID: pending.TenantID, StackID: pending.StackID, Type: pending.Type, State: "pending", Result: receipt, Payload: payload}); err != nil {
							t.Fatal(err)
						}
						restarted := New(&orch.cfg, nil)
						defer restarted.Stop()
						handled := make(chan string, 1)
						restarted.queue.RegisterHandler(jobs.JobTypeDeploy, func(_ context.Context, job *jobs.Job, _ *jobs.Queue) error {
							handled <- job.ID
							return nil
						})
						if _, err := restarted.RequeuePendingJobs(ctx); err != nil {
							t.Fatal(err)
						}
						restarted.Start()
						if mutation != "valid" {
							if _, queued := restarted.queue.Get(pending.ID); queued {
								t.Fatal("altered pending CAS or lineage reached execution after restart")
							}
							return
						}
						select {
						case id := <-handled:
							if id != pending.ID {
								t.Fatal("restart executed another dispatch")
							}
						case <-time.After(5 * time.Second):
							t.Fatal("valid sealed partial INIT never executed after restart")
						}
						deadline := time.Now().Add(5 * time.Second)
						for {
							completed, err := store.GetJob(ctx, req.TenantID, pending.ID)
							if err != nil {
								t.Fatal(err)
							}
							if completed.State == "completed" {
								break
							}
							if time.Now().After(deadline) {
								t.Fatal("restarted sealed retry did not persist its successful execution")
							}
							time.Sleep(20 * time.Millisecond)
						}
					})
				}
				return
			}
			candidate["access"].(map[string]any)["vault-policy"] = map[string]any{"mode": "changed"}
			if _, err := orch.RetryRollout(req); err == nil {
				t.Fatal("changed candidate reused the sealed retry receipt")
			}
		})
	}
}
