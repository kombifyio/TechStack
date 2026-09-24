package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/runtimeaction"
	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/secrets"
)

type fakeRuntimeDiagnosticsCollector struct {
	requests []RuntimeDiagnosticsRequest
	bundle   *RuntimeDiagnosticsBundle
	err      error
}

func (f *fakeRuntimeDiagnosticsCollector) CollectRuntimeDiagnostics(_ context.Context, req RuntimeDiagnosticsRequest) (*RuntimeDiagnosticsBundle, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return nil, f.err
	}
	if f.bundle != nil {
		return f.bundle, nil
	}
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	return &RuntimeDiagnosticsBundle{
		Status:  "collected",
		Reason:  req.Reason,
		Action:  req.Action,
		Binding: runtimeDiagnosticsBinding(req),
		Target:  runtimeDiagnosticsTargetMap(req.RuntimeTarget),
		Commands: []RuntimeDiagnosticsCommand{
			{Name: "docker_ps", Command: "docker ps -a", Output: "coolify healthy", DurationMS: 12},
		},
		StartedAt:   now,
		CompletedAt: now.Add(20 * time.Millisecond),
		DurationMS:  20,
	}, nil
}

type fakeRuntimeTargetBootstrapper struct {
	calls  []*RuntimeActionTarget
	result *RuntimeTargetBootstrapResult
	err    error
	order  *[]string
}

