package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

func TestLifecycleBackupRunDispatchesPlanBoundBackupRunWithOperationID(t *testing.T) {
	sender := &typedLifecycleCommander{planHash: testResolvedPlanHash}
	handler := StackKitLifecycleHandler(StackKitLifecycleConfig{
		Sender:          sender,
		releaseResolver: func() (*stackkitrelease.Release, error) { return &stackkitrelease.Release{}, nil },
	})
	req := StackKitLifecycleRequest{
		StackID: "stack-1", StackKitInstanceID: "home-stack", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1",
		Operation: StackKitLifecycleBackupRun, OwnerApproved: true, StackKit: "basement-kit", SpecPath: "stack-spec.v2.json",
	}
	job := &Job{ID: "job-backup-1", TargetID: req.StackID, Payload: StackKitLifecyclePayload(req)}
	if err := handler(context.Background(), job, NewQueue(0, nil)); err != nil {
		t.Fatal(err)
	}
	run := sender.commands[len(sender.commands)-1]
	if run.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RUN || run.CommandId != job.ID ||
		run.ExpectedPlanHash != testResolvedPlanHash || run.StackkitInstanceId != "home-stack" || run.SpecPath != "stack-spec.v2.json" {
		t.Fatalf("backup run command = %+v", run)
	}
	backup, _ := job.Snapshot().Result["backup"].(map[string]interface{})
	if backup["operation_id"] != job.ID || backup["snapshot_anchor_id"] == "" {
		t.Fatalf("backup evidence = %+v", backup)
	}
}

