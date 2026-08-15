package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/runtimeaction"
	"github.com/kombifyio/techstack/pkg/core"
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
		Status: "collected",
		Reason: req.Reason,
		Action: req.Action,
		Target: runtimeDiagnosticsTargetMap(req.RuntimeTarget),
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

func TestDeployRolloutBootstrapsManagedRuntimeTargetBeforeStackKits(t *testing.T) {
	order := []string{}
	bootstrapper := &fakeRuntimeTargetBootstrapper{order: &order}
	rolloutRunner := &fakeRuntimeRunner{name: "rollout", order: &order, result: map[string]interface{}{"status": "applied"}}
	job := &Job{
		ID:       "job-runtime-bootstrap",
		Type:     JobTypeDeploy,
		TargetID: "stack-runtime-bootstrap",
		Payload:  map[string]interface{}{},
		Result:   map[string]interface{}{},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}
	rollout := &deployRollout{
		cfg: &ProvisionConfig{RuntimeActions: RuntimeActions{
			TargetBootstrapper: bootstrapper,
			RolloutRunner:      rolloutRunner,
		}},
		job:            job,
		q:              queue,
		managedRuntime: true,
		targetKind:     "cloud",
		unifiedSpec:    &core.UnifiedSpec{StackKit: "basement-kit"},
		actionReq: RuntimeActionRequest{
			StackID:  job.TargetID,
			StackKit: "basement-kit",
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
	job := &Job{
		ID:       "job-stackkit-prepare",
		Type:     JobTypeDeploy,
		TargetID: "stack-stackkit-prepare",
		Payload:  map[string]interface{}{},
		Result:   map[string]interface{}{},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}
	rollout := &deployRollout{
		cfg: &ProvisionConfig{RuntimeActions: RuntimeActions{
			StackKitPrepRunner: prepRunner,
			TargetBootstrapper: bootstrapper,
			RolloutRunner:      rolloutRunner,
		}},
		job:            job,
		q:              queue,
		managedRuntime: true,
		targetKind:     "cloud",
		unifiedSpec:    &core.UnifiedSpec{StackKit: "cloud-kit"},
		actionReq: RuntimeActionRequest{
			StackID:  job.TargetID,
			StackKit: "cloud-kit",
			RuntimeTarget: &RuntimeActionTarget{
				Host:       "203.0.113.20",
				User:       "root",
				Port:       22,
				PrivateKey: "test-private-key",
			},
		},
		runtimeProof: map[string]interface{}{},
		e2eProof:     map[string]any{"phases_completed": []string{}},
	}

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
	job := &Job{
		ID:       "job-stackkit-prepare-no-prebootstrap",
		Type:     JobTypeDeploy,
		TargetID: "stack-stackkit-prepare-no-prebootstrap",
		Payload:  map[string]interface{}{},
		Result:   map[string]interface{}{},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}
	rollout := &deployRollout{
		cfg: &ProvisionConfig{RuntimeActions: RuntimeActions{
			StackKitPrepRunner: prepRunner,
			TargetBootstrapper: bootstrapper,
			RolloutRunner:      rolloutRunner,
		}},
		job:            job,
		q:              queue,
		managedRuntime: true,
		targetKind:     "cloud",
		unifiedSpec:    &core.UnifiedSpec{StackKit: "cloud-kit"},
		actionReq: RuntimeActionRequest{
			StackID:  job.TargetID,
			StackKit: "cloud-kit",
			RuntimeTarget: &RuntimeActionTarget{
				Host:       "203.0.113.21",
				User:       "root",
				Port:       22,
				PrivateKey: "test-private-key",
			},
		},
		runtimeProof: map[string]interface{}{},
		e2eProof:     map[string]any{"phases_completed": []string{}},
	}

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
	job := &Job{
		ID:       "job-stackkit-prepare-prebootstrap-failed",
		Type:     JobTypeDeploy,
		TargetID: "stack-stackkit-prepare-prebootstrap-failed",
		Payload:  map[string]interface{}{},
		Result:   map[string]interface{}{},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}
	rollout := &deployRollout{
		cfg: &ProvisionConfig{RuntimeActions: RuntimeActions{
			StackKitPrepRunner: prepRunner,
			TargetBootstrapper: bootstrapper,
			RolloutRunner:      rolloutRunner,
		}},
		job:            job,
		q:              queue,
		managedRuntime: true,
		targetKind:     "cloud",
		unifiedSpec:    &core.UnifiedSpec{StackKit: "cloud-kit"},
		actionReq: RuntimeActionRequest{
			StackID:  job.TargetID,
			StackKit: "cloud-kit",
			RuntimeTarget: &RuntimeActionTarget{
				Host:       "203.0.113.22",
				User:       "root",
				Port:       22,
				PrivateKey: "test-private-key",
			},
		},
		runtimeProof: map[string]interface{}{},
		e2eProof:     map[string]any{"phases_completed": []string{}},
	}

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
	job := &Job{
		ID:       "job-runtime-bootstrap-failed",
		Type:     JobTypeDeploy,
		TargetID: "stack-runtime-bootstrap-failed",
		Payload: map[string]interface{}{
			providerField: "ionos",
		},
		Result: map[string]interface{}{
			leaseIDField: "lease-runtime-bootstrap-failed",
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}
	rollout := &deployRollout{
		cfg: &ProvisionConfig{RuntimeActions: RuntimeActions{
			TargetBootstrapper:   bootstrapper,
			RolloutRunner:        rolloutRunner,
			DiagnosticsCollector: collector,
		}},
		job:            job,
		q:              queue,
		managedRuntime: true,
		targetKind:     "cloud",
		unifiedSpec:    &core.UnifiedSpec{StackKit: "basement-kit"},
		actionReq: RuntimeActionRequest{
			StackID:  job.TargetID,
			StackKit: "basement-kit",
			RuntimeTarget: &RuntimeActionTarget{
				Host:       "203.0.113.40",
				PublicIP:   "203.0.113.40",
				User:       "ubuntu",
				Port:       22,
				PrivateKey: "-----BEGIN OPENSSH PRIVATE KEY-----\nsecret-key-material\n-----END OPENSSH PRIVATE KEY-----",
			},
		},
		runtimeProof: map[string]interface{}{},
		e2eProof:     map[string]any{"phases_completed": []string{}},
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
	if req.JobID != job.ID || req.StackID != job.TargetID || req.LeaseID != "lease-runtime-bootstrap-failed" || req.Provider != "ionos" {
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
	job := &Job{
		ID:       "job-centron-apt-wait-timeout",
		Type:     JobTypeDeploy,
		TargetID: "stack-centron-apt-wait-timeout",
		Payload: map[string]interface{}{
			providerField: "centron",
		},
		Result: map[string]interface{}{
			leaseIDField: "lease-mwhrsh04v3hl2qo",
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}
	rollout := &deployRollout{
		cfg: &ProvisionConfig{RuntimeActions: RuntimeActions{
			StackKitPrepRunner:   prepRunner,
			RolloutRunner:        rolloutRunner,
			DiagnosticsCollector: collector,
		}},
		job:            job,
		q:              queue,
		managedRuntime: true,
		targetKind:     "cloud",
		unifiedSpec:    &core.UnifiedSpec{StackKit: "cloud-kit"},
		actionReq: RuntimeActionRequest{
			StackID:  job.TargetID,
			StackKit: "cloud-kit",
			RuntimeTarget: &RuntimeActionTarget{
				Host:       "188.64.59.141",
				PublicIP:   "188.64.59.141",
				User:       "root",
				Port:       22,
				PrivateKey: "test-private-key",
			},
		},
		runtimeProof: map[string]interface{}{},
		e2eProof:     map[string]any{"phases_completed": []string{}},
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

func TestDeployRolloutCollectsRuntimeDiagnosticsOnStackKitsFailure(t *testing.T) {
	rolloutErr := errors.New("runtime action stackkit_rollout request failed: context deadline exceeded")
	collector := &fakeRuntimeDiagnosticsCollector{}
	job := &Job{
		ID:       "job-runtime-diagnostics",
		Type:     JobTypeDeploy,
		TargetID: "stack-runtime-diagnostics",
		Payload: map[string]interface{}{
			providerField: "ionos",
		},
		Result: map[string]interface{}{
			leaseIDField: "lease-runtime-diagnostics",
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}
	rollout := &deployRollout{
		cfg: &ProvisionConfig{RuntimeActions: RuntimeActions{
			RolloutRunner:        &fakeRuntimeRunner{name: "rollout", err: rolloutErr},
			DiagnosticsCollector: collector,
		}},
		job:            job,
		q:              queue,
		managedRuntime: true,
		targetKind:     "cloud",
		unifiedSpec:    &core.UnifiedSpec{StackKit: "basement-kit"},
		actionReq: RuntimeActionRequest{
			StackID:  job.TargetID,
			StackKit: "basement-kit",
			RuntimeTarget: &RuntimeActionTarget{
				Host:       "213.165.73.109",
				PublicIP:   "213.165.73.109",
				User:       "ubuntu",
				Port:       22,
				PrivateKey: "-----BEGIN OPENSSH PRIVATE KEY-----\nsecret-key-material\n-----END OPENSSH PRIVATE KEY-----",
			},
		},
		e2eProof: map[string]any{"phases_completed": []string{}},
	}

	err := rollout.runRollout(context.Background())
	if err == nil {
		t.Fatal("expected rollout failure")
	}
	if len(collector.requests) != 1 {
		t.Fatalf("diagnostic requests = %d, want 1", len(collector.requests))
	}
	req := collector.requests[0]
	if req.Action != StepRolloutRunner || req.Reason != "runtime_action_failed" {
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
	raw, marshalErr := json.Marshal(diagnostics)
	if marshalErr != nil {
		t.Fatalf("marshal diagnostics: %v", marshalErr)
	}
	if strings.Contains(string(raw), "secret-key-material") {
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
		managedRuntime: true,
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

	job := &Job{
		ID:       "job-runtime-action-timeout",
		Type:     JobTypeDeploy,
		TargetID: "stack-runtime-action-timeout",
		Payload: map[string]interface{}{
			providerField: "ionos",
		},
		Result: map[string]interface{}{
			leaseIDField: "lease-runtime-action-timeout",
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}
	rollout := &deployRollout{
		cfg: &ProvisionConfig{
			RuntimeActionTimeout: 20 * time.Millisecond,
			RuntimeActions: RuntimeActions{
				RolloutRunner:        blockingRuntimeActionRunner{unblock: unblock},
				DiagnosticsCollector: collector,
			},
		},
		job:            job,
		q:              queue,
		managedRuntime: true,
		targetKind:     "cloud",
		unifiedSpec:    &core.UnifiedSpec{StackKit: "basement-kit"},
		actionReq: RuntimeActionRequest{
			Action:   runtimeaction.ActionStackKitRollout,
			StackID:  job.TargetID,
			StackKit: "basement-kit",
			RuntimeTarget: &RuntimeActionTarget{
				Host:     "203.0.113.20",
				PublicIP: "203.0.113.20",
				User:     "ubuntu",
				Port:     22,
				Password: "secret",
			},
		},
		e2eProof: map[string]any{"phases_completed": []string{}},
	}

	started := time.Now()
	err := rollout.runRollout(context.Background())
	if err == nil {
		t.Fatal("expected rollout timeout")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("rollout took %s, want bounded timeout", elapsed)
	}
	if !strings.Contains(err.Error(), "timed out after 20ms") {
		t.Fatalf("error = %q, want timeout detail", err.Error())
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
	if !strings.Contains(bundle.Error, "no SSH credential") {
		t.Fatalf("bundle error = %q, want missing credential", bundle.Error)
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
