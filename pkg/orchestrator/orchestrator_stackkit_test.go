package orchestrator

import (
	"errors"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	vmleases "github.com/kombifyio/techstack/pkg/vmleases"
	"time"

	"github.com/kombifyio/techstack/pkg/jobs"
)

func TestLifecycleBindsStoredStackKitInstance(t *testing.T) {
	for _, operation := range []string{jobs.StackKitLifecycleServiceRestart, jobs.StackKitLifecycleRemove, jobs.StackKitLifecycleGenerate, jobs.StackKitLifecycleBackupRestoreActivate, jobs.StackKitLifecycleBackupRestoreRecover} {
		t.Run(operation, func(t *testing.T) {
			got, err := bindStackKitLifecycleInstance(jobs.StackKitLifecycleRequest{Operation: operation, StackKitInstanceID: "caller-value", ManagedRuntimeLeaseID: "lease-1"}, &orchestratorStack{stackKitInstanceID: "owner-kit"})
			if err != nil || got.StackKitInstanceID != "owner-kit" {
				t.Fatalf("bound request = %#v, error = %v", got, err)
			}
		})
	}
}

func TestRestoreActivationCannotBypassManagedBackupCustody(t *testing.T) {
	for _, kit := range []string{"cloud-kit", "modern-kit"} {
		for _, operation := range []string{jobs.StackKitLifecycleBackupRestoreActivate, jobs.StackKitLifecycleBackupRestoreRecover} {
			t.Run(kit+"/"+operation, func(t *testing.T) {
				rollout := jobs.StackKitRolloutBinding{StackKit: kit, SpecPath: "bound.json"}
				orch, _ := newRolledOutStack(t, map[string]any{jobs.StackKitRolloutBindingResultField: rollout.Map()})
				req := jobs.StackKitLifecycleRequest{StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", Operation: operation, StackKit: "basement", OwnerApproved: true, RestoreResultID: "sha256:" + strings.Repeat("a", 64)}
				if operation == jobs.StackKitLifecycleBackupRestoreActivate {
					var err error
					req, err = jobs.PrepareRestoreActivationIdempotency(req, "managed-denial")
					if err != nil {
						t.Fatal(err)
					}
				}
				if operation == jobs.StackKitLifecycleBackupRestoreRecover {
					req.ActivationOperationID = "restore-original-job"
					req.RestorePlanHash = "sha256:" + strings.Repeat("b", 64)
				}
				if _, err := orch.EnqueueStackKitLifecycle(t.Context(), req); !errors.Is(err, ErrStackKitBackupCustodyManaged) {
					t.Fatalf("managed native activation admission: %v", err)
				}
			})
		}
	}
	for _, scenario := range []string{"owned-recovery", "owned-new-checkpoint", "foreign-owner", "foreign-node", "foreign-instance", "foreign-result", "foreign-plan", "missing-directory", "conflicting-directory", "ambiguous-directory"} {
		t.Run(scenario, func(t *testing.T) {
			rollout := jobs.StackKitRolloutBinding{StackKit: "basement-kit", SpecPath: "bound.json"}
			orch, store := newRolledOutStack(t, map[string]any{jobs.StackKitRolloutBindingResultField: rollout.Map()})
			resultID := "sha256:" + strings.Repeat("a", 64)
			planHash := "sha256:" + strings.Repeat("b", 64)
			admission := map[string]interface{}{"tenant_id": "tenant-1", "owner_id": "owner-1", "stack_id": "stack-1", "stackkit_instance_id": "home", "agent_id": "agent-1", "node_id": "server-1", "node_ref": "main", "spec_path": "bound.json", "restore_result_id": resultID, "activation_operation_id": "restore-original-job", "plan_hash": planHash, "release_version": "v0.51.8", "release_archive_sha256": strings.Repeat("c", 64)}
			directory := "/srv/owned stack/original"
			originalRequest := jobs.StackKitLifecycleRequest{StackID: "stack-1", StackKitInstanceID: "home", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", NodeID: "server-1", NodeRef: "main", Operation: jobs.StackKitLifecycleBackupRestoreActivate, OwnerApproved: true, WorkingDirectory: directory, SpecPath: "bound.json", RestoreResultID: resultID}
			payload := jobs.StackKitLifecyclePayload(originalRequest)
			if scenario == "owned-new-checkpoint" {
				admission["working_directory"] = directory
			}
			if scenario == "missing-directory" {
				delete(payload, "working_directory")
			}
			if scenario == "conflicting-directory" {
				admission["working_directory"] = "/different/journal"
			}
			if scenario == "ambiguous-directory" {
				payload["working_directory"] = `C:\foreign\journal`
			}
			if scenario == "foreign-instance" {
				admission["stackkit_instance_id"] = "other-instance"
			}
			if scenario == "foreign-node" {
				admission["node_id"] = "other-node"
			}
			if scenario == "foreign-result" {
				admission["restore_result_id"] = "sha256:" + strings.Repeat("d", 64)
			}
			if scenario == "foreign-plan" {
				admission["plan_hash"] = "sha256:" + strings.Repeat("d", 64)
			}
			if _, err := store.CreateJob(t.Context(), controlplane.UpsertJobRequest{ID: "original-job", TenantID: "tenant-1", StackID: "stack-1", Type: string(jobs.JobTypeStackKitLifecycle), State: "pending", Payload: payload, Result: map[string]interface{}{jobs.RestoreActivationCheckpointResultKey: admission}}); err != nil {
				t.Fatal(err)
			}
			req := jobs.StackKitLifecycleRequest{StackID: "stack-1", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", WorkingDirectory: "/opt/current-new-default", Operation: jobs.StackKitLifecycleBackupRestoreRecover, OwnerApproved: true, RestoreResultID: resultID, ActivationOperationID: "restore-original-job", RestorePlanHash: planHash}
			if scenario == "foreign-owner" {
				req.OwnerID = "owner-2"
			}
			id, err := orch.EnqueueStackKitLifecycle(t.Context(), req)
			if scenario != "owned-recovery" && scenario != "owned-new-checkpoint" {
				if err == nil {
					t.Fatal("foreign recovery admission enqueued")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			job, ok := orch.Queue().Get(id)
			if !ok {
				t.Fatal("recovery was not enqueued")
			}
			bound := job.Snapshot().Result[jobs.RestoreActivationCheckpointResultKey].(map[string]interface{})
			if job.Snapshot().Payload["working_directory"] != directory || bound["working_directory"] != directory {
				t.Fatal("recovery selected the current directory instead of the original node journal")
			}
			if bound["release_version"] != admission["release_version"] || bound["release_archive_sha256"] != admission["release_archive_sha256"] || bound["activation_operation_id"] != req.ActivationOperationID {
				t.Fatal("recovery replaced original release/journal authority")
			}
			changed := jobs.StackKitLifecyclePayload(originalRequest)
			changed["working_directory"] = "/different/journal"
			if err := store.SetJobPayload(t.Context(), "tenant-1", "original-job", changed); !errors.Is(err, controlplane.ErrConflict) {
				t.Fatalf("admitted original directory could change: %v", err)
			}
			if _, err := store.UpsertJob(t.Context(), controlplane.UpsertJobRequest{ID: "original-job", TenantID: "tenant-1", StackID: "stack-1", Type: string(jobs.JobTypeStackKitLifecycle), State: "pending", Payload: changed}); !errors.Is(err, controlplane.ErrConflict) {
				t.Fatalf("Upsert could erase original directory authority: %v", err)
			}
			row, err := store.GetJob(t.Context(), "tenant-1", "original-job")
			if err != nil {
				t.Fatal(err)
			}
			hydrated, err := orch.hydratePendingOriginalRestore(t.Context(), *row)
			if err != nil || hydrated.Result[jobs.RestoreActivationCheckpointResultKey].(map[string]any)["working_directory"] != directory {
				t.Fatalf("original restart directory proof failed: %v", err)
			}
			if scenario == "owned-recovery" && admission["working_directory"] != nil {
				t.Fatal("legacy admission was rewritten during hydration")
			}
			if row.Payload["activation_operation_id"] != "" {
				t.Fatal("original caller payload invented an activation operation")
			}
			wrongOperation := make(map[string]any, len(admission))
			for key, value := range admission {
				wrongOperation[key] = value
			}
			wrongOperation["activation_operation_id"] = "restore-foreign-job"
			wrongRow := *row
			wrongRow.Result = map[string]any{jobs.RestoreActivationCheckpointResultKey: wrongOperation}
			// A stale or forged copied boot observation cannot substitute its
			// journal even when the exact stored row has valid authority.
			if _, err := orch.hydratePendingOriginalRestore(t.Context(), wrongRow); err == nil {
				t.Fatal("conflicting observed activation operation was admitted")
			}
			legacyRecovery := *row
			legacyRecovery.ID = "legacy-recovery-job"
			legacyRecovery.Payload = job.Snapshot().Payload
			if recovered, err := orch.hydratePendingOriginalRestore(t.Context(), legacyRecovery); err != nil || recovered.Result[jobs.RestoreActivationCheckpointResultKey].(map[string]any)["working_directory"] != directory {
				t.Fatalf("legacy recovery restart lost its exact original directory: %v", err)
			}
			mismatched := make(map[string]any, len(legacyRecovery.Payload))
			for key, value := range legacyRecovery.Payload {
				mismatched[key] = value
			}
			mismatched["working_directory"] = "/different/journal"
			legacyRecovery.Payload = mismatched
			if _, err := orch.hydratePendingOriginalRestore(t.Context(), legacyRecovery); err == nil {
				t.Fatal("legacy recovery directory mismatch was silently migrated")
			}
			if _, err := store.StartJob(t.Context(), "tenant-1", "original-job", time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if _, err := store.CompleteJob(t.Context(), "tenant-1", "original-job", map[string]any{}, time.Now().UTC()); !errors.Is(err, controlplane.ErrConflict) {
				t.Fatalf("CompleteJob could erase original directory authority: %v", err)
			}
		})
	}
}

// Admission and the registered job-authority seam read actual canonical
// stores. A later owner, node, lease, resource or credential change must deny
// the retained operation before another command can be sent or projected.
func TestApplicationConnectorAuthorityRejoinsCanonicalRuntimeAfterAdmission(t *testing.T) {
	for _, scenario := range []string{"current", "owner", "server-generation", "node", "worker", "lease", "provider", "credential-generation", "stale-heartbeat", "spec", "revoked-key", "dangling-key", "legacy-channel", "lease-withdrawn"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			rollout := jobs.StackKitRolloutBinding{StackKit: "basement-kit", SpecPath: "bound.json"}
			orch, store := newRolledOutStack(t, map[string]any{jobs.StackKitRolloutBindingResultField: rollout.Map()})
			now := time.Now().UTC()
			digest := strings.Repeat("a", 64)
			serverID, leaseID := "server-1", ""
			var leaseAuthority *fakeManagedRuntimeLeaseLister
			if scenario == "lease-withdrawn" {
				leaseID = "lease-connector"
				serverID = runtimeidentity.LeaseServerID(leaseID)
				lease := managedDeployLeaseFixture(leaseID, "tenant-1", "owner-1", "stack-1", "foundation", "ionos-managed", "", now)
				leaseAuthority = &fakeManagedRuntimeLeaseLister{records: []vmleases.LeaseInventoryRecord{{Lease: lease, ExecutionAuthority: vmleases.LeaseExecutionAuthorityTechStackProviderControl, AuthorityState: vmleases.LeaseAuthorityStateNativeActive}}}
				orch.leaseLister = leaseAuthority
			}
			initial := controlplane.WorkerCredentialState{Generation: 1, TokenSHA256: digest, IdempotencySHA256: digest, RequestSHA256: digest}
			if _, err := store.CompareAndSwapWorkerCredential(ctx, controlplane.WorkerCredentialCAS{TenantID: "tenant-1", WorkerID: "worker-1", Next: initial}); err != nil {
				t.Fatal(err)
			}
			worker, err := store.GetWorker(ctx, "tenant-1", "worker-1")
			if err != nil {
				t.Fatal(err)
			}
			worker.Capabilities["identity_binding"] = controlplane.IdentityBindingHostKey
			worker.Capabilities["server_id"], worker.Capabilities["node_id"] = serverID, serverID
			if _, err = store.UpsertWorkerHeartbeat(ctx, *worker); err != nil {
				t.Fatal(err)
			}
			if _, err = store.IssueWorkerIdentity(ctx, controlplane.WorkerIdentityIssue{TenantID: "tenant-1", WorkerID: "worker-1", Thumbprint: "synthetic-public-key", PublicKeyJWK: "synthetic-public-jwk", IssuedAt: now}); err != nil {
				t.Fatal(err)
			}
			if _, err = store.ConfirmWorkerIdentity(ctx, "tenant-1", "worker-1", "synthetic-public-key", now); err != nil {
				t.Fatal(err)
			}
			runtime := controlplane.ServerRuntime{ID: serverID, TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", WorkerID: "worker-1", NodeID: serverID, LeaseID: leaseID, Generation: 1, LifecycleState: "active", ConnectionState: "connected", HealthState: "healthy", LastHeartbeatAt: &now, Metadata: map[string]any{"identity_binding": controlplane.IdentityBindingHostKey}}
			if _, err := store.UpsertServerRuntime(ctx, runtime); err != nil {
				t.Fatal(err)
			}
			request := stackkitcommand.ConnectorRequest{SchemaVersion: stackkitcommand.ApplicationConnectorRequestSchema}
			request.Connector.Binding = stackkitcommand.ConnectorSource{ResourceID: "ha", ContainerID: strings.Repeat("b", 64), ImageDigest: "sha256:" + strings.Repeat("c", 64), ServiceRevision: 1}
			request.Connector.Action = "ensure"
			request.Connector.LifespanDays = 30
			req := jobs.StackKitLifecycleRequest{StackID: "stack-1", StackKitInstanceID: "home", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", NodeID: serverID, StackKit: "basement-kit", SpecPath: "bound.json", ServiceID: "ha", ServiceKey: "smart-home", DurableJobID: "connector-original", ServiceActionDigest: strings.Repeat("d", 64), Operation: jobs.StackKitLifecycleApplicationConnector, OwnerApproved: true, Connector: &jobs.ApplicationConnectorBinding{Request: request, ServerID: serverID, ServerGeneration: 1, NodeID: serverID, LeaseID: leaseID}}
			id, err := orch.EnqueueStackKitLifecycle(ctx, req)
			if err != nil || id == "" {
				t.Fatalf("canonical source admission failed: %v", err)
			}
			// Enqueue derives credential generation and the exact kit node itself.
			req.Connector.WorkerGeneration = 1
			readReq := req
			readReq.AgentID = "worker-1"
			if err = orch.AuthorizeApplicationConnectorRead(ctx, readReq); err != nil {
				t.Fatalf("current readonly authority refused: %v", err)
			}
			switch scenario {
			case "lease-withdrawn":
				leaseAuthority.records[0].AuthorityState = vmleases.LeaseAuthorityStateNativeInactive
			case "revoked-key":
				if err = store.RevokeWorkerIdentity(ctx, "tenant-1", "worker-1", now); err != nil {
					t.Fatal(err)
				}
			case "dangling-key":
				if _, err = store.IssueWorkerIdentity(ctx, controlplane.WorkerIdentityIssue{TenantID: "tenant-1", WorkerID: "worker-1", Thumbprint: "unconfirmed-public-key", PublicKeyJWK: "synthetic-public-jwk", IssuedAt: now}); err != nil {
					t.Fatal(err)
				}
			case "legacy-channel":
				runtime.Metadata["identity_binding"] = controlplane.IdentityBindingLegacyBearer
			case "owner":
				runtime.OwnerSubjectID = "another-owner"
			case "server-generation":
				runtime.Generation = 2
			case "node":
				runtime.NodeID = "another-node"
			case "worker":
				runtime.WorkerID = "another-worker"
			case "lease":
				runtime.LeaseID = "unadmitted-lease"
			case "provider":
				runtime.ProviderRef = "another-provider"
			case "stale-heartbeat":
				now = now.Add(-time.Hour)
			case "spec":
				req.SpecPath = "another.json"
			case "credential-generation":
				next := initial
				next.Generation = 2
				next.TokenSHA256 = strings.Repeat("e", 64)
				if _, err := store.CompareAndSwapWorkerCredential(ctx, controlplane.WorkerCredentialCAS{TenantID: "tenant-1", WorkerID: "worker-1", Expected: initial, Next: next}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "credential-generation" && scenario != "spec" {
				if _, err := store.UpsertServerRuntime(ctx, runtime); err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "spec" {
				readErr := orch.AuthorizeApplicationConnectorRead(ctx, readReq)
				if (readErr == nil) != (scenario == "current") {
					t.Fatalf("withdrawn readonly authority for %s: %v", scenario, readErr)
				}
			}
			err = orch.authorizeApplicationConnector(ctx, req)
			if (err == nil) != (scenario == "current") {
				t.Fatalf("retained operation authority for %s: %v", scenario, err)
			}
		})
	}
}