// The external node boundary retains the original activation journal. The
// actual job handler must reconcile its receipt rather than infer success from
// a failed command or activate again under a convergence retry ID.
func TestLifecycleRestoreActivationReconcilesOnlyOriginalNodeJournal(t *testing.T) {
	previousDelay := typedStackKitRuntimePendingRetryDelay
	typedStackKitRuntimePendingRetryDelay = 0
	t.Cleanup(func() { typedStackKitRuntimePendingRetryDelay = previousDelay })
	for _, scenario := range []string{"activated", "recovered", "committed-lost-ack", "missing-journal", "foreign-result", "explicit-recover", "checkpoint-fenced", "restart-after-admission", "restart-foreign-release", "restart-cross-release", "restart-changed-directory", "pre-admission-refused"} {
		t.Run(scenario, func(t *testing.T) {
			sender := &restoreJournalCommander{scenario: scenario}
			currentRelease := stackkitrelease.Release{}
			if scenario == "restart-cross-release" {
				currentRelease = releaseWithAdvancedCatalogVersion(t, "v0.52.5")
			}
			handler := StackKitLifecycleHandler(StackKitLifecycleConfig{Sender: sender, releaseResolver: func() (*stackkitrelease.Release, error) { return &currentRelease, nil }})
			req := StackKitLifecycleRequest{StackID: "stack-1", StackKitInstanceID: "home-stack", TenantID: "tenant-1", OwnerID: "owner-1", AgentID: "agent-1", Operation: StackKitLifecycleBackupRestoreActivate, OwnerApproved: true, StackKit: "basement-kit", WorkingDirectory: "/srv/owner/original", SpecPath: "stack-spec.v2.json", RestoreResultID: "sha256:" + strings.Repeat("a", 64)}
			if scenario == "explicit-recover" {
				req.Operation = StackKitLifecycleBackupRestoreRecover
				req.ActivationOperationID = "restore-original-job"
				req.RestorePlanHash = testResolvedPlanHash
			}
			job := &Job{ID: "activation-job-1", TargetID: req.StackID, Payload: StackKitLifecyclePayload(req)}
			if scenario == "pre-admission-refused" {
				var err error
				req, err = PrepareRestoreActivationIdempotency(req, "not-admitted-probe")
				if err != nil {
					t.Fatal(err)
				}
				job.Payload = StackKitLifecyclePayload(req)
				job.Result = map[string]interface{}{controlplane.RestoreActivationRequestResultKey: RestoreActivationRequestReceipt(req)}
			}
			if scenario == "explicit-recover" {
				job.Result = map[string]interface{}{restoreActivationCheckpointKey: restoreActivationCheckpoint(req, stackkitrelease.Release{}, req.RestorePlanHash)}
			}
			queue, store := restoreCheckpointQueue(t, job)
			if scenario == "checkpoint-fenced" {
				wrongGeneration := job.StartedAt.Add(time.Second)
				job.StartedAt = &wrongGeneration
				if err := handler(context.Background(), job, queue); err == nil {
					t.Fatal("activation ignored durable execution fence")
				}
				if sender.effects != 0 {
					t.Fatal("fenced checkpoint mutated node")
				}
				return
			}
			if strings.HasPrefix(scenario, "restart-") {
				crashed := false
				func() {
					defer func() {
						if recover() == "process-death" {
							crashed = true
						}
					}()
					_ = handler(context.Background(), job, queue)
				}()
				if !crashed {
					t.Fatal("process-death probe did not cross dispatch")
				}
				persisted, err := store.GetJob(t.Context(), req.TenantID, job.ID)
				if err != nil {
					t.Fatal(err)
				}
				bytes, err := json.Marshal(persisted.Result)
				if err != nil {
					t.Fatal(err)
				}
				var durable map[string]interface{}
				if err = json.Unmarshal(bytes, &durable); err != nil {
					t.Fatal(err)
				}
				job = &Job{ID: persisted.ID, Type: JobTypeStackKitLifecycle, TargetID: persisted.StackID, Payload: persisted.Payload, Result: durable, State: JobStateRunning, StartedAt: persisted.StartedAt}
				sender.scenario = "resume-original"
				sender.commands = nil
				if scenario == "restart-cross-release" {
					currentRelease = releaseWithAdvancedCatalogVersion(t, "v0.52.6")
				}
				if scenario == "restart-changed-directory" {
					job.Payload["working_directory"] = "/srv/owner/replacement"
					if err := handler(context.Background(), job, queue); err == nil || len(sender.commands) != 0 {
						t.Fatal("restart crossed the original workspace directory")
					}
					return
				}
				if scenario == "restart-foreign-release" {
					durable[restoreActivationCheckpointKey].(map[string]interface{})["release_archive_sha256"] = strings.Repeat("b", 64)
					if err := handler(context.Background(), job, queue); err == nil {
						t.Fatal("restart silently rebound configured release")
					}
					if len(sender.commands) != 0 {
						t.Fatal("foreign checkpoint dispatched node work")
					}
					return
				}
			}
			err := handler(context.Background(), job, queue)
			if scenario == "pre-admission-refused" {
				backup, _ := job.Snapshot().Result["backup"].(map[string]interface{})
				if err == nil || sender.effects != 0 || backup["mode"] != "not-admitted" || backup["request_digest"] != req.ServiceActionDigest || backup["key_sha256"] != req.RestoreIdempotencyKeySHA256 || job.Snapshot().Result[restoreActivationCheckpointKey] != nil {
					t.Fatalf("pre-admission refusal was not positively correlated: %v %v", backup, err)
				}
				return
			}
			backup, _ := job.Snapshot().Result["backup"].(map[string]interface{})
			want := "activated"
			if scenario == "recovered" || scenario == "explicit-recover" {
				want = "recovered"
			}
			if scenario == "missing-journal" || scenario == "foreign-result" {
				want = "recovery-required"
			}
			if backup["mode"] != want || (err != nil) != (scenario == "recovered" || want == "recovery-required") {
				t.Fatalf("outcome=%v error=%v", backup, err)
			}
			original := "restore-" + job.ID
			if scenario == "explicit-recover" {
				original = req.ActivationOperationID
			}
			if backup["activation_operation_id"] != original {
				t.Fatalf("lost original journal: %v", backup)
			}
			for _, c := range sender.commands {
				if stackkitcommand.IsRestoreActivationOperation(c.Operation) && (c.ActivationOperationId != original || c.RestoreResultId != req.RestoreResultID || c.ExpectedPlanHash != testResolvedPlanHash) {
					t.Fatalf("foreign activation authority: %+v", c)
				}
				if scenario == "explicit-recover" && c.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN {
					t.Fatal("recovery required a fresh plan instead of retained journal authority")
				}
				if strings.HasPrefix(scenario, "restart-") && (c.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN || c.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE_ACTIVATE) {
					t.Fatal("restart rebound fresh plan or dispatched second activation")
				}
				if scenario == "restart-cross-release" && (c.Release.Version != "v0.52.6" || c.OriginalRecoveryRelease == nil || c.OriginalRecoveryRelease.Version != "v0.52.5" || c.WorkingDirectory != req.WorkingDirectory) {
					t.Fatal("original recovery lost distinct execution/original release or workspace authority")
				}
			}
			if sender.effects > 1 {
				t.Fatal("transport retry activated more than once")
			}
			if scenario == "recovered" && job.Snapshot().Result["success"] != false {
				t.Fatal("prior-state recovery claimed selected-backup activation success")
			}
		})
	}
}