func (f *fakeRuntimeTargetBootstrapper) BootstrapRuntimeTarget(_ context.Context, target *RuntimeActionTarget) (*RuntimeTargetBootstrapResult, error) {
	f.calls = append(f.calls, target)
	if f.order != nil {
		*f.order = append(*f.order, "bootstrap")
	}
	if f.err != nil {
		return f.result, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return &RuntimeTargetBootstrapResult{Status: "ready", Message: "test bootstrap ready", DurationMS: 12}, nil
}

type blockingRuntimeActionRunner struct {
	unblock chan struct{}
}

type fakeStackKitPrepRunner struct {
	calls  []RuntimeActionRequest
	order  *[]string
	result *RuntimeTargetBootstrapResult
	err    error
}

func (f *fakeStackKitPrepRunner) PrepareStackKitRuntimeTarget(_ context.Context, req RuntimeActionRequest) (*RuntimeTargetBootstrapResult, error) {
	f.calls = append(f.calls, req)
	if f.order != nil {
		*f.order = append(*f.order, "prepare")
	}
	if f.result != nil || f.err != nil {
		return f.result, f.err
	}
	return &RuntimeTargetBootstrapResult{
		Status:     "ready",
		ReasonCode: RuntimeTargetBootstrapReady,
		Message:    "StackKits CLI prepare completed",
	}, nil
}

func (b blockingRuntimeActionRunner) Run(ctx context.Context, req RuntimeActionRequest) error {
	_, err := b.RunWithResult(ctx, req)
	return err
}

func (b blockingRuntimeActionRunner) RunWithResult(ctx context.Context, req RuntimeActionRequest) (map[string]interface{}, error) {
	<-b.unblock
	return nil, ctx.Err()
}

func managedDeployRolloutFixture(t *testing.T, stackID, stackKit string, actions RuntimeActions) *deployRollout {
	t.Helper()
	job := &Job{
		ID:       "job-" + stackID,
		Type:     JobTypeDeploy,
		TargetID: stackID,
		Payload:  map[string]interface{}{},
		Result:   map[string]interface{}{},
	}
	return &deployRollout{
		cfg:            &ProvisionConfig{RuntimeActions: actions},
		job:            job,
		q:              &Queue{jobs: map[string]*Job{job.ID: job}},
		managedRuntime: true,
		targetKind:     "cloud",
		unifiedSpec:    &core.UnifiedSpec{StackKit: stackKit},
		actionReq: RuntimeActionRequest{
			StackID:  stackID,
			StackKit: stackKit,
			RuntimeTarget: &RuntimeActionTarget{
				Host:       "203.0.113.10",
				User:       "root",
				Port:       22,
				PrivateKey: "test-private-key",
			},
		},
		runtimeProof: map[string]interface{}{},
		e2eProof:     map[string]any{"phases_completed": []string{}},
	}
}

func TestDeployRolloutBootstrapsManagedRuntimeTargetBeforeStackKits(t *testing.T) {
	order := []string{}
	bootstrapper := &fakeRuntimeTargetBootstrapper{order: &order}
	rolloutRunner := &fakeRuntimeRunner{name: "rollout", order: &order, result: map[string]interface{}{"status": "applied"}}
	rollout := managedDeployRolloutFixture(t, "stack-runtime-bootstrap", "basement-kit", RuntimeActions{
		TargetBootstrapper: bootstrapper,
		RolloutRunner:      rolloutRunner,
	})

	if err := rollout.runRollout(context.Background()); err != nil {
		t.Fatalf("runRollout: %v", err)
	}
	if len(bootstrapper.calls) != 1 {
		t.Fatalf("bootstrap calls = %d, want 1", len(bootstrapper.calls))
	}
	if len(rolloutRunner.calls) != 1 {
		t.Fatalf("rollout calls = %d, want 1", len(rolloutRunner.calls))
	}
	if got := strings.Join(order, ","); got != "bootstrap,rollout" {
		t.Fatalf("order = %s, want bootstrap,rollout", got)
	}
	if rollout.e2eProof["target_bootstrap"] != "ready" {
		t.Fatalf("target bootstrap proof = %+v, want ready", rollout.e2eProof["target_bootstrap"])
	}
}

func TestDeployRolloutPreBootstrapsBeforeStackKitPrepare(t *testing.T) {
	order := []string{}
	prepRunner := &fakeStackKitPrepRunner{order: &order}
	bootstrapper := &fakeRuntimeTargetBootstrapper{order: &order}
	rolloutRunner := &fakeRuntimeRunner{name: "rollout", order: &order, result: map[string]interface{}{"status": "applied"}}
	rollout := managedDeployRolloutFixture(t, "stack-stackkit-prepare", "cloud-kit", RuntimeActions{
		StackKitPrepRunner: prepRunner,
		TargetBootstrapper: bootstrapper,
		RolloutRunner:      rolloutRunner,
	})

	if err := rollout.runRollout(context.Background()); err != nil {
		t.Fatalf("runRollout: %v", err)
	}
	if len(prepRunner.calls) != 1 {
		t.Fatalf("prepare calls = %d, want 1", len(prepRunner.calls))
	}
	if len(bootstrapper.calls) != 1 {
		t.Fatalf("pre-bootstrap calls = %d, want 1", len(bootstrapper.calls))
	}
	if len(rolloutRunner.calls) != 1 {
		t.Fatalf("rollout calls = %d, want 1", len(rolloutRunner.calls))
	}
	if got := strings.Join(order, ","); got != "bootstrap,prepare,rollout" {
		t.Fatalf("order = %s, want bootstrap,prepare,rollout", got)
	}
}

func TestDeployRolloutCanDisablePreBootstrapBeforeStackKitPrepare(t *testing.T) {
	t.Setenv("TECHSTACK_STACKKIT_PREP_PREBOOTSTRAP_DISABLED", "1")
	order := []string{}
	prepRunner := &fakeStackKitPrepRunner{order: &order}
	bootstrapper := &fakeRuntimeTargetBootstrapper{order: &order}
	rolloutRunner := &fakeRuntimeRunner{name: "rollout", order: &order, result: map[string]interface{}{"status": "applied"}}
	rollout := managedDeployRolloutFixture(t, "stack-stackkit-prepare-no-prebootstrap", "cloud-kit", RuntimeActions{
		StackKitPrepRunner: prepRunner,
		TargetBootstrapper: bootstrapper,
		RolloutRunner:      rolloutRunner,
	})

	if err := rollout.runRollout(context.Background()); err != nil {
		t.Fatalf("runRollout: %v", err)
	}
	if len(prepRunner.calls) != 1 {
		t.Fatalf("prepare calls = %d, want 1", len(prepRunner.calls))
	}
	if len(bootstrapper.calls) != 0 {
		t.Fatalf("pre-bootstrap calls = %d, want 0 when disabled", len(bootstrapper.calls))
	}
	if len(rolloutRunner.calls) != 1 {
		t.Fatalf("rollout calls = %d, want 1", len(rolloutRunner.calls))
	}
	if got := strings.Join(order, ","); got != "prepare,rollout" {
		t.Fatalf("order = %s, want prepare,rollout", got)
	}
}

func TestDeployRolloutStopsWhenPreBootstrapBeforeStackKitPrepareFails(t *testing.T) {
	order := []string{}
	prepRunner := &fakeStackKitPrepRunner{order: &order}
	bootstrapper := &fakeRuntimeTargetBootstrapper{
		order: &order,
		result: &RuntimeTargetBootstrapResult{
			Status:     "failed",
			ReasonCode: RuntimeTargetBootstrapTimeout,
			Message:    "apt wait timed out",
		},
		err: errors.New("apt wait timed out"),
	}
	rolloutRunner := &fakeRuntimeRunner{name: "rollout", order: &order, result: map[string]interface{}{"status": "applied"}}
	rollout := managedDeployRolloutFixture(t, "stack-stackkit-prepare-prebootstrap-failed", "cloud-kit", RuntimeActions{
		StackKitPrepRunner: prepRunner,
		TargetBootstrapper: bootstrapper,
		RolloutRunner:      rolloutRunner,
	})

	if err := rollout.runRollout(context.Background()); err == nil {
		t.Fatal("runRollout returned nil, want pre-bootstrap error")
	}
	if len(bootstrapper.calls) != 1 {
		t.Fatalf("pre-bootstrap calls = %d, want 1", len(bootstrapper.calls))
	}
	if len(prepRunner.calls) != 0 {
		t.Fatalf("prepare calls = %d, want 0 after pre-bootstrap failure", len(prepRunner.calls))
	}
	if len(rolloutRunner.calls) != 0 {
		t.Fatalf("rollout calls = %d, want 0 after pre-bootstrap failure", len(rolloutRunner.calls))
	}
	if got := strings.Join(order, ","); got != "bootstrap" {
		t.Fatalf("order = %s, want bootstrap", got)
	}
}

func TestDeployRolloutPersistsRedactedDiagnosticsOnTargetBootstrapTimeout(t *testing.T) {
	bootstrapErr := context.DeadlineExceeded
	collector := &fakeRuntimeDiagnosticsCollector{bundle: &RuntimeDiagnosticsBundle{
		Status: "collected",
		Reason: RuntimeTargetBootstrapTimeout,
		Action: "target_bootstrap",
		Commands: []RuntimeDiagnosticsCommand{{
			Name:    "docker_status",
			Command: "systemctl is-active docker",
			Output:  "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
		}},
	}}
	bootstrapper := &fakeRuntimeTargetBootstrapper{
		err: bootstrapErr,
		result: &RuntimeTargetBootstrapResult{
			Status:     "failed",
			ReasonCode: RuntimeTargetBootstrapTimeout,
			Message:    bootstrapErr.Error(),
			Output:     "phase=docker_ready status=wait_begin\nAuthorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
			DurationMS: 1234,
			Attempts:   2,
		},
	}
	rolloutRunner := &fakeRuntimeRunner{name: "rollout", result: map[string]interface{}{"status": "applied"}}
	rollout := managedDeployRolloutFixture(t, "stack-runtime-bootstrap-failed", "basement-kit", RuntimeActions{
		TargetBootstrapper:   bootstrapper,
		RolloutRunner:        rolloutRunner,
		DiagnosticsCollector: collector,
	})
	job := rollout.job
	job.Payload = map[string]interface{}{
		providerField: "ionos",
		tenantIDField: "tenant-runtime-bootstrap",
	}
	job.Result = map[string]interface{}{
		leaseIDField:   "lease-runtime-bootstrap-failed",
		"operation_id": "operation-runtime-bootstrap-failed",
	}
	rollout.actionReq.TenantID = "tenant-runtime-bootstrap"
	rollout.actionReq.RuntimeTarget = &RuntimeActionTarget{
		Host:       "203.0.113.40",
		PublicIP:   "203.0.113.40",
		User:       "ubuntu",
		Port:       22,
		PrivateKey: "-----BEGIN OPENSSH PRIVATE KEY-----\nsecret-key-material\n-----END OPENSSH PRIVATE KEY-----",
	}

	err := rollout.runRollout(context.Background())
	if err == nil {
		t.Fatal("expected bootstrap failure")
	}
	if provisionErr, ok := err.(*ProvisionError); !ok || provisionErr.Step != StepStackKitPrepare {
		t.Fatalf("bootstrap error = %#v, want ProvisionError step %s", err, StepStackKitPrepare)
	}
	if len(rolloutRunner.calls) != 0 {
		t.Fatalf("rollout calls = %d, want none after bootstrap failure", len(rolloutRunner.calls))
	}
	if len(collector.requests) != 1 {
		t.Fatalf("diagnostic requests = %d, want 1", len(collector.requests))
	}
	req := collector.requests[0]
	if req.Action != "target_bootstrap" || req.Reason != RuntimeTargetBootstrapTimeout {
		t.Fatalf("diagnostic action/reason = %q/%q", req.Action, req.Reason)
	}
	if req.JobID != job.ID || req.StackID != job.TargetID || req.TenantID != "tenant-runtime-bootstrap" ||
		req.LeaseID != "lease-runtime-bootstrap-failed" || req.OperationID != "operation-runtime-bootstrap-failed" ||
		req.ServerID != runtimeidentity.LeaseServerID("lease-runtime-bootstrap-failed") ||
		req.RuntimeAgentID != runtimeidentity.LeaseRuntimeAgentID("tenant-runtime-bootstrap", "lease-runtime-bootstrap-failed") || req.Provider != "ionos" {
		t.Fatalf("diagnostic request context = %+v", req)
	}
	proof := mapFromInterface(job.Result["target_bootstrap"])
	if proof["status"] != "failed" || proof["reason_code"] != RuntimeTargetBootstrapTimeout {
		t.Fatalf("target bootstrap proof = %+v", proof)
	}
	if proof["attempts"] != float64(2) && proof["attempts"] != 2 {
		t.Fatalf("target bootstrap attempts = %#v, want 2", proof["attempts"])
	}
	raw, marshalErr := json.Marshal(job.Result)
	if marshalErr != nil {
		t.Fatalf("marshal job result: %v", marshalErr)
	}
	if strings.Contains(string(raw), "secret-key-material") || strings.Contains(string(raw), "eyJhbGci") {
		t.Fatalf("job result leaked secret material: %s", raw)
	}
	if diagnostics := mapFromInterface(job.Result["runtime_diagnostics"]); diagnostics["reason"] != RuntimeTargetBootstrapTimeout || diagnostics["status"] != "collected" {
		t.Fatalf("runtime diagnostics = %+v, want collected timeout diagnostics", diagnostics)
	}
	if !jobLogsContain(job, "Runtime diagnostics collected for target_bootstrap") {
		t.Fatalf("job logs missing target bootstrap diagnostics marker: %+v", job.Logs)
	}
}

func TestDeployRolloutClassifiesPrepTimeoutAsPostLeaseFailure(t *testing.T) {
	prepErr := errors.New("context deadline exceeded")
	prepRunner := &fakeStackKitPrepRunner{
		result: &RuntimeTargetBootstrapResult{
			Status:     "failed",
			ReasonCode: RuntimeTargetBootstrapTimeout,
			Message:    "target_bootstrap_timeout",
			Output: strings.Join([]string{
				"phase=cloud_init status=wait_done",
				"phase=docker_install method=apt",
				"phase=apt_wait status=begin",
				"Runtime diagnostics:",
				"Status: collected",
				"Reason: target_bootstrap_timeout",
				"Commands: 12",
			}, "\n"),
			DurationMS: 900000,
			Attempts:   1,
		},
		err: prepErr,
	}
	collector := &fakeRuntimeDiagnosticsCollector{}
	rolloutRunner := &fakeRuntimeRunner{name: "rollout", result: map[string]interface{}{"status": "applied"}}
	rollout := managedDeployRolloutFixture(t, "stack-centron-apt-wait-timeout", "cloud-kit", RuntimeActions{
		StackKitPrepRunner:   prepRunner,
		RolloutRunner:        rolloutRunner,
		DiagnosticsCollector: collector,
	})
	job := rollout.job
	job.Payload = map[string]interface{}{providerField: "centron"}
	job.Result = map[string]interface{}{leaseIDField: "lease-mwhrsh04v3hl2qo"}
	rollout.actionReq.RuntimeTarget = &RuntimeActionTarget{
		Host:       "188.64.59.141",
		PublicIP:   "188.64.59.141",
		User:       "root",
		Port:       22,
		PrivateKey: "test-private-key",
	}

	err := rollout.runRollout(context.Background())
	if err == nil {
		t.Fatal("expected StackKits prepare timeout failure")
	}
	provisionErr, ok := err.(*ProvisionError)
	if !ok {
		t.Fatalf("error = %#v, want ProvisionError", err)
	}
	if provisionErr.Step != StepStackKitPrepare {
		t.Fatalf("error step = %q, want %q", provisionErr.Step, StepStackKitPrepare)
	}
	if len(rolloutRunner.calls) != 0 {
		t.Fatalf("rollout calls = %d, want none after prep failure", len(rolloutRunner.calls))
	}
	if len(collector.requests) != 1 {
		t.Fatalf("diagnostic requests = %d, want 1", len(collector.requests))
	}
	req := collector.requests[0]
	if req.Action != "target_bootstrap" || req.Reason != RuntimeTargetBootstrapTimeout {
		t.Fatalf("diagnostic action/reason = %q/%q", req.Action, req.Reason)
	}
	if req.LeaseID != "lease-mwhrsh04v3hl2qo" || req.Provider != "centron" {
		t.Fatalf("diagnostic request context = %+v", req)
	}
	proof := mapFromInterface(job.Result["target_bootstrap"])
	if proof["status"] != "failed" || proof["reason_code"] != RuntimeTargetBootstrapTimeout {
		t.Fatalf("target bootstrap proof = %+v", proof)
	}
	if job.Step != StepStackKitPrepare {
		t.Fatalf("job step = %q, want %q", job.Step, StepStackKitPrepare)
	}
}

type failedApplyDiagnosticsSender struct{ recordingStackKitCommandSender }

func (s *failedApplyDiagnosticsSender) SendStackKitCommand(ctx context.Context, agent string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY {
		return &agentpb.StackKitResult{Success: false, ExitCode: 1, Release: command.Release,
			Stderr: "connection reset by peer", CommandResultJson: []byte(`{"schemaVersion":"stackkit.command-result/v1","status":"success","data":{"schemaVersion":"stackkit.apply-result/v2","status":"failed","apply":{},"outcomes":{"schemaVersion":"stackkit.apply-outcome-ledger/v1","overall":"failed","units":[{"ref":"cloud-core","outcome":"failed","failure":{"class":"registry_unreachable","retryable":true,"message":"connection reset by peer password=inline-secret","password":"apply-secret"}}]}}}`)}, nil
	}
	return s.recordingStackKitCommandSender.SendStackKitCommand(ctx, agent, command)
}

func TestDeployRolloutCollectsRuntimeDiagnosticsOnStackKitsFailure(t *testing.T) {
	collector := &fakeRuntimeDiagnosticsCollector{}
	rollout := managedDeployRolloutFixture(t, "stack-runtime-diagnostics", "basement-kit", RuntimeActions{
		StackKitPrepRunner:   &fakeStackKitPrepRunner{},
		DiagnosticsCollector: collector,
	})
	// Exercise the production typed dispatcher while keeping its artifact input local.
	dir := t.TempDir()
	binary := filepath.Join(dir, "stackkit")
	if err := os.WriteFile(binary, []byte("test artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("test artifact")))
	pin, err := json.Marshal(stackkitrelease.Pin{SchemaVersion: stackkitrelease.PinSchemaVersion, Kit: "basement-kit", Version: "v0.24.58", Platform: stackkitrelease.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ArchiveSHA256: digest, IndexSHA256: digest, BinarySHA256: digest, BinaryPath: binary})
	if err != nil {
		t.Fatal(err)
	}
	pinPath := filepath.Join(dir, "pin.json")
	if err := os.WriteFile(pinPath, pin, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(stackKitReleasePinEnv, pinPath)
	t.Setenv(stackKitReleaseCacheEnv, dir)
	rollout.cfg.StackKitCommander = &failedApplyDiagnosticsSender{}
	rollout.actionReq.TenantID, rollout.actionReq.OwnerID = "tenant-1", "owner-1"
	rollout.actionReq.TechStackEnrollment = &TechStackEnrollment{RuntimeAgentID: "agent-1"}
	rollout.actionReq.StackSpecPath = writeSpec(t, `{"apiVersion":"stackkit/v2alpha1","kind":"StackSpec","metadata":{"name":"diagnostics"},"kit":{"slug":"basement-kit"},"workloads":{"core":{"alternative":"standalone"}},"generation":{"outputRoot":"deploy"}}`)
	job := rollout.job
	job.Payload = map[string]interface{}{providerField: "ionos"}
	job.Result = map[string]interface{}{leaseIDField: "lease-runtime-diagnostics"}
	rollout.actionReq.RuntimeTarget = &RuntimeActionTarget{
		Host:       "213.165.73.109",
		PublicIP:   "213.165.73.109",
		User:       "ubuntu",
		Port:       22,
		PrivateKey: "-----BEGIN OPENSSH PRIVATE KEY-----\nsecret-key-material\n-----END OPENSSH PRIVATE KEY-----",
	}

	err = rollout.runRollout(context.Background())
	var operationErr *typedStackKitOperationError
	if !errors.As(err, &operationErr) || operationErr.Operation != StackKitLifecycleApply || operationErr.TimedOut() {
		t.Fatalf("expected original non-timeout typed Apply failure, got %v", err)
	}
	if len(collector.requests) == 0 {
		t.Fatalf("typed Apply failure lost runtime diagnostics: %v", err)
	}
	req := collector.requests[0]
	if req.Action != StepRolloutRunner || req.Reason != "typed_stackkit_apply_failed" {
		t.Fatalf("diagnostic request action/reason = %q/%q", req.Action, req.Reason)
	}
	if req.JobID != job.ID || req.StackID != job.TargetID || req.LeaseID != "lease-runtime-diagnostics" || req.Provider != "ionos" {
		t.Fatalf("diagnostic request context = %+v", req)
	}
	diagnostics := mapFromInterface(job.Result["runtime_diagnostics"])
	if diagnostics["status"] != "collected" {
		t.Fatalf("runtime diagnostics = %+v, want collected", diagnostics)
	}
	target := mapFromInterface(diagnostics["target"])
	if target["host"] != "213.165.73.109" || target["private_key"] != nil || target["password"] != nil {
		t.Fatalf("diagnostic target leaked or lost fields: %+v", target)
	}
	proof := mapFromInterface(mapFromInterface(job.Result["runtime_proof"])["rollout"])
	if runtimeActionProofStatus(proof) != "failed" || rollout.e2eProof["rollout_result"] != "failed" || mapFromInterface(proof["outcomes"])["overall"] != "failed" {
		t.Fatalf("failed typed Apply proof was lost: %+v", proof)
	}
	units, _ := mapFromInterface(proof["outcomes"])["units"].([]interface{})
	if len(units) == 0 || mapFromInterface(mapFromInterface(units[0])["failure"])["class"] != "registry_unreachable" {
		t.Fatalf("per-unit failure evidence was lost: %+v", proof)
	}
	raw, marshalErr := json.Marshal(job.Result)
	if marshalErr != nil {
		t.Fatalf("marshal diagnostics: %v", marshalErr)
	}
	if strings.Contains(string(raw), "secret-key-material") || strings.Contains(string(raw), "apply-secret") || strings.Contains(string(raw), "inline-secret") {
		t.Fatalf("diagnostics leaked private key: %s", raw)
	}
	if !jobLogsContain(job, "Runtime diagnostics collected for stackkit_rollout") {
		t.Fatalf("job logs missing diagnostics marker: %+v", job.Logs)
	}
}

func TestDeployRolloutRestoreFailureKeepsRolloutProofAndCollectsDiagnostics(t *testing.T) {
	oldDelays := stackKitsRestoreReadinessRetryDelays
	stackKitsRestoreReadinessRetryDelays = nil
	defer func() { stackKitsRestoreReadinessRetryDelays = oldDelays }()

	collector := &fakeRuntimeDiagnosticsCollector{}
	job := &Job{
		ID:       "job-restore-diagnostics",
		Type:     JobTypeDeploy,
		TargetID: "stack-restore-diagnostics",
		Payload: map[string]interface{}{
			providerField: "ionos",
		},
		Result: map[string]interface{}{
			leaseIDField: "lease-restore-diagnostics",
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}
	rollout := &deployRollout{
		cfg: &ProvisionConfig{RuntimeActions: RuntimeActions{
			RolloutRunner:        &fakeRuntimeRunner{name: "rollout", result: map[string]interface{}{"status": "applied"}},
			RestoreDrill:         &fakeRuntimeRunner{name: "restore", err: errors.New(`runtime action restore_drill returned 502: {"error":{"details":{"error":"no running Docker containers"}}}`)},
			DiagnosticsCollector: collector,
		}},
		job:            job,
		q:              queue,
		managedRuntime: false,
		targetKind:     "cloud",
		unifiedSpec:    &core.UnifiedSpec{StackKit: "cloud-kit"},
		actionReq: RuntimeActionRequest{
			StackID:  job.TargetID,
			StackKit: "cloud-kit",
			RuntimeTarget: &RuntimeActionTarget{
				Host:       "203.0.113.40",
				PublicIP:   "203.0.113.40",
				User:       "root",
				Port:       22,
				PrivateKey: "test-key",
			},
		},
		e2eProof: map[string]any{
			"phases_completed": []string{},
			"rollout_result":   string(JobStatePending),
			"restore_result":   string(JobStatePending),
		},
		runtimeProof:      map[string]interface{}{},
		stackKitOutputs:   map[string]interface{}{},
		runtimeMetrics:    map[string]string{},
		finalRuntimePhase: RuntimePhaseDeployed,
	}

	if err := rollout.runRollout(context.Background()); err != nil {
		t.Fatalf("runRollout failed: %v", err)
	}
	if err := rollout.runRestoreDrill(context.Background()); err == nil {
		t.Fatal("expected restore drill failure")
	}

	if len(collector.requests) != 1 {
		t.Fatalf("diagnostic requests = %d, want 1", len(collector.requests))
	}
	req := collector.requests[0]
	if req.Action != StepRestoreDrill || req.Reason != "runtime_action_failed" {
		t.Fatalf("diagnostic request action/reason = %q/%q, want restore_drill/runtime_action_failed", req.Action, req.Reason)
	}
	runtimeProof := mapFromInterface(job.Result["runtime_proof"])
	rolloutProof := mapFromInterface(runtimeProof["rollout"])
	if rolloutProof["status"] != "applied" {
		t.Fatalf("runtime_proof.rollout = %+v, want applied after later restore failure", rolloutProof)
	}
	e2eProof := mapFromInterface(job.Result["e2e_proof"])
	if e2eProof["rollout_result"] != "applied" {
		t.Fatalf("e2e_proof = %+v, want rollout_result applied", e2eProof)
	}
	diagnostics := mapFromInterface(job.Result["runtime_diagnostics"])
	if diagnostics["action"] != StepRestoreDrill || diagnostics["status"] != "collected" {
		t.Fatalf("runtime diagnostics = %+v, want collected restore diagnostics", diagnostics)
	}
	if !jobLogsContain(job, "Runtime diagnostics collected for restore_drill") {
		t.Fatalf("job logs missing restore diagnostics marker: %+v", job.Logs)
	}
}

func TestDeployRolloutTimesOutStuckRuntimeAction(t *testing.T) {
	collector := &fakeRuntimeDiagnosticsCollector{}
	unblock := make(chan struct{})
	defer close(unblock)

	rollout := managedDeployRolloutFixture(t, "stack-runtime-action-timeout", "basement-kit", RuntimeActions{
		RolloutRunner:        blockingRuntimeActionRunner{unblock: unblock},
		DiagnosticsCollector: collector,
	})
	job := rollout.job
	job.Payload = map[string]interface{}{providerField: "ionos"}
	job.Result = map[string]interface{}{leaseIDField: "lease-runtime-action-timeout"}
	rollout.cfg.RuntimeActionTimeout = 20 * time.Millisecond
	rollout.runtimeProof = nil
	rollout.actionReq.Action = runtimeaction.ActionStackKitRollout
	rollout.actionReq.RuntimeTarget = &RuntimeActionTarget{
		Host:     "203.0.113.20",
		PublicIP: "203.0.113.20",
		User:     "ubuntu",
		Port:     22,
		Password: "secret",
	}

	started := time.Now()
	err := rollout.runRollout(context.Background())
	if err == nil {
		t.Fatal("expected rollout timeout")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("rollout took %s, want bounded timeout", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want wrapped runtime action timeout", err)
	}
	if len(collector.requests) != 1 {
		t.Fatalf("diagnostic requests = %d, want 1", len(collector.requests))
	}
	req := collector.requests[0]
	if req.Reason != "runtime_action_timeout" || req.Action != StepRolloutRunner {
		t.Fatalf("diagnostic action/reason = %q/%q, want stackkit_rollout/runtime_action_timeout", req.Action, req.Reason)
	}
	diagnostics := mapFromInterface(job.Result["runtime_diagnostics"])
	if diagnostics["reason"] != "runtime_action_timeout" {
		t.Fatalf("runtime diagnostics = %+v, want timeout reason", diagnostics)
	}
}

func TestSSHRuntimeDiagnosticsCollectorSkipsMissingCredential(t *testing.T) {
	collector := NewSSHRuntimeDiagnosticsCollector(SSHRuntimeDiagnosticsCollectorConfig{})
	bundle, err := collector.CollectRuntimeDiagnostics(context.Background(), RuntimeDiagnosticsRequest{
		Action: "stackkit_rollout",
		Reason: "runtime_action_failed",
		RuntimeTarget: &RuntimeActionTarget{
			Host: "203.0.113.10",
			User: "ubuntu",
			Port: 22,
		},
	})
	if err != nil {
		t.Fatalf("CollectRuntimeDiagnostics returned error: %v", err)
	}
	if bundle == nil || bundle.Status != "skipped" {
		t.Fatalf("bundle = %+v, want skipped", bundle)
	}
	if bundle.Error == "" {
		t.Fatal("skipped diagnostics omitted their operator-facing reason")
	}
}

// This white-box test protects the provider-control secret boundary: cloud-init
// userData may be hashed for correlation but must never be copied into evidence.
func TestBootstrapDiagnosticsNeverReadRawUserData(t *testing.T) {
	commands := runtimeDiagnosticsCommands("managed_runtime_enrollment")
	foundDigest := false
	for _, command := range commands {
		if !strings.Contains(command.command, "user-data") {
			continue
		}
		foundDigest = true
		if command.name != "cloud_init_digest" || !strings.Contains(command.command, "sha256sum") || strings.Contains(command.command, "cat ") {
			t.Fatalf("unsafe cloud-init diagnostic command: %+v", command)
		}
	}
	if !foundDigest {
		t.Fatal("bootstrap diagnostics omitted the cloud-init correlation digest")
	}
}

func TestRuntimeDiagnosticsSSHAuthMethodsIgnoresProviderLocalKeyPathWhenPasswordExists(t *testing.T) {
	methods, err := runtimeDiagnosticsSSHAuthMethods(&RuntimeActionTarget{
		Host:     "203.0.113.10",
		User:     "ubuntu",
		Port:     22,
		KeyPath:  "/data/ionos-ssh-keys/missing-provider-local.pem",
		Password: "secret",
	})
	if err != nil {
		t.Fatalf("runtimeDiagnosticsSSHAuthMethods: %v", err)
	}
	if len(methods) != 1 {
		t.Fatalf("auth methods = %d, want password method only", len(methods))
	}
}

func TestRuntimeDiagnosticsRedactsAndTruncatesOutput(t *testing.T) {
	output := "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9\n" + strings.Repeat("x", 64)
	redacted := truncateRuntimeDiagnosticsOutput(secrets.Redact(output), 80)
	if strings.Contains(redacted, "eyJhbGci") {
		t.Fatalf("expected bearer token redacted before truncation, got %q", redacted)
	}
	if !strings.Contains(redacted, "[truncated]") {
		t.Fatalf("expected truncation marker, got %q", redacted)
	}
}

func jobLogsContain(job *Job, needle string) bool {
	for _, entry := range job.Logs {
		if strings.Contains(entry.Message, needle) {
			return true
		}
	}
	return false
}