func restoreCheckpointQueue(t *testing.T, job *Job) (*Queue, *controlplane.MemoryStore) {
	t.Helper()
	store := controlplane.NewMemoryStore()
	job.Type = JobTypeStackKitLifecycle
	if _, err := store.CreateJob(t.Context(), controlplane.UpsertJobRequest{ID: job.ID, TenantID: "tenant-1", StackID: job.TargetID, Type: string(job.Type), State: "pending", Payload: job.Payload}); err != nil {
		t.Fatal(err)
	}
	started, err := store.StartJob(t.Context(), "tenant-1", job.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	job.State, job.StartedAt = JobStateRunning, started.StartedAt
	queue := NewQueue(0, nil)
	queue.SetExecutionSnapshotSyncer(func(ctx context.Context, snapshot JobSnapshot) error {
		// Delayed same-generation heartbeat from before admission: the actual
		// store must retain the acknowledged tuple when this lands after ACK.
		older := snapshot
		older.Result = map[string]interface{}{"progress": "before-admission"}
		_, err := store.SyncJobSnapshot(ctx, controlplane.SyncJobSnapshotRequest{Job: controlplane.UpsertJobRequest{ID: snapshot.ID, TenantID: "tenant-1", StackID: snapshot.TargetID, Type: string(snapshot.Type), State: string(snapshot.State), Result: snapshot.Result}, ObservedState: string(snapshot.State), AttemptStartedAt: snapshot.StartedAt})
		if err == nil {
			_, err = store.SyncJobSnapshot(ctx, controlplane.SyncJobSnapshotRequest{Job: controlplane.UpsertJobRequest{ID: older.ID, TenantID: "tenant-1", StackID: older.TargetID, Type: string(older.Type), State: string(older.State), Result: older.Result}, ObservedState: string(older.State), AttemptStartedAt: older.StartedAt})
		}
		return err
	})
	return queue, store
}

type restoreJournalCommander struct {
	scenario string
	commands []*agentpb.StackKitCommand
	pending  bool
	effects  int
}

func (s *restoreJournalCommander) SendStackKitCommand(_ context.Context, _ string, c *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	s.commands = append(s.commands, c)
	if c.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN {
		if s.scenario == "pre-admission-refused" {
			return nil, fmt.Errorf("current node cannot admit the selected plan")
		}
		return &agentpb.StackKitResult{Success: true, CommandResultJson: typedPlanCommandResult(testResolvedPlanHash), Release: c.Release}, nil
	}
	if c.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE_ACTIVATE {
		if !s.pending {
			s.pending = true
			return &agentpb.StackKitResult{Success: false, Stderr: "runtime convergence is pending; retry after the Guard reports runtime readiness"}, nil
		}
		s.effects++
		if strings.HasPrefix(s.scenario, "restart-") {
			panic("process-death")
		}
		if s.scenario == "committed-lost-ack" {
			return nil, fmt.Errorf("node result acknowledgement unavailable")
		}
		if s.scenario != "activated" {
			return &agentpb.StackKitResult{Success: false, ExitCode: 23, Release: c.Release, CommandResultJson: []byte(`{"schemaVersion":"stackkit.command-result/v1","command":"stackkit backup restore activate","status":"failed"}`)}, nil
		}
	}
	if s.scenario == "missing-journal" {
		return nil, fmt.Errorf("original journal unavailable")
	}
	if s.scenario == "resume-original" && c.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RESTORE_RECOVER {
		return nil, fmt.Errorf("fresh planning/activation is unavailable after controller restart")
	}
	status := "activated"
	if s.scenario == "recovered" || s.scenario == "explicit-recover" {
		status = "recovered"
	}
	restoreID := c.RestoreResultId
	if s.scenario == "foreign-result" {
		restoreID = "sha256:" + strings.Repeat("b", 64)
	}
	data := map[string]any{
		"apiVersion": "stackkit.restore-activation-result/v1", "operationId": c.ActivationOperationId, "restoreResultId": restoreID, "safetySnapshotId": "sha256:" + strings.Repeat("d", 64), "planHash": c.ExpectedPlanHash, "managedVolumeSetHash": "sha256:" + strings.Repeat("e", 64), "status": status,
		"verification": map[string]any{"apiVersion": "stackkit.local-backup-restore-verification/v1", "ownerRef": "owner/node", "ownerBindingDigest": "sha256:" + strings.Repeat("f", 64), "pocketIdSubject": "owner-subject", "planHash": c.ExpectedPlanHash, "servicesVerified": true, "verifiedAt": time.Now().UTC()},
		"signature":    map[string]any{"ownerRef": "owner/node", "keyId": "node-key", "value": "node-verified-signature"}}
	if original := c.OriginalRecoveryRelease; original != nil {
		data["originalRecoveryRelease"] = map[string]any{"version": original.Version, "platform": map[string]any{"os": original.PlatformOs, "arch": original.PlatformArch}, "archiveSha256": original.ArchiveSha256, "releaseIndexSha256": original.ReleaseIndexSha256}
	}
	body, _ := json.Marshal(map[string]any{"schemaVersion": stackkitcommand.CommandResultVersion, "command": stackkitcommand.ResultCommandName(c.Operation), "status": "success", "data": data})
	return &agentpb.StackKitResult{CommandId: c.CommandId, Success: true, Release: c.Release, CommandResultJson: body, PinnedCliEvidenceVerified: true, PinnedCliEvidenceSha256: fmt.Sprintf("sha256:%x", sha256.Sum256(body))}, nil
}
