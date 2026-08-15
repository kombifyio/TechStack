// Package jobs provides async job processing for kombifyTechstack.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kombifyio/go-common/identity"
	"github.com/kombifyio/go-common/servicecall"
	"github.com/kombifyio/techstack/internal/runtimeproduct/runtimeaction"
	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/unifier"
	"github.com/kombifyio/techstack/pkg/workerauth"
)

// Managed rollout tests exercise the release path and therefore provide the
// signing dependency that production now requires. Individual failure tests
// explicitly clear it with t.Setenv.
func TestMain(m *testing.M) {
	previous, hadPrevious := os.LookupEnv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET")
	_ = os.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "jobs-test-worker-agent-secret")
	code := m.Run()
	if hadPrevious {
		_ = os.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", previous)
	} else {
		_ = os.Unsetenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET")
	}
	os.Exit(code)
}

func allowAutoDeployAdmissionForTest(context.Context, AutoDeployAdmissionRequest) error {
	return nil
}

type fakeManagedLeaseManager struct {
	requests        []ManagedLeaseRequest
	metadataUpdates []fakeManagedLeaseMetadataUpdate
	result          *ManagedLeaseResult
	err             error
	metadataErr     error
}

type fakeManagedLeaseDecommissioner struct {
	requests []ManagedLeaseDecommissionRequest
	result   *ManagedLeaseDecommissionResult
	err      error
}

type fakeManagedLeaseMetadataUpdate struct {
	TenantID string
	LeaseID  string
	Metadata map[string]string
}

type fakeManagedRuntimeTargetResolver struct {
	requests []ManagedRuntimeTargetRequest
	target   *ManagedRuntimeTarget
	err      error
}

type sequenceManagedRuntimeTargetResolver struct {
	requests []ManagedRuntimeTargetRequest
	targets  []*ManagedRuntimeTarget
	errs     []error
}

type blockingManagedRuntimeTargetResolver struct {
	requests []ManagedRuntimeTargetRequest
}

type fakeRuntimeActionRunner struct {
	calls []RuntimeActionRequest
	err   error
}

func (f *fakeRuntimeActionRunner) Run(_ context.Context, req RuntimeActionRequest) error {
	f.calls = append(f.calls, req)
	return f.err
}

type fakeRuntimeRunner struct {
	name       string
	order      *[]string
	calls      []RuntimeActionRequest
	identities []*identity.Identity
	result     map[string]interface{}
	err        error
}

type contextDeadlineRuntimeRunner struct {
	calls []RuntimeActionRequest
}

func (f *contextDeadlineRuntimeRunner) Run(ctx context.Context, req RuntimeActionRequest) error {
	_, err := f.RunWithResult(ctx, req)
	return err
}

func (f *contextDeadlineRuntimeRunner) RunWithResult(ctx context.Context, req RuntimeActionRequest) (map[string]interface{}, error) {
	f.calls = append(f.calls, req)
	<-ctx.Done()
	return nil, ctx.Err()
}

type sequenceRuntimeRunner struct {
	name    string
	order   *[]string
	calls   []RuntimeActionRequest
	results []map[string]interface{}
	errs    []error
}

type fakeStackKitArtifactGenerator struct {
	requests []StackKitArtifactGenerateRequest
	err      error
}

type kombifyMeOutageStackKitGenerator struct {
	requests []StackKitArtifactGenerateRequest
	specs    []string
	errText  string
}

func (f *fakeStackKitArtifactGenerator) GenerateStackKitArtifacts(_ context.Context, req StackKitArtifactGenerateRequest) (*StackKitArtifactGenerateResult, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return nil, f.err
	}
	if err := os.MkdirAll(req.OutputDir, 0750); err != nil {
		return nil, err
	}
	tfvars := []byte("{\n  \"domain\": \"kombify.me\",\n  \"enable_coolify\": true\n}\n")
	if err := os.WriteFile(filepath.Join(req.OutputDir, "terraform.tfvars.json"), tfvars, 0600); err != nil {
		return nil, err
	}
	resolvedPlanDir := filepath.Join(req.OutputDir, ".stackkit")
	if err := os.MkdirAll(resolvedPlanDir, 0750); err != nil {
		return nil, err
	}
	resolvedPlanPath := filepath.Join(resolvedPlanDir, "resolved-plan.json")
	resolvedPlan := []byte(fmt.Sprintf(
		`{"apiVersion":"stackkit.resolved-plan/v1","kind":"ResolvedPlan","stackId":%q,"planHash":"sha256:%s","kit":{"slug":%q},"network":{"runtimeListeners":[]}}`,
		req.StackID,
		strings.Repeat("a", 64),
		req.StackKit,
	))
	if err := os.WriteFile(resolvedPlanPath, resolvedPlan, 0600); err != nil {
		return nil, err
	}
	return &StackKitArtifactGenerateResult{
		StackSpecPath:    req.StackSpecPath,
		OutputDir:        req.OutputDir,
		ResolvedPlanPath: resolvedPlanPath,
		Metadata: map[string]string{
			"artifact_generator": "fake-stackkit-cli",
			"resolved_plan_hash": "sha256:" + strings.Repeat("a", 64),
		},
	}, nil
}

func (f *kombifyMeOutageStackKitGenerator) GenerateStackKitArtifacts(_ context.Context, req StackKitArtifactGenerateRequest) (*StackKitArtifactGenerateResult, error) {
	f.requests = append(f.requests, req)
	specBytes, err := os.ReadFile(req.StackSpecPath)
	if err != nil {
		return nil, err
	}
	specText := string(specBytes)
	f.specs = append(f.specs, specText)
	if strings.Contains(specText, "domain: kombify.me") {
		errText := f.errText
		if errText == "" {
			errText = `StackKits CLI generate failed: Error: kombify.me registration failed and no subdomainPrefix is configured: registration failed (503): {"status":"provider_offline"}`
		}
		return nil, errors.New(errText)
	}
	if err := os.MkdirAll(req.OutputDir, 0750); err != nil {
		return nil, err
	}
	tfvars := []byte("{\n  \"domain\": \"home.localhost\",\n  \"enable_coolify\": true\n}\n")
	if err := os.WriteFile(filepath.Join(req.OutputDir, "terraform.tfvars.json"), tfvars, 0600); err != nil {
		return nil, err
	}
	return &StackKitArtifactGenerateResult{
		StackSpecPath: req.StackSpecPath,
		OutputDir:     req.OutputDir,
		Metadata:      map[string]string{"artifact_generator": "fake-stackkit-cli"},
	}, nil
}

func (f *fakeRuntimeRunner) Run(ctx context.Context, req RuntimeActionRequest) error {
	_, err := f.run(ctx, req)
	return err
}

func (f *fakeRuntimeRunner) RunWithResult(ctx context.Context, req RuntimeActionRequest) (map[string]interface{}, error) {
	return f.run(ctx, req)
}

func (f *fakeRuntimeRunner) run(ctx context.Context, req RuntimeActionRequest) (map[string]interface{}, error) {
	f.calls = append(f.calls, req)
	f.identities = append(f.identities, identity.FromContext(ctx))
	if f.order != nil {
		*f.order = append(*f.order, f.name)
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func (f *sequenceRuntimeRunner) Run(_ context.Context, req RuntimeActionRequest) error {
	_, err := f.run(req)
	return err
}

func (f *sequenceRuntimeRunner) RunWithResult(_ context.Context, req RuntimeActionRequest) (map[string]interface{}, error) {
	return f.run(req)
}

func (f *sequenceRuntimeRunner) run(req RuntimeActionRequest) (map[string]interface{}, error) {
	f.calls = append(f.calls, req)
	if f.order != nil {
		*f.order = append(*f.order, f.name)
	}
	idx := len(f.calls) - 1
	if idx < len(f.errs) && f.errs[idx] != nil {
		return nil, f.errs[idx]
	}
	if idx < len(f.results) {
		return f.results[idx], nil
	}
	return nil, nil
}

func (f *fakeManagedLeaseManager) CreateOrBindLease(_ context.Context, req ManagedLeaseRequest) (*ManagedLeaseResult, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return &ManagedLeaseResult{
		LeaseID:      "lease-test",
		Provider:     "centron",
		DesiredState: "running",
		Phase:        RuntimePhaseLeaseReady,
		Target: &ManagedRuntimeTarget{
			Host:          "203.0.113.10",
			PublicIP:      "203.0.113.10",
			SSHUser:       "ubuntu",
			SSHPort:       22,
			SSHPrivateKey: "test-private-key",
			Source:        "test-lease",
		},
	}, nil
}

func (f *fakeManagedLeaseManager) UpdateLeaseMetadata(_ context.Context, tenantID, leaseID string, metadata map[string]string) error {
	if f.metadataErr != nil {
		return f.metadataErr
	}
	copied := make(map[string]string, len(metadata))
	for key, value := range metadata {
		copied[key] = value
	}
	f.metadataUpdates = append(f.metadataUpdates, fakeManagedLeaseMetadataUpdate{
		TenantID: tenantID,
		LeaseID:  leaseID,
		Metadata: copied,
	})
	return nil
}

func (f *fakeManagedLeaseDecommissioner) DecommissionManagedLeases(_ context.Context, req ManagedLeaseDecommissionRequest) (*ManagedLeaseDecommissionResult, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return nil, f.err
	}
	if f.result != nil {
		return f.result, nil
	}
	return &ManagedLeaseDecommissionResult{
		Decommissioned: 1,
		LeaseIDs:       []string{"lease-test"},
		Proofs: []ManagedLeaseDecommissionProof{
			testManagedLeaseDecommissionProof(req.StackID, req.TenantID, "lease-test", ManagedLeaseDecommissionObservedDecommissioned, req.ResourceGenerationDigest),
		},
	}, nil
}

func testManagedLeaseDecommissionProof(stackID, tenantID, leaseID, observedState, generationDigest string) ManagedLeaseDecommissionProof {
	if strings.TrimSpace(generationDigest) == "" {
		generationDigest = strings.Repeat("a", 64)
	}
	return ManagedLeaseDecommissionProof{
		StackID:                  stackID,
		TenantID:                 tenantID,
		LeaseID:                  leaseID,
		ProviderID:               "centron",
		ResourceGenerationID:     "11111111-1111-4111-8111-111111111111",
		ResourceGenerationDigest: generationDigest,
		ObservedState:            observedState,
		ReceiptRef:               "provider-receipt://" + leaseID,
		ReceiptDigest:            strings.Repeat("b", 64),
		VerifiedAt:               time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC),
	}
}

func (f *fakeManagedRuntimeTargetResolver) ResolveManagedRuntimeTarget(_ context.Context, req ManagedRuntimeTargetRequest) (*ManagedRuntimeTarget, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return nil, f.err
	}
	if f.target != nil {
		return f.target, nil
	}
	return &ManagedRuntimeTarget{
		Host:          "203.0.113.30",
		PublicIP:      "203.0.113.30",
		SSHUser:       "ubuntu",
		SSHPort:       22,
		SSHPrivateKey: "test-private-key",
		Source:        "test-resolver",
	}, nil
}

func (f *sequenceManagedRuntimeTargetResolver) ResolveManagedRuntimeTarget(_ context.Context, req ManagedRuntimeTargetRequest) (*ManagedRuntimeTarget, error) {
	f.requests = append(f.requests, req)
	index := len(f.requests) - 1
	if index < len(f.errs) && f.errs[index] != nil {
		return nil, f.errs[index]
	}
	if index < len(f.targets) && f.targets[index] != nil {
		return f.targets[index], nil
	}
	if len(f.targets) > 0 {
		return f.targets[len(f.targets)-1], nil
	}
	return nil, errors.New("target not ready")
}

func (f *blockingManagedRuntimeTargetResolver) ResolveManagedRuntimeTarget(ctx context.Context, req ManagedRuntimeTargetRequest) (*ManagedRuntimeTarget, error) {
	f.requests = append(f.requests, req)
	<-ctx.Done()
	return nil, ctx.Err()
}

func persistDeployFixture(t *testing.T, baseDir, stackID string) {
	t.Helper()

	persister, err := unifier.NewSpecPersisterWithPath(filepath.Join(baseDir, stackID))
	if err != nil {
		t.Fatalf("create persister: %v", err)
	}

	intent := []byte(`name: test-homelab
kit: basement-kit
domain: home.localhost
nodes:
  - name: main-server
    type: main
    provider: local
services:
  - name: traefik
    type: reverse-proxy
    node: main-server
`)
	intentPath, _, err := persister.SaveIntentBytes(intent)
	if err != nil {
		t.Fatalf("save intent: %v", err)
	}
	if _, err := persister.SaveRequirementsSpec(&core.RequirementsSpec{
		StackKit: "basement-kit",
		RequiredWorkers: core.WorkerRequirements{
			MinLocalServers: 1,
			MinRAM:          512,
			MinCPU:          1,
		},
		Description: "BasementKit test requirements",
	}, intentPath); err != nil {
		t.Fatalf("save requirements: %v", err)
	}
}

func writeJobsTestStackKitsDir(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	writeJobsTestStackKitCue(t, root)
	for _, kit := range []string{DefaultBasementKitRef, DefaultCloudKitRef, unifier.StackKitModernHomelab} {
		templateDir := filepath.Join(root, kit, "templates", "simple")
		if err := os.MkdirAll(templateDir, 0o750); err != nil {
			t.Fatalf("create StackKits template dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(templateDir, "main.tf"), []byte("# jobs test StackKit\n"), 0o600); err != nil {
			t.Fatalf("write test main.tf: %v", err)
		}
	}
	return root
}

func writeJobsTestStackKitCue(t *testing.T, root string) {
	t.Helper()

	baseDir := filepath.Join(root, "base")
	if err := os.MkdirAll(baseDir, 0o750); err != nil {
		t.Fatalf("create base StackKit schema dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "base.cue"), []byte(`package base

#StackBase: _
`), 0o600); err != nil {
		t.Fatalf("write base StackKit schema: %v", err)
	}

	for _, kit := range []string{DefaultBasementKitRef, DefaultCloudKitRef, unifier.StackKitModernHomelab} {
		kitDir := filepath.Join(root, kit)
		if err := os.MkdirAll(kitDir, 0o750); err != nil {
			t.Fatalf("create StackKit schema dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(kitDir, "stackfile.cue"), []byte(fmt.Sprintf(`package stackkit

metadata: {
	name: %q
	displayName: %q
	version: "0.0.0-test"
	description: "Test StackKit"
}

#TestStack: #StackBase
`, kit, kit)), 0o600); err != nil {
			t.Fatalf("write StackKit schema: %v", err)
		}
	}
}

func TestProvisionHandler_MissingSpec(t *testing.T) {
	cfg := &ProvisionConfig{
		WorkDir: t.TempDir(),
		// keep persisted specs in temp dir
		SpecBaseDir: t.TempDir(),
	}

	handler := ProvisionHandler(cfg)

	job := &Job{
		ID:         "test-job-1",
		Type:       JobTypeProvision,
		TargetID:   "stack-123",
		TargetName: "test-stack",
		Payload:    map[string]interface{}{}, // Missing spec
	}

	// Create a minimal queue for testing
	queue := &Queue{
		jobs: map[string]*Job{job.ID: job},
	}

	err := handler(context.Background(), job, queue)
	if err != nil {
		if pe, ok := err.(*ProvisionError); ok {
			t.Logf("provision error: step=%s msg=%s details=%s", pe.Step, pe.Message, pe.Details)
		} else {
			t.Logf("provision error: %v", err)
		}
	}
	if err == nil {
		t.Fatal("expected error for missing spec")
	}

	if err.Error() != "missing 'spec' in job payload" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestProvisionHandler_InvalidSpec(t *testing.T) {
	cfg := &ProvisionConfig{
		WorkDir:      t.TempDir(),
		SpecBaseDir:  t.TempDir(),
		StackKitsDir: writeJobsTestStackKitsDir(t),
	}

	handler := ProvisionHandler(cfg)

	job := &Job{
		ID:         "test-job-2",
		Type:       JobTypeProvision,
		TargetID:   "stack-123",
		TargetName: "test-stack",
		Payload: map[string]interface{}{
			"spec": map[string]interface{}{
				// Missing required fields
				"name": "incomplete-spec",
			},
		},
	}

	queue := &Queue{
		jobs: map[string]*Job{job.ID: job},
	}

	err := handler(context.Background(), job, queue)
	if err != nil {
		if pe, ok := err.(*ProvisionError); ok {
			t.Fatalf("expected wizard-format spec to be auto-normalized and succeed, got ProvisionError: step=%s msg=%s details=%s", pe.Step, pe.Message, pe.Details)
		}
		t.Fatalf("expected wizard-format spec to be auto-normalized and succeed, got error: %v", err)
	}

	if job.Result == nil {
		t.Fatalf("expected job.Result to be populated")
	}

	if got, _ := job.Result["status"].(string); got != "requirements_ready" {
		t.Fatalf("expected status requirements_ready, got %q", got)
	}
	if got, _ := job.Result["intent_path"].(string); got == "" {
		t.Fatalf("expected intent_path in job.Result")
	}
}

func TestProvisionHandler_ValidSpec_NoTofu(t *testing.T) {
	// This test validates the flow up to OpenTofu execution
	// Provision no longer runs OpenTofu; rollout happens in the deploy job.

	cfg := &ProvisionConfig{
		WorkDir:      t.TempDir(),
		SpecBaseDir:  t.TempDir(),
		StackKitsDir: writeJobsTestStackKitsDir(t),
	}

	handler := ProvisionHandler(cfg)

	job := &Job{
		ID:         "test-job-3",
		Type:       JobTypeProvision,
		TargetID:   "stack-valid",
		TargetName: "test-stack",
		Payload: map[string]interface{}{
			"spec": map[string]interface{}{
				"name": "test-homelab",
				"kit":  "basement-kit",
				"nodes": []interface{}{
					map[string]interface{}{
						"name":     "main-server",
						"type":     "main",
						"provider": "local",
					},
				},
				"services": []interface{}{
					map[string]interface{}{
						"name": "traefik",
						"type": "reverse-proxy",
						"node": "main-server",
					},
				},
			},
		},
	}

	queue := &Queue{
		jobs: map[string]*Job{job.ID: job},
	}

	err := handler(context.Background(), job, queue)
	if err != nil {
		if pe, ok := err.(*ProvisionError); ok {
			t.Fatalf("expected success, got ProvisionError: step=%s msg=%s details=%s", pe.Step, pe.Message, pe.Details)
		}
		t.Fatalf("expected success, got error: %v", err)
	}

	// Ensure intent + requirements were persisted under SpecBaseDir
	base := filepath.Join(cfg.SpecBaseDir, job.TargetID)
	if _, statErr := os.Stat(filepath.Join(base, "kombination.yaml")); os.IsNotExist(statErr) {
		t.Fatalf("expected persisted kombination.yaml")
	}
	if _, statErr := os.Stat(filepath.Join(base, "requirements-spec.yaml")); os.IsNotExist(statErr) {
		t.Fatalf("expected persisted requirements-spec.yaml")
	}
}

func TestProvisionHandler_UserOwnedTargetDoesNotEmitMonthlyRuntimeDefaults(t *testing.T) {
	cfg := &ProvisionConfig{
		WorkDir:      t.TempDir(),
		SpecBaseDir:  t.TempDir(),
		StackKitsDir: writeJobsTestStackKitsDir(t),
	}

	handler := ProvisionHandler(cfg)
	job := &Job{
		ID:         "test-job-user-owned-runtime-fields",
		Type:       JobTypeProvision,
		TargetID:   "stack-user-owned",
		TargetName: "user-owned-stack",
		Payload: map[string]interface{}{
			"spec": map[string]interface{}{
				"name": "user-owned-stack",
				"kit":  "basement-kit",
				"nodes": []interface{}{
					map[string]interface{}{
						"name":     "main-server",
						"type":     "main",
						"provider": "local",
					},
				},
				"services": []interface{}{
					map[string]interface{}{
						"name": "traefik",
						"type": "reverse-proxy",
						"node": "main-server",
					},
				},
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	if err := handler(context.Background(), job, queue); err != nil {
		t.Fatalf("expected user-owned target provision to succeed, got %v", err)
	}
	if got := job.Result[metadataKeyServerMode]; got != serverModeUserOwned {
		t.Fatalf("server_mode = %v, want %s", got, serverModeUserOwned)
	}
	for _, key := range []string{
		metadataKeyRuntimeLane,
		metadataKeyRuntimeOfferingID,
		metadataKeyLeaseProvider,
		metadataKeySimulateProviderID,
		metadataKeySimulateLifecycle,
		metadataKeyBillingCadence,
		"server_provisioning_mode",
		"server_connection_mode",
	} {
		if got, ok := job.Result[key]; ok && got != "" {
			t.Fatalf("%s = %v, want absent or empty for user-owned target provision", key, got)
		}
	}
}

func TestProvisionHandler_InstallCommandDoesNotBlockWithoutSimulatePreview(t *testing.T) {
	cfg := &ProvisionConfig{
		WorkDir:      t.TempDir(),
		SpecBaseDir:  t.TempDir(),
		StackKitsDir: writeJobsTestStackKitsDir(t),
	}
	handler := ProvisionHandler(cfg)
	job := &Job{
		ID:         "test-job-oneliner-preview-missing",
		Type:       JobTypeProvision,
		TargetID:   "stack-oneliner-preview-missing",
		TargetName: "one-liner-stack",
		Payload: map[string]interface{}{
			"spec": map[string]interface{}{
				"name": "one-liner-stack",
				"kit":  "basement-kit",
				"nodes": []interface{}{
					map[string]interface{}{
						"name":     "main-server",
						"type":     "main",
						"provider": "local",
					},
				},
				"services": []interface{}{
					map[string]interface{}{
						"name": "traefik",
						"type": "reverse-proxy",
						"node": "main-server",
					},
				},
				"metadata": map[string]interface{}{
					"server_provisioning_mode":        "install-command",
					"server_connection_mode":          "agent-oneliner",
					"server_install_command_required": "true",
				},
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}
	if err := handler(context.Background(), job, queue); err != nil {
		t.Fatalf("ProvisionHandler: %v", err)
	}
	if got := job.Result["server_install_command_released"]; got != true {
		t.Fatalf("server_install_command_released = %v, want true", got)
	}
	if got := job.Result["simulation_preview_status"]; got != "not_configured" {
		t.Fatalf("simulation_preview_status = %v, want not_configured", got)
	}
	if got := job.Result["simulation_preview_ttl_seconds"]; got != 3600 {
		t.Fatalf("simulation_preview_ttl_seconds = %v, want 3600", got)
	}
}

func TestProvisionHandler_InstallCommandReleasesCommandAfterSimulatePreview(t *testing.T) {
	simulation := &fakeRuntimeRunner{
		name: "simulation",
		result: map[string]interface{}{
			"status":        string(runtimeaction.StatusReady),
			"mode":          string(runtimeaction.ModeDryRun),
			"simulation_id": "simprev-1",
			"deployment_id": "simdep-1",
			"preview_url":   "https://simulate.kombify.io/previews/simprev-1",
			"expires_at":    "2026-07-03T12:00:00Z",
			"node_ids":      []interface{}{"preview-primary", "preview-services", "preview-storage"},
			"install_command_release": map[string]interface{}{
				"state":  "released",
				"reason": "simulation_preview_ready",
			},
		},
	}
	cfg := &ProvisionConfig{
		WorkDir:      t.TempDir(),
		SpecBaseDir:  t.TempDir(),
		StackKitsDir: writeJobsTestStackKitsDir(t),
		RuntimeActions: RuntimeActions{
			SimulationGate: simulation,
		},
	}
	handler := ProvisionHandler(cfg)
	job := &Job{
		ID:         "test-job-oneliner-preview",
		Type:       JobTypeProvision,
		TargetID:   "stack-oneliner-preview",
		TargetName: "one-liner-stack",
		Payload: map[string]interface{}{
			"owner_id":  "auth0|staff",
			"tenant_id": "org-1",
			"actor": map[string]interface{}{
				"user_id":   "auth0|staff",
				"tenant_id": "org-1",
				"roles":     []interface{}{"developer"},
			},
			"spec": map[string]interface{}{
				"name": "one-liner-stack",
				"kit":  "modern-homelab",
				"nodes": []interface{}{
					map[string]interface{}{
						"name":     "main-server",
						"type":     "main",
						"provider": "local",
					},
					map[string]interface{}{
						"name":     "cloud-1",
						"type":     "worker",
						"provider": "ionos",
					},
					map[string]interface{}{
						"name":     "cloud-2",
						"type":     "worker",
						"provider": "centron",
					},
				},
				"services": []interface{}{
					map[string]interface{}{
						"name": "traefik",
						"type": "reverse-proxy",
						"node": "main-server",
					},
					map[string]interface{}{
						"name": "immich",
						"type": "media",
						"node": "cloud-1",
					},
				},
				"metadata": map[string]interface{}{
					"server_provisioning_mode":        "install-command",
					"server_connection_mode":          "agent-oneliner",
					"server_install_command_required": "true",
				},
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}
	if err := handler(context.Background(), job, queue); err != nil {
		if pe, ok := err.(*ProvisionError); ok {
			t.Fatalf("ProvisionHandler: %v\n%s", err, pe.Details)
		}
		t.Fatalf("ProvisionHandler: %v", err)
	}
	if len(simulation.calls) != 1 {
		t.Fatalf("simulation calls = %d, want 1", len(simulation.calls))
	}
	call := simulation.calls[0]
	if call.Action != runtimeaction.ActionSimulateUpdate || call.PreviewPolicy == nil || !call.PreviewPolicy.StaffOnly || call.PreviewPolicy.Required {
		t.Fatalf("simulation request = %+v", call)
	}
	if call.PreviewPolicy.TTLSeconds != 3600 {
		t.Fatalf("simulation preview ttl = %d, want 3600", call.PreviewPolicy.TTLSeconds)
	}
	if call.StackKit != "modern-homelab" || call.TenantID != "org-1" || call.OwnerID != "auth0|staff" {
		t.Fatalf("simulation identity/scope = %+v", call)
	}
	if len(simulation.identities) != 1 || simulation.identities[0] == nil || !simulation.identities[0].HasRole("developer") {
		t.Fatalf("simulation context identity = %+v", simulation.identities)
	}
	if got := job.Result["simulation_preview_id"]; got != "simprev-1" {
		t.Fatalf("simulation_preview_id = %v", got)
	}
	if got := job.Result["server_install_command_released"]; got != true {
		t.Fatalf("server_install_command_released = %v, want true", got)
	}
	runtimeProof := job.Result["runtime_proof"].(map[string]interface{})
	proof := runtimeProof["simulation"].(map[string]interface{})
	if proof["preview_url"] == "" || proof["install_command_release"] == nil {
		t.Fatalf("simulation proof = %+v", proof)
	}
}

func TestProvisionHandler_InstallCommandPreviewTimeoutDoesNotBlockCommand(t *testing.T) {
	oldTimeout := oneLinerSimulationPreviewTimeout
	oneLinerSimulationPreviewTimeout = 20 * time.Millisecond
	t.Cleanup(func() { oneLinerSimulationPreviewTimeout = oldTimeout })

	simulation := &contextDeadlineRuntimeRunner{}
	cfg := &ProvisionConfig{
		WorkDir:      t.TempDir(),
		SpecBaseDir:  t.TempDir(),
		StackKitsDir: writeJobsTestStackKitsDir(t),
		RuntimeActions: RuntimeActions{
			SimulationGate: simulation,
		},
	}
	handler := ProvisionHandler(cfg)
	job := &Job{
		ID:         "test-job-oneliner-preview-timeout",
		Type:       JobTypeProvision,
		TargetID:   "stack-oneliner-timeout",
		TargetName: "one-liner-timeout-stack",
		Payload: map[string]interface{}{
			"owner_id":  "auth0|staff",
			"tenant_id": "org-1",
			"actor": map[string]interface{}{
				"user_id":   "auth0|staff",
				"tenant_id": "org-1",
				"roles":     []interface{}{"developer"},
			},
			"spec": map[string]interface{}{
				"name": "one-liner-timeout-stack",
				"kit":  "modern-homelab",
				"nodes": []interface{}{
					map[string]interface{}{
						"name":     "main-server",
						"type":     "main",
						"provider": "local",
					},
				},
				"metadata": map[string]interface{}{
					"server_provisioning_mode":        "install-command",
					"server_connection_mode":          "agent-oneliner",
					"server_install_command_required": "true",
				},
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}
	startedAt := time.Now()
	if err := handler(context.Background(), job, queue); err != nil {
		t.Fatalf("ProvisionHandler: %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("handler elapsed = %s, want bounded preview timeout", elapsed)
	}
	if len(simulation.calls) != 1 {
		t.Fatalf("simulation calls = %d, want 1", len(simulation.calls))
	}
	if got := job.Result["server_install_command_released"]; got != true {
		t.Fatalf("server_install_command_released = %v, want true", got)
	}
	if got := job.Result["simulation_preview_status"]; got != "timeout" {
		t.Fatalf("simulation_preview_status = %v, want timeout", got)
	}
	if _, ok := job.Result["simulation_preview_url"]; ok {
		t.Fatalf("simulation_preview_url should not be set after timeout: %+v", job.Result)
	}
}

func TestProvisionHandler_ManagedCloudCreatesLeaseAndReturnsRuntimePhase(t *testing.T) {
	leaseManager := &fakeManagedLeaseManager{}
	cfg := &ProvisionConfig{
		WorkDir:      t.TempDir(),
		SpecBaseDir:  t.TempDir(),
		StackKitsDir: writeJobsTestStackKitsDir(t),
		RuntimeActions: RuntimeActions{
			StackKitGenerator: &fakeStackKitArtifactGenerator{},
			LeaseManager:      leaseManager,
		},
	}

	handler := ProvisionHandler(cfg)
	job := &Job{
		ID:         "test-job-managed-cloud",
		Type:       JobTypeProvision,
		TargetID:   "stack-managed",
		TargetName: "managed-stack",
		Payload: map[string]interface{}{
			"owner_id":  "user-1",
			"tenant_id": "org-1",
			"spec": map[string]interface{}{
				"name":        "managed-stack",
				"provider":    "cloud",
				"provider_id": "centron",
				"goals": map[string]interface{}{
					"storage": true,
				},
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	err := handler(context.Background(), job, queue)
	if err != nil {
		t.Fatalf("expected managed cloud provision to succeed, got %v", err)
	}
	if len(leaseManager.requests) != 1 {
		t.Fatalf("expected one lease request, got %d", len(leaseManager.requests))
	}
	req := leaseManager.requests[0]
	if req.StackID != "stack-managed" || req.TenantID != "org-1" || req.OwnerID != "user-1" {
		t.Fatalf("unexpected lease identity: %+v", req)
	}
	if req.Provider != "centron" {
		t.Fatalf("lease provider = %q, want centron", req.Provider)
	}
	if req.OperationKey != PrimaryManagedLeaseOperationKey || req.NodeRole != "foundation" {
		t.Fatalf("primary managed lease intent = %+v, want primary/foundation", req)
	}
	services := make(map[string]struct{}, len(req.Services))
	for _, service := range req.Services {
		services[service] = struct{}{}
	}
	for _, required := range []string{"traefik", "pocket-id"} {
		if _, ok := services[required]; !ok {
			t.Fatalf("primary managed lease services = %v, missing %q", req.Services, required)
		}
	}
	if req.Metadata["server_mode"] != "monthly-runtime" || req.Metadata["runtime_lane"] != "monthly-runtime" {
		t.Fatalf("runtime metadata = %+v, want monthly-runtime lane", req.Metadata)
	}
	if req.Metadata["provider_id"] != "centron" || req.Metadata["lease_provider"] != "" || req.Metadata["simulate_provider_id"] != "" {
		t.Fatalf("provider metadata = %+v, want canonical centron without legacy fields", req.Metadata)
	}
	if req.StackKit != DefaultCloudKitRef {
		t.Fatalf("lease stackkit = %q, want %s", req.StackKit, DefaultCloudKitRef)
	}
	if got := job.Result["runtime_phase"]; got != string(RuntimePhaseLeaseReady) {
		t.Fatalf("runtime_phase = %v, want %s", got, RuntimePhaseLeaseReady)
	}
	if got := job.Result["lease_id"]; got != "lease-test" {
		t.Fatalf("lease_id = %v, want lease-test", got)
	}
}

func TestProvisionHandler_ManagedCloudWaitsWhileProviderProvisionIsPending(t *testing.T) {
	leaseManager := &fakeManagedLeaseManager{result: &ManagedLeaseResult{
		LeaseID:      "lease-pending",
		OperationID:  "operation-pending",
		Provider:     "ionos",
		DesiredState: "running",
		Phase:        RuntimePhaseLeasePending,
	}}
	cfg := &ProvisionConfig{
		WorkDir:      t.TempDir(),
		SpecBaseDir:  t.TempDir(),
		StackKitsDir: writeJobsTestStackKitsDir(t),
		RuntimeActions: RuntimeActions{
			StackKitGenerator: &fakeStackKitArtifactGenerator{},
			LeaseManager:      leaseManager,
		},
	}
	job := &Job{
		ID:         "test-job-managed-cloud-pending",
		Type:       JobTypeProvision,
		TargetID:   "stack-managed-pending",
		TargetName: "managed-stack-pending",
		Payload: map[string]interface{}{
			"owner_id":  "user-1",
			"tenant_id": "org-1",
			"spec": map[string]interface{}{
				"name":        "managed-stack-pending",
				"provider":    "cloud",
				"provider_id": "ionos",
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	err := ProvisionHandler(cfg)(context.Background(), job, queue)
	waitErr, ok := asJobWaitError(err)
	if !ok {
		t.Fatalf("ProvisionHandler error = %v, want JobWaitError", err)
	}
	if waitErr.Reason != WaitReasonManagedRuntimeProvider || waitErr.ResumeAfter != 15*time.Second {
		t.Fatalf("wait error = %+v, want provider wait with 15s resume", waitErr)
	}
	if len(leaseManager.requests) != 1 {
		t.Fatalf("lease requests = %d, want 1", len(leaseManager.requests))
	}
	if got := job.Result["runtime_phase"]; got != string(RuntimePhaseLeasePending) {
		t.Fatalf("runtime_phase = %v, want %s", got, RuntimePhaseLeasePending)
	}
	if got := job.Result["operation_id"]; got != "operation-pending" {
		t.Fatalf("operation_id = %v, want durable provider correlation", got)
	}
	lifecycle := copyRuntimeLifecycle(job.Result)
	if got := stringFromInterface(lifecycle["current_phase"]); got != runtimePhaseServerAllocate {
		t.Fatalf("current lifecycle phase = %q, want %q", got, runtimePhaseServerAllocate)
	}
	phases, _ := lifecycle["phases"].([]interface{})
	for _, raw := range phases {
		entry, _ := raw.(map[string]interface{})
		if stringFromInterface(entry["id"]) != runtimePhaseServerAllocate {
			continue
		}
		if got := stringFromInterface(entry[resultStatusField]); got != runtimeLifecycleRunning {
			t.Fatalf("server_allocate status = %q, want %q", got, runtimeLifecycleRunning)
		}
		return
	}
	t.Fatal("server_allocate lifecycle phase missing")
}

func TestProvisionHandler_ProviderWaitReplaysPreparedManagedLeaseRequest(t *testing.T) {
	leaseManager := &fakeManagedLeaseManager{result: &ManagedLeaseResult{
		LeaseID: "lease-pending", OperationID: "operation-pending", Provider: "ionos",
		DesiredState: "running", Phase: RuntimePhaseLeasePending,
	}}
	cfg := &ProvisionConfig{
		WorkDir: t.TempDir(), SpecBaseDir: t.TempDir(), StackKitsDir: writeJobsTestStackKitsDir(t),
		RuntimeActions: RuntimeActions{StackKitGenerator: &fakeStackKitArtifactGenerator{}, LeaseManager: leaseManager},
	}
	prepared := ManagedLeaseRequest{
		StackID: "stack-managed-pending", StackName: "managed-stack-2", StackKit: DefaultCloudKitRef,
		TenantID: "org-1", OwnerID: "user-1", Provider: "ionos",
		OperationKey: PrimaryManagedLeaseOperationKey, RuntimeSlotKey: PrimaryManagedRuntimeSlotKey,
		RuntimeSlotGeneration: 1, NodeRole: "foundation",
	}
	job := &Job{
		ID: "test-job-managed-cloud-prepared-replay", Type: JobTypeProvision,
		TargetID: "stack-managed-pending", TargetName: "managed-stack-2",
		Payload: map[string]interface{}{
			"owner_id": "user-1", "tenant_id": "org-1",
			PreparedManagedLeaseRequestPayloadKey: ManagedLeaseRequestPayload(prepared),
			"spec": map[string]interface{}{
				"name": "managed-stack", "provider": "cloud", "provider_id": "ionos",
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}
	handler := ProvisionHandler(cfg)
	for attempt := 0; attempt < 2; attempt++ {
		if _, ok := asJobWaitError(handler(context.Background(), job, queue)); !ok {
			t.Fatalf("attempt %d did not wait for provider", attempt+1)
		}
	}
	if len(leaseManager.requests) != 2 {
		t.Fatalf("lease requests = %d, want one exact replay", len(leaseManager.requests))
	}
	for index, request := range leaseManager.requests {
		if request.StackName != "managed-stack-2" || request.StackID != prepared.StackID || request.RuntimeSlotKey != prepared.RuntimeSlotKey {
			t.Fatalf("lease request %d drifted across provider wait: %+v", index+1, request)
		}
	}
}

func TestProvisionHandlerRejectsLegacyProviderIdentityBeforeArtifactsOrLease(t *testing.T) {
	tests := map[string]map[string]interface{}{
		"missing provider_id": {
			"name":     "missing-provider",
			"provider": "cloud",
		},
		"managed node cannot replace provider_id": {
			"name":     "node-only-provider",
			"provider": "cloud",
			"nodes": []interface{}{
				map[string]interface{}{"name": "main", "provider": "ionos"},
			},
		},
		"provider mode case is not normalized": {
			"name":     "case-provider",
			"provider": "IONOS",
		},
		"provider mode whitespace is not normalized": {
			"name":     "whitespace-provider",
			"provider": " ionos ",
		},
		"composite provider_id": {
			"name":        "alias-provider",
			"provider_id": "ionos-managed",
		},
		"legacy lease_provider": {
			"name":     "legacy-provider-field",
			"provider": "cloud",
			"options": map[string]interface{}{
				"lease_provider": "ionos-managed",
			},
		},
	}
	for name, spec := range tests {
		t.Run(name, func(t *testing.T) {
			specDir := t.TempDir()
			leaseManager := &fakeManagedLeaseManager{}
			handler := ProvisionHandler(&ProvisionConfig{
				WorkDir:      t.TempDir(),
				SpecBaseDir:  specDir,
				StackKitsDir: writeJobsTestStackKitsDir(t),
				RuntimeActions: RuntimeActions{
					LeaseManager: leaseManager,
				},
			})
			job := &Job{
				ID:       "reject-provider-identity",
				Type:     JobTypeProvision,
				TargetID: "stack-reject-provider-identity",
				Payload: map[string]interface{}{
					"owner_id":  "user-1",
					"tenant_id": "org-1",
					"spec":      spec,
				},
			}
			queue := &Queue{jobs: map[string]*Job{job.ID: job}}

			if err := handler(context.Background(), job, queue); err == nil {
				t.Fatal("ProvisionHandler succeeded")
			}
			if len(leaseManager.requests) != 0 {
				t.Fatalf("lease requests = %d, want zero", len(leaseManager.requests))
			}
			entries, err := os.ReadDir(specDir)
			if err != nil {
				t.Fatalf("ReadDir: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("persisted artifact entries = %v, want none", entries)
			}
		})
	}
}

func TestProvisionHandler_ManagedCloudSelectsOfferingForRequirements(t *testing.T) {
	job := &Job{Result: map[string]interface{}{}}
	spec := &core.KombinationSpec{Metadata: map[string]string{}}
	requirements := &core.RequirementsSpec{RequiredWorkers: core.WorkerRequirements{MinCPU: 4, MinRAM: 4096}}

	if err := applyManagedRuntimeOfferingRequirements(job, spec, requirements); err != nil {
		t.Fatalf("apply offering requirements: %v", err)
	}
	if got := spec.Metadata[metadataKeyRuntimeOfferingID]; got != "monthly-runtime-premium" {
		t.Fatalf("spec metadata runtime offering = %q, want premium", got)
	}
	if got := job.Result[metadataKeyRuntimeOfferingID]; got != "monthly-runtime-premium" {
		t.Fatalf("job result runtime offering = %v, want premium", got)
	}
}

func TestProvisionHandler_ManagedCloudUsesLargestOfferingWhenRequirementsExceedCatalog(t *testing.T) {
	job := &Job{Result: map[string]interface{}{}}
	spec := &core.KombinationSpec{Metadata: map[string]string{}}
	requirements := &core.RequirementsSpec{RequiredWorkers: core.WorkerRequirements{MinCPU: 128, MinRAM: 1048576}}

	if err := applyManagedRuntimeOfferingRequirements(job, spec, requirements); err != nil {
		t.Fatalf("apply oversized offering requirements: %v", err)
	}
	if got := spec.Metadata[metadataKeyRuntimeOfferingID]; got != "monthly-runtime-premium" {
		t.Fatalf("spec metadata runtime offering = %q, want premium", got)
	}
	if got := spec.Metadata["runtime_offering_capacity_status"]; got != "below_requirements" {
		t.Fatalf("runtime capacity status = %q, want below_requirements", got)
	}
	if got := job.Result["runtime_offering_capacity_status"]; got != "below_requirements" {
		t.Fatalf("job result capacity status = %v, want below_requirements", got)
	}
}

func prepareStackSpecsForDeploy(t *testing.T, specBaseDir string, stackID string) {
	t.Helper()

	cfg := &ProvisionConfig{
		WorkDir:      t.TempDir(),
		SpecBaseDir:  specBaseDir,
		StackKitsDir: writeJobsTestStackKitsDir(t),
	}
	handler := ProvisionHandler(cfg)
	job := &Job{
		ID:         "prepare-" + stackID,
		Type:       JobTypeProvision,
		TargetID:   stackID,
		TargetName: "test-stack",
		Payload: map[string]interface{}{
			"spec": map[string]interface{}{
				"name": "basekit-test",
				"kit":  DefaultBasementKitRef,
				"nodes": []interface{}{
					map[string]interface{}{
						"name":     "main-server",
						"type":     "main",
						"provider": "local",
					},
				},
				"services": []interface{}{
					map[string]interface{}{"name": "traefik", "type": "reverse-proxy", "node": "main-server"},
					map[string]interface{}{"name": "pocket-id", "type": "auth", "node": "main-server"},
					map[string]interface{}{"name": "vaultwarden", "type": "auth", "node": "main-server"},
					map[string]interface{}{"name": "immich-server", "type": "media", "node": "main-server"},
				},
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}
	if err := handler(context.Background(), job, queue); err != nil {
		t.Fatalf("prepare stack specs failed: %v", err)
	}
}

func deployWorkerPayload() []interface{} {
	return []interface{}{
		map[string]interface{}{
			"id":       "worker-1",
			"name":     "main-server",
			"type":     "main",
			"provider": "local",
			"status":   "online",
			"capabilities": map[string]interface{}{
				"cpu":           float64(4),
				"ram":           float64(8192),
				"disk":          float64(160),
				"arch":          "amd64",
				"os":            "ubuntu",
				"dockerVersion": "24.0.0",
			},
		},
	}
}

func TestParseWorkersFromPayloadAcceptsMapSlice(t *testing.T) {
	workers, err := parseWorkersFromPayload([]map[string]interface{}{
		{
			"id":       "worker-1",
			"name":     "main-server",
			"type":     "main",
			"provider": "local",
			"status":   "online",
			"capabilities": map[string]interface{}{
				"cpu":           float64(4),
				"ram":           float64(8192),
				"disk":          float64(160),
				"arch":          "amd64",
				"os":            "linux",
				"dockerVersion": "docker-desktop",
			},
		},
	})
	if err != nil {
		t.Fatalf("parseWorkersFromPayload returned error: %v", err)
	}
	if len(workers) != 1 || workers[0].ID != "worker-1" || workers[0].Provider != "local" {
		t.Fatalf("unexpected workers: %+v", workers)
	}
	if workers[0].Capabilities.CPU != 4 || workers[0].Capabilities.RAM != 8192 || workers[0].Capabilities.Disk != 160 {
		t.Fatalf("unexpected capabilities: %+v", workers[0].Capabilities)
	}
}

func createTestBasementKitStackKitsDir(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	writeJobsTestStackKitCue(t, root)
	for _, kit := range []string{DefaultBasementKitRef, DefaultCloudKitRef} {
		templateDir := filepath.Join(root, kit, "templates", "simple")
		if err := os.MkdirAll(templateDir, 0o750); err != nil {
			t.Fatalf("create template dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(templateDir, "main.tf"), []byte(`terraform {
  required_version = ">= 1.6.0"
}
`), 0o600); err != nil {
			t.Fatalf("write test StackKit template: %v", err)
		}
	}
	return root
}

func stackKitIdentityHandoffResult(stackID string) map[string]interface{} {
	return map[string]interface{}{
		"status": "verified",
		"stackkit_outputs": map[string]interface{}{
			"identity": map[string]interface{}{
				"owner": map[string]interface{}{
					"username": "owner@example.com",
					"email":    "owner@example.com",
				},
				"recovery": map[string]interface{}{
					"bundle_ref":              "vault:recovery/" + stackID,
					"passphrase_hash_present": true,
				},
			},
			"login_gateway": map[string]interface{}{
				"url": "https://techstack.kombify.io/login",
			},
		},
	}
}

func TestDeployHandler_SimulationFailureBlocksRollout(t *testing.T) {
	specBaseDir := t.TempDir()
	stackID := "stack-simulation-blocks"
	prepareStackSpecsForDeploy(t, specBaseDir, stackID)

	simulation := &fakeRuntimeActionRunner{err: errors.New("simulation boom")}
	verifier := &fakeRuntimeActionRunner{}
	restore := &fakeRuntimeActionRunner{}
	handler := DeployHandler(&ProvisionConfig{
		WorkDir:      t.TempDir(),
		StackKitsDir: createTestBasementKitStackKitsDir(t),
		SpecBaseDir:  specBaseDir,
		RuntimeActions: RuntimeActions{
			StackKitGenerator: &fakeStackKitArtifactGenerator{},
			SimulationGate:    simulation,
			RolloutVerifier:   verifier,
			RestoreDrill:      restore,
		},
	})

	job := &Job{
		ID:         "deploy-fail",
		Type:       JobTypeDeploy,
		TargetID:   stackID,
		TargetName: "test-stack",
		Payload: map[string]interface{}{
			"workers": deployWorkerPayload(),
			"apply":   false,
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	err := handler(context.Background(), job, queue)
	if err == nil {
		t.Fatal("expected simulation failure to block rollout")
	}
	if job.Step != StepSimulationGate {
		t.Fatalf("step = %q, want %q", job.Step, StepSimulationGate)
	}
	if len(simulation.calls) != 1 {
		t.Fatalf("simulation calls = %d, want 1", len(simulation.calls))
	}
	if len(verifier.calls) != 0 || len(restore.calls) != 0 {
		t.Fatalf("verification or restore ran after simulation failure")
	}
}

func TestDeployHandler_VerificationAndRestoreMarkRuntimeVerified(t *testing.T) {
	specBaseDir := t.TempDir()
	stackID := "stack-verified"
	prepareStackSpecsForDeploy(t, specBaseDir, stackID)

	simulation := &fakeRuntimeActionRunner{}
	verifier := &fakeRuntimeActionRunner{}
	restore := &fakeRuntimeActionRunner{}
	handler := DeployHandler(&ProvisionConfig{
		WorkDir:      t.TempDir(),
		StackKitsDir: createTestBasementKitStackKitsDir(t),
		SpecBaseDir:  specBaseDir,
		RuntimeActions: RuntimeActions{
			StackKitGenerator: &fakeStackKitArtifactGenerator{},
			SimulationGate:    simulation,
			RolloutRunner:     &fakeRuntimeRunner{name: "rollout", result: map[string]interface{}{"status": "applied"}},
			RolloutVerifier:   verifier,
			RestoreDrill:      restore,
		},
	})

	job := &Job{
		ID:         "deploy-verified",
		Type:       JobTypeDeploy,
		TargetID:   stackID,
		TargetName: "test-stack",
		Payload: map[string]interface{}{
			"workers": deployWorkerPayload(),
			"apply":   true,
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	if err := handler(context.Background(), job, queue); err != nil {
		t.Fatalf("deploy handler failed: %v", err)
	}
	if len(simulation.calls) != 1 || len(verifier.calls) != 1 || len(restore.calls) != 1 {
		t.Fatalf("expected all runtime hooks once, got simulation=%d verifier=%d restore=%d", len(simulation.calls), len(verifier.calls), len(restore.calls))
	}
	if got := job.Result["runtime_phase"]; got != string(RuntimePhaseVerified) {
		t.Fatalf("runtime_phase = %v, want %s", got, RuntimePhaseVerified)
	}
	if got := job.Result["verification_status"]; got != string(RuntimePhaseVerified) {
		t.Fatalf("verification_status = %v, want %s", got, RuntimePhaseVerified)
	}
	if got := job.Result["status"]; got != "deployed" {
		t.Fatalf("status = %v, want deployed", got)
	}
}

func TestProvisionHandler_ManagedCloudLeaseFailureBlocksRolloutPreparation(t *testing.T) {
	cfg := &ProvisionConfig{
		WorkDir:      t.TempDir(),
		SpecBaseDir:  t.TempDir(),
		StackKitsDir: writeJobsTestStackKitsDir(t),
		RuntimeActions: RuntimeActions{
			LeaseManager: &fakeManagedLeaseManager{err: errors.New("simulate unavailable")},
		},
	}

	handler := ProvisionHandler(cfg)
	job := &Job{
		ID:       "test-job-managed-cloud-failure",
		Type:     JobTypeProvision,
		TargetID: "stack-managed",
		Payload: map[string]interface{}{
			"owner_id":  "user-1",
			"tenant_id": "org-1",
			"spec": map[string]interface{}{
				"name":        "managed-stack",
				"provider":    "cloud",
				"provider_id": "centron",
				"goals": map[string]interface{}{
					"storage": true,
				},
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	err := handler(context.Background(), job, queue)
	if err == nil {
		t.Fatal("expected lease failure to block managed cloud preparation")
	}
	if !strings.Contains(err.Error(), "simulate unavailable") {
		t.Fatalf("expected underlying lease error, got %v", err)
	}
}

func TestProvisionHandler_AutoDeploysManagedCloudStack(t *testing.T) {
	order := []string{}
	simulation := &fakeRuntimeRunner{name: "simulate", order: &order}
	rollout := &fakeRuntimeRunner{name: "rollout", order: &order}
	verify := &fakeRuntimeRunner{name: "verify", order: &order, result: stackKitIdentityHandoffResult("stack-managed-auto")}
	restore := &fakeRuntimeRunner{name: "restore", order: &order, result: map[string]interface{}{
		"status": "verified",
		"runtime_metrics": map[string]interface{}{
			"cpu_percent":    12.5,
			"memory_percent": 34.5,
			"disk_percent":   56.5,
			"uptime_seconds": float64(789),
			"updated_at":     "2026-05-25T08:00:00Z",
		},
	}}
	lease := &fakeManagedLeaseManager{result: &ManagedLeaseResult{
		LeaseID:      "lease-test",
		Provider:     "centron",
		DesiredState: "running",
		Phase:        RuntimePhaseLeaseReady,
	}}
	resolver := &fakeManagedRuntimeTargetResolver{}
	specBaseDir := t.TempDir()
	cfg := &ProvisionConfig{
		WorkDir:             t.TempDir(),
		SpecBaseDir:         specBaseDir,
		StackKitsDir:        writeJobsTestStackKitsDir(t),
		AutoDeployAdmission: allowAutoDeployAdmissionForTest,
		RuntimeActions: RuntimeActions{
			StackKitGenerator:     &fakeStackKitArtifactGenerator{},
			LeaseManager:          lease,
			RuntimeTargetResolver: resolver,
			SimulationGate:        simulation,
			RolloutRunner:         rollout,
			RolloutVerifier:       verify,
			RestoreDrill:          restore,
		},
	}

	handler := ProvisionHandler(cfg)
	job := &Job{
		ID:       "test-job-managed-cloud-auto-deploy",
		Type:     JobTypeProvision,
		TargetID: "stack-managed-auto",
		Payload: map[string]interface{}{
			"auto_deploy": true,
			"owner_id":    "user-1",
			"tenant_id":   "org-1",
			"spec": map[string]interface{}{
				"name":        "managed-stack",
				"provider":    "cloud",
				"provider_id": "centron",
				"goals": map[string]interface{}{
					"storage": true,
				},
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	if err := handler(context.Background(), job, queue); err != nil {
		t.Fatalf("ProvisionHandler auto deploy failed: %v", err)
	}
	wantOrder := []string{"simulate", "rollout", "verify", "restore"}
	if strings.Join(order, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("runtime action order = %v, want %v", order, wantOrder)
	}
	if len(lease.requests) != 1 {
		t.Fatalf("lease requests = %d, want 1", len(lease.requests))
	}
	if got := job.Result["runtime_phase"]; got != string(RuntimePhaseVerified) {
		t.Fatalf("runtime_phase = %v, want %s", got, RuntimePhaseVerified)
	}
	if got := job.Result["verification_status"]; got != string(RuntimePhaseVerified) {
		t.Fatalf("verification_status = %v, want %s", got, RuntimePhaseVerified)
	}
	if got := job.Result["lease_id"]; got != "lease-test" {
		t.Fatalf("lease_id = %v, want lease-test", got)
	}
	if got := job.Result[metadataKeyRuntimeSSHHost]; got != "203.0.113.30" {
		t.Fatalf("%s = %v, want managed runtime host", metadataKeyRuntimeSSHHost, got)
	}
	if len(lease.metadataUpdates) != 1 {
		t.Fatalf("lease metadata updates = %d, want 1", len(lease.metadataUpdates))
	}
	update := lease.metadataUpdates[0]
	if update.TenantID != "org-1" || update.LeaseID != "lease-test" {
		t.Fatalf("metadata update target = tenant %q lease %q, want org-1 lease-test", update.TenantID, update.LeaseID)
	}
	if update.Metadata[metadataKeyRuntimeSSHHost] != "203.0.113.30" ||
		update.Metadata[metadataKeyRuntimePublicIP] != "203.0.113.30" ||
		update.Metadata["runtime_cpu_percent"] != "12.5" ||
		update.Metadata["runtime_memory_percent"] != "34.5" ||
		update.Metadata["runtime_disk_percent"] != "56.5" ||
		update.Metadata["runtime_uptime_seconds"] != "789" {
		t.Fatalf("metadata update = %+v, want target and runtime metrics", update.Metadata)
	}
	metrics, ok := job.Result["runtime_metrics"].(map[string]interface{})
	if !ok {
		t.Fatalf("job runtime_metrics = %#v, want map", job.Result["runtime_metrics"])
	}
	if metrics["runtime_cpu_percent"] != "12.5" {
		t.Fatalf("job runtime metrics = %+v, want persisted CPU metric", metrics)
	}
	if len(resolver.requests) != 1 {
		t.Fatalf("resolver requests = %d, want 1", len(resolver.requests))
	}
	if len(rollout.calls) != 1 || rollout.calls[0].RuntimeTarget == nil {
		t.Fatalf("rollout runtime target missing: %+v", rollout.calls)
	}
	if rollout.calls[0].RuntimeTarget.Host != "203.0.113.30" || rollout.calls[0].RuntimeTarget.PrivateKey != "test-private-key" {
		t.Fatalf("rollout runtime target = %+v", rollout.calls[0].RuntimeTarget)
	}
	resultJSON, err := json.Marshal(job.Result)
	if err != nil {
		t.Fatalf("marshal job result: %v", err)
	}
	if strings.Contains(string(resultJSON), "test-private-key") {
		t.Fatalf("job result leaked runtime private key: %s", resultJSON)
	}
	payloadJSON, err := json.Marshal(job.Payload)
	if err != nil {
		t.Fatalf("marshal job payload: %v", err)
	}
	if strings.Contains(string(payloadJSON), "test-private-key") {
		t.Fatalf("job payload leaked runtime private key: %s", payloadJSON)
	}
	unifiedBytes, err := os.ReadFile(filepath.Join(specBaseDir, "stack-managed-auto", "unified-spec.yaml"))
	if err != nil {
		t.Fatalf("read unified spec: %v", err)
	}
	unifiedText := string(unifiedBytes)
	if !strings.Contains(unifiedText, "host: 203.0.113.30") || !strings.Contains(unifiedText, "public_ip: 203.0.113.30") {
		t.Fatalf("unified spec does not include managed runtime target:\n%s", unifiedText)
	}
}

func TestProvisionHandler_RetriesManagedRestoreDrillWhileContainersStart(t *testing.T) {
	oldDelays := stackKitsRestoreReadinessRetryDelays
	stackKitsRestoreReadinessRetryDelays = []time.Duration{0}
	defer func() { stackKitsRestoreReadinessRetryDelays = oldDelays }()

	order := []string{}
	restore := &sequenceRuntimeRunner{
		name:  "restore",
		order: &order,
		errs: []error{
			errors.New(`runtime action restore_drill returned 502: {"error":{"details":{"error":"no running Docker containers"}}}`),
			nil,
		},
		results: []map[string]interface{}{
			nil,
			{"status": "verified", "runtime_metrics": map[string]interface{}{"uptime_seconds": float64(60)}},
		},
	}
	lease := &fakeManagedLeaseManager{result: &ManagedLeaseResult{
		LeaseID:      "lease-test",
		Provider:     "ionos",
		DesiredState: "running",
		Phase:        RuntimePhaseLeaseReady,
	}}
	cfg := &ProvisionConfig{
		WorkDir:             t.TempDir(),
		SpecBaseDir:         t.TempDir(),
		StackKitsDir:        writeJobsTestStackKitsDir(t),
		AutoDeployAdmission: allowAutoDeployAdmissionForTest,
		RuntimeActions: RuntimeActions{
			StackKitGenerator:     &fakeStackKitArtifactGenerator{},
			LeaseManager:          lease,
			RuntimeTargetResolver: &fakeManagedRuntimeTargetResolver{},
			SimulationGate:        &fakeRuntimeRunner{name: "simulate", order: &order},
			RolloutRunner:         &fakeRuntimeRunner{name: "rollout", order: &order},
			RolloutVerifier:       &fakeRuntimeRunner{name: "verify", order: &order, result: stackKitIdentityHandoffResult("stack-managed-restore-retry")},
			RestoreDrill:          restore,
		},
	}
	handler := ProvisionHandler(cfg)
	job := &Job{
		ID:       "test-job-managed-restore-retry",
		Type:     JobTypeProvision,
		TargetID: "stack-managed-restore-retry",
		Payload: map[string]interface{}{
			"auto_deploy": true,
			"owner_id":    "user-1",
			"tenant_id":   "org-1",
			"spec": map[string]interface{}{
				"name":        "managed-restore-retry",
				"provider":    "cloud",
				"provider_id": "centron",
				"goals": map[string]interface{}{
					"storage": true,
				},
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	if err := handler(context.Background(), job, queue); err != nil {
		t.Fatalf("ProvisionHandler auto deploy failed: %v", err)
	}
	wantOrder := []string{"simulate", "rollout", "verify", "restore", "restore"}
	if strings.Join(order, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("runtime action order = %v, want %v", order, wantOrder)
	}
	if len(restore.calls) != 2 {
		t.Fatalf("restore calls = %d, want retry after transient Docker readiness error", len(restore.calls))
	}
	if got := job.Result["runtime_phase"]; got != string(RuntimePhaseVerified) {
		t.Fatalf("runtime_phase = %v, want %s", got, RuntimePhaseVerified)
	}
}

func TestProvisionHandler_AutoDeployManagedKombifyMeDefersRouteRegistrationToStackKits(t *testing.T) {
	order := []string{}
	leaseManager := &fakeManagedLeaseManager{result: &ManagedLeaseResult{
		LeaseID:      "lease-ionos",
		Provider:     "ionos",
		DesiredState: "running",
		Phase:        RuntimePhaseLeaseReady,
		Target: &ManagedRuntimeTarget{
			Host:     "203.0.113.10",
			PublicIP: "203.0.113.10",
			SSHUser:  "ubuntu",
			SSHPort:  22,
			Source:   "test-lease",
		},
	}}
	stackKitGenerator := &fakeStackKitArtifactGenerator{}
	specBaseDir := t.TempDir()
	cfg := &ProvisionConfig{
		WorkDir:             t.TempDir(),
		SpecBaseDir:         specBaseDir,
		StackKitsDir:        writeJobsTestStackKitsDir(t),
		AutoDeployAdmission: allowAutoDeployAdmissionForTest,
		RuntimeActions: RuntimeActions{
			LeaseManager:          leaseManager,
			RuntimeTargetResolver: &fakeManagedRuntimeTargetResolver{},
			StackKitGenerator:     stackKitGenerator,
			SimulationGate:        &fakeRuntimeRunner{name: "simulate", order: &order},
			RolloutRunner:         &fakeRuntimeRunner{name: "rollout", order: &order},
			RolloutVerifier:       &fakeRuntimeRunner{name: "verify", order: &order, result: stackKitIdentityHandoffResult("stack-managed-kombify-me")},
			RestoreDrill:          &fakeRuntimeRunner{name: "restore", order: &order, result: map[string]interface{}{"status": "verified"}},
		},
	}

	handler := ProvisionHandler(cfg)
	job := &Job{
		ID:       "test-job-managed-kombify-me",
		Type:     JobTypeProvision,
		TargetID: "stack-managed-kombify-me",
		Payload: map[string]interface{}{
			"auto_deploy": true,
			"owner_id":    "user-1",
			"tenant_id":   "org-1",
			"spec": map[string]interface{}{
				"name":     "managed-kombify-me",
				"stackkit": "cloud-kit",
				"provider": "cloud",
				"context":  "cloud",
				"domain":   "kombify.me",
				"network": map[string]interface{}{
					"mode": "public",
				},
				"metadata": map[string]interface{}{
					"address_mode":             "kombify-me",
					"provider_id":              "ionos",
					"server_provisioning_mode": "kombify-cloud",
					"server_connection_mode":   "managed-subscription",
					"runtime_lane":             "monthly-runtime",
					"stackkit_catalog_ref":     "cloud-kit",
				},
				"services": map[string]interface{}{
					"coolify": map[string]interface{}{"enabled": true},
				},
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	if err := handler(context.Background(), job, queue); err != nil {
		t.Fatalf("ProvisionHandler auto deploy failed: %v", err)
	}
	if len(leaseManager.requests) != 1 {
		t.Fatalf("lease requests = %d, want 1", len(leaseManager.requests))
	}
	if leaseManager.requests[0].Provider != "ionos" {
		t.Fatalf("lease provider = %q, want ionos", leaseManager.requests[0].Provider)
	}
	if len(stackKitGenerator.requests) != 1 {
		t.Fatalf("StackKits artifact requests = %d, want 1", len(stackKitGenerator.requests))
	}
	if stackKitGenerator.requests[0].StackSpecPath == "" {
		t.Fatal("StackKits artifact request missing stack-spec.yaml path")
	}
	stackSpecBytes, err := os.ReadFile(stackKitGenerator.requests[0].StackSpecPath)
	if err != nil {
		t.Fatalf("read StackKits handoff spec: %v", err)
	}
	stackSpecText := string(stackSpecBytes)
	if !strings.Contains(stackSpecText, "domain: kombify.me") {
		t.Fatalf("StackKits handoff spec missing kombify.me domain:\n%s", stackSpecText)
	}
	if strings.Contains(stackSpecText, "subdomainPrefix:") {
		t.Fatalf("StackKits handoff spec preallocated a kombify.me prefix:\n%s", stackSpecText)
	}
	if !strings.Contains(stackSpecText, "ip: 203.0.113.30") || !strings.Contains(stackSpecText, "host: 203.0.113.30") {
		t.Fatalf("StackKits handoff spec missing managed runtime target:\n%s", stackSpecText)
	}
	if !strings.Contains(stackSpecText, "runtime_ssh_host: 203.0.113.30") || !strings.Contains(stackSpecText, "runtime_public_ip: 203.0.113.30") {
		t.Fatalf("StackKits handoff spec missing managed runtime target metadata:\n%s", stackSpecText)
	}
	if strings.Contains(stackSpecText, "test-private-key") {
		t.Fatalf("StackKits handoff spec leaked runtime private key:\n%s", stackSpecText)
	}
	tfvarsBytes, err := os.ReadFile(filepath.Join(specBaseDir, "stack-managed-kombify-me", "tofu", "terraform.tfvars.json"))
	if err != nil {
		t.Fatalf("read generated tfvars: %v", err)
	}
	tfvars := string(tfvarsBytes)
	if strings.Contains(tfvars, "sh-managed-registered") || strings.Contains(tfvars, "kombify_me_registered") {
		t.Fatalf("terraform.tfvars.json contains TechStack-owned kombify.me registration proof:\n%s", tfvars)
	}
	if !strings.Contains(tfvars, `"domain": "kombify.me"`) {
		t.Fatalf("terraform.tfvars.json missing kombify.me domain intent for StackKits:\n%s", tfvars)
	}
	if _, ok := job.Result["kombify_me"]; ok {
		t.Fatalf("job.Result[kombify_me] = %#v, want StackKits-owned outputs only", job.Result["kombify_me"])
	}
	resultJSON, err := json.Marshal(job.Result)
	if err != nil {
		t.Fatalf("marshal job result: %v", err)
	}
	if strings.Contains(string(resultJSON), `"provider_id":"centron"`) {
		t.Fatalf("IONOS kombify.me result leaked centron provider: %s", resultJSON)
	}
	if strings.Contains(string(resultJSON), `"lease_provider"`) || strings.Contains(string(resultJSON), `"simulate_provider_id"`) {
		t.Fatalf("IONOS kombify.me result emitted legacy provider fields: %s", resultJSON)
	}
}

func TestProvisionHandler_AutoDeployManagedKombifyMeOutageFailsBeforeRuntimeRollout(t *testing.T) {
	order := []string{}
	leaseManager := &fakeManagedLeaseManager{result: &ManagedLeaseResult{
		LeaseID:      "lease-ionos",
		Provider:     "ionos",
		DesiredState: "running",
		Phase:        RuntimePhaseLeaseReady,
		Target: &ManagedRuntimeTarget{
			Host:     "203.0.113.10",
			PublicIP: "203.0.113.10",
			SSHUser:  "ubuntu",
			SSHPort:  22,
			Source:   "test-lease",
		},
	}}
	stackKitGenerator := &kombifyMeOutageStackKitGenerator{}
	cfg := &ProvisionConfig{
		WorkDir:             t.TempDir(),
		SpecBaseDir:         t.TempDir(),
		StackKitsDir:        writeJobsTestStackKitsDir(t),
		AutoDeployAdmission: allowAutoDeployAdmissionForTest,
		RuntimeActions: RuntimeActions{
			LeaseManager:          leaseManager,
			RuntimeTargetResolver: &fakeManagedRuntimeTargetResolver{},
			StackKitGenerator:     stackKitGenerator,
			SimulationGate:        &fakeRuntimeRunner{name: "simulate", order: &order},
			RolloutRunner:         &fakeRuntimeRunner{name: "rollout", order: &order},
			RolloutVerifier:       &fakeRuntimeRunner{name: "verify", order: &order, result: stackKitIdentityHandoffResult("stack-managed-kombify-me-outage")},
			RestoreDrill:          &fakeRuntimeRunner{name: "restore", order: &order, result: map[string]interface{}{"status": "verified"}},
		},
	}

	handler := ProvisionHandler(cfg)
	job := &Job{
		ID:       "test-job-managed-kombify-me-provider-offline",
		Type:     JobTypeProvision,
		TargetID: "stack-managed-kombify-me-outage",
		Payload: map[string]interface{}{
			"auto_deploy": true,
			"owner_id":    "user-1",
			"tenant_id":   "org-1",
			"spec": map[string]interface{}{
				"name":     "managed-kombify-me-provider-offline",
				"stackkit": "cloud-kit",
				"provider": "cloud",
				"context":  "cloud",
				"domain":   "kombify.me",
				"network": map[string]interface{}{
					"mode": "public",
				},
				"metadata": map[string]interface{}{
					"address_mode":             "kombify-me",
					"provider_id":              "ionos",
					"server_provisioning_mode": "kombify-cloud",
					"server_connection_mode":   "managed-subscription",
					"runtime_lane":             "monthly-runtime",
					"stackkit_catalog_ref":     "cloud-kit",
				},
				"services": map[string]interface{}{
					"coolify": map[string]interface{}{"enabled": true},
				},
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	err := handler(context.Background(), job, queue)
	if err == nil {
		t.Fatal("ProvisionHandler should fail when kombify.me artifacts cannot be generated for managed VPS")
	}
	if !strings.Contains(err.Error(), "provider_offline") {
		t.Fatalf("ProvisionHandler error = %v, want provider_offline diagnostic", err)
	}
	if len(stackKitGenerator.requests) != 1 {
		t.Fatalf("StackKits artifact requests = %d, want one kombify.me attempt", len(stackKitGenerator.requests))
	}
	if len(stackKitGenerator.specs) != 1 {
		t.Fatalf("captured StackKits handoff specs = %d, want 1", len(stackKitGenerator.specs))
	}
	if !strings.Contains(stackKitGenerator.specs[0], "domain: kombify.me") {
		t.Fatalf("StackKits handoff spec did not attempt kombify.me:\n%s", stackKitGenerator.specs[0])
	}
	if strings.Contains(stackKitGenerator.specs[0], "domain: home.localhost") ||
		strings.Contains(stackKitGenerator.specs[0], "address_mode: provider-direct") {
		t.Fatalf("StackKits handoff spec downgraded to provider-direct:\n%s", stackKitGenerator.specs[0])
	}
	if got := strings.Join(order, ","); got != "" {
		t.Fatalf("runtime action order = %s, want no runtime rollout after address failure", got)
	}
}

func TestProvisionHandler_AutoDeployManagedKombifyMeQuotaFailsAtGenerateIaCWithLeaseMetadata(t *testing.T) {
	order := []string{}
	leaseManager := &fakeManagedLeaseManager{result: &ManagedLeaseResult{
		LeaseID:      "lease-ionos",
		Provider:     "ionos",
		DesiredState: "running",
		Phase:        RuntimePhaseLeaseReady,
		Target: &ManagedRuntimeTarget{
			Host:     "203.0.113.10",
			PublicIP: "203.0.113.10",
			SSHUser:  "ubuntu",
			SSHPort:  22,
			Source:   "test-lease",
		},
	}}
	incidentError := strings.Join([]string{
		"StackKits CLI generate failed: exit status 1: WARN legacy stackkit install mode normalized from=simple to=bootstrapped",
		"Registering subdomains on kombify.me...",
		`Error: kombify.me registration failed and no subdomainPrefix is configured: auto-register base subdomain: API error 429: {"error":"base subdomain limit reached (max 5 per user)"}`,
	}, "\n")
	stackKitGenerator := &kombifyMeOutageStackKitGenerator{errText: incidentError}
	cfg := &ProvisionConfig{
		WorkDir:             t.TempDir(),
		SpecBaseDir:         t.TempDir(),
		StackKitsDir:        writeJobsTestStackKitsDir(t),
		AutoDeployAdmission: allowAutoDeployAdmissionForTest,
		RuntimeActions: RuntimeActions{
			LeaseManager:          leaseManager,
			RuntimeTargetResolver: &fakeManagedRuntimeTargetResolver{},
			StackKitGenerator:     stackKitGenerator,
			SimulationGate:        &fakeRuntimeRunner{name: "simulate", order: &order},
			RolloutRunner:         &fakeRuntimeRunner{name: "rollout", order: &order},
			RolloutVerifier:       &fakeRuntimeRunner{name: "verify", order: &order, result: stackKitIdentityHandoffResult("stack-managed-kombify-me-quota")},
			RestoreDrill:          &fakeRuntimeRunner{name: "restore", order: &order, result: map[string]interface{}{"status": "verified"}},
		},
	}

	handler := ProvisionHandler(cfg)
	job := &Job{
		ID:       "test-job-managed-kombify-me-quota",
		Type:     JobTypeProvision,
		TargetID: "stack-managed-kombify-me-quota",
		Payload: map[string]interface{}{
			"auto_deploy": true,
			"owner_id":    "user-1",
			"tenant_id":   "org-1",
			"spec": map[string]interface{}{
				"name":     "managed-kombify-me-quota",
				"stackkit": "cloud-kit",
				"provider": "cloud",
				"context":  "cloud",
				"domain":   "kombify.me",
				"network": map[string]interface{}{
					"mode": "public",
				},
				"metadata": map[string]interface{}{
					"address_mode":             "kombify-me",
					"provider_id":              "ionos",
					"server_provisioning_mode": "kombify-cloud",
					"server_connection_mode":   "managed-subscription",
					"runtime_lane":             "monthly-runtime",
					"stackkit_catalog_ref":     "cloud-kit",
				},
				"services": map[string]interface{}{
					"coolify": map[string]interface{}{"enabled": true},
				},
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	err := handler(context.Background(), job, queue)
	if err == nil {
		t.Fatal("ProvisionHandler should fail when kombify.me base subdomain quota blocks artifact generation")
	}
	if !strings.Contains(err.Error(), "base subdomain limit reached") {
		t.Fatalf("ProvisionHandler error = %v, want kombify.me quota diagnostic", err)
	}
	if job.Step != StepGenerateIaC {
		t.Fatalf("job.Step = %q, want %q", job.Step, StepGenerateIaC)
	}
	if got := job.Result["runtime_phase"]; got != string(RuntimePhaseLeaseReady) {
		t.Fatalf("runtime_phase = %v, want %s", got, RuntimePhaseLeaseReady)
	}
	if got := job.Result["lease_id"]; got != "lease-ionos" {
		t.Fatalf("lease_id = %v, want lease-ionos", got)
	}
	if got := job.Result["provider_id"]; got != "ionos" {
		t.Fatalf("provider_id = %v, want ionos", got)
	}
	if got := strings.Join(order, ","); got != "" {
		t.Fatalf("runtime action order = %s, want no runtime rollout after artifact generation failure", got)
	}
	if len(stackKitGenerator.requests) != 1 {
		t.Fatalf("StackKits artifact requests = %d, want one kombify.me attempt", len(stackKitGenerator.requests))
	}
}

func TestProvisionHandler_AutoDeployManagedCloudFailsWithoutRuntimeAddress(t *testing.T) {
	cfg := &ProvisionConfig{
		WorkDir:             t.TempDir(),
		SpecBaseDir:         t.TempDir(),
		StackKitsDir:        writeJobsTestStackKitsDir(t),
		AutoDeployAdmission: allowAutoDeployAdmissionForTest,
		RuntimeActions: RuntimeActions{
			LeaseManager: &fakeManagedLeaseManager{result: &ManagedLeaseResult{
				LeaseID:      "lease-no-address",
				Provider:     "centron",
				DesiredState: "running",
				Phase:        RuntimePhaseLeaseReady,
			}},
			SimulationGate:  &fakeRuntimeRunner{name: "simulate"},
			RolloutRunner:   &fakeRuntimeRunner{name: "rollout"},
			RolloutVerifier: &fakeRuntimeRunner{name: "verify"},
			RestoreDrill:    &fakeRuntimeRunner{name: "restore"},
		},
	}

	handler := ProvisionHandler(cfg)
	job := &Job{
		ID:       "test-job-managed-cloud-auto-deploy-missing-address",
		Type:     JobTypeProvision,
		TargetID: "stack-managed-auto-missing-address",
		Payload: map[string]interface{}{
			"auto_deploy": true,
			"owner_id":    "user-1",
			"tenant_id":   "org-1",
			"spec": map[string]interface{}{
				"name":        "managed-stack",
				"provider":    "cloud",
				"provider_id": "centron",
				"goals": map[string]interface{}{
					"storage": true,
				},
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	err := handler(context.Background(), job, queue)
	if err == nil {
		t.Fatal("expected missing managed runtime address to fail auto deploy")
	}
	if !strings.Contains(err.Error(), "managed runtime lease address missing") {
		t.Fatalf("error = %v, want missing address diagnostic", err)
	}
	if job.Step != StepPrepareRollout {
		t.Fatalf("job step = %q, want %q", job.Step, StepPrepareRollout)
	}
}

func TestProvisionHandler_AutoDeployYieldsWhileManagedRuntimeTargetIsPending(t *testing.T) {
	order := []string{}
	resolver := &sequenceManagedRuntimeTargetResolver{
		errs: []error{errors.New("enrollment pending")},
		targets: []*ManagedRuntimeTarget{
			nil,
			{
				Host:          "203.0.113.55",
				PublicIP:      "203.0.113.55",
				SSHUser:       "ubuntu",
				SSHPort:       22,
				SSHPrivateKey: "test-private-key",
				Source:        "test-enrollment",
			},
		},
	}
	cfg := &ProvisionConfig{
		WorkDir:                          t.TempDir(),
		SpecBaseDir:                      t.TempDir(),
		StackKitsDir:                     writeJobsTestStackKitsDir(t),
		ManagedRuntimeTargetWaitTimeout:  100 * time.Millisecond,
		ManagedRuntimeTargetPollInterval: time.Millisecond,
		AutoDeployAdmission:              allowAutoDeployAdmissionForTest,
		RuntimeActions: RuntimeActions{
			StackKitGenerator: &fakeStackKitArtifactGenerator{},
			LeaseManager: &fakeManagedLeaseManager{result: &ManagedLeaseResult{
				LeaseID:      "lease-await-target",
				Provider:     "centron",
				DesiredState: "running",
				Phase:        RuntimePhaseLeaseReady,
			}},
			RuntimeTargetResolver: resolver,
			SimulationGate:        &fakeRuntimeRunner{name: "simulate", order: &order, result: map[string]interface{}{"status": "accepted"}},
			RolloutRunner:         &fakeRuntimeRunner{name: "rollout", order: &order, result: map[string]interface{}{"status": "applied"}},
			RolloutVerifier:       &fakeRuntimeRunner{name: "verify", order: &order, result: stackKitIdentityHandoffResult("stack-await-target")},
			RestoreDrill:          &fakeRuntimeRunner{name: "restore", order: &order, result: map[string]interface{}{"status": "verified"}},
		},
	}

	handler := ProvisionHandler(cfg)
	job := &Job{
		ID:       "test-job-managed-cloud-await-target",
		Type:     JobTypeProvision,
		TargetID: "stack-await-target",
		Payload: map[string]interface{}{
			"auto_deploy": true,
			"owner_id":    "user-1",
			"tenant_id":   "org-1",
			"spec": map[string]interface{}{
				"name":        "managed-stack",
				"provider":    "cloud",
				"provider_id": "centron",
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	err := handler(context.Background(), job, queue)
	var pending *ManagedRuntimeEnrollmentPendingError
	if !errors.As(err, &pending) {
		t.Fatalf("first attempt error = %T %v, want non-terminal enrollment wait", err, err)
	}
	if len(resolver.requests) != 1 {
		t.Fatalf("resolver requests = %d, want one bounded observation before yielding", len(resolver.requests))
	}
	if len(order) != 0 {
		t.Fatalf("runtime actions before target resolution = %v, want none", order)
	}
	if waitStartedAt := stringFromMap(job.Snapshot().Result, managedRuntimeEnrollmentWaitStartedAtField); waitStartedAt == "" {
		t.Fatal("first enrollment wait did not persist its cumulative wait start")
	}

	if err := handler(context.Background(), job, queue); err != nil {
		t.Fatalf("second queue attempt should continue after target enrollment, got %v", err)
	}
	if len(resolver.requests) != 2 {
		t.Fatalf("resolver requests = %d, want one request per queue attempt", len(resolver.requests))
	}
	if got := job.Result[metadataKeyRuntimeSSHHost]; got != "203.0.113.55" {
		t.Fatalf("%s = %v, want resolved runtime target", metadataKeyRuntimeSSHHost, got)
	}
	if waitStartedAt := stringFromMap(job.Snapshot().Result, managedRuntimeEnrollmentWaitStartedAtField); waitStartedAt != "" {
		t.Fatalf("successful target resolution retained wait start %q", waitStartedAt)
	}
	if strings.Join(order, ",") != "simulate,rollout,verify,restore" {
		t.Fatalf("runtime action order = %v", order)
	}
}

func TestProvisionHandler_AutoDeployQueueResumePreservesPreparedResult(t *testing.T) {
	order := []string{}
	leaseManager := &fakeManagedLeaseManager{result: &ManagedLeaseResult{
		LeaseID:      "lease-queue-resume",
		Provider:     "centron",
		DesiredState: "running",
		Phase:        RuntimePhaseLeaseReady,
	}}
	resolver := &sequenceManagedRuntimeTargetResolver{
		errs: []error{errors.New("enrollment pending")},
		targets: []*ManagedRuntimeTarget{
			nil,
			{
				Host:          "203.0.113.56",
				PublicIP:      "203.0.113.56",
				SSHUser:       "ubuntu",
				SSHPort:       22,
				SSHPrivateKey: "test-private-key",
				Source:        "test-enrollment",
			},
		},
	}
	cfg := &ProvisionConfig{
		WorkDir:                          t.TempDir(),
		SpecBaseDir:                      t.TempDir(),
		StackKitsDir:                     writeJobsTestStackKitsDir(t),
		ManagedRuntimeTargetWaitTimeout:  100 * time.Millisecond,
		ManagedRuntimeTargetPollInterval: time.Millisecond,
		AutoDeployAdmission:              allowAutoDeployAdmissionForTest,
		RuntimeActions: RuntimeActions{
			StackKitGenerator:     &fakeStackKitArtifactGenerator{},
			LeaseManager:          leaseManager,
			RuntimeTargetResolver: resolver,
			SimulationGate:        &fakeRuntimeRunner{name: "simulate", order: &order, result: map[string]interface{}{"status": "accepted"}},
			RolloutRunner:         &fakeRuntimeRunner{name: "rollout", order: &order, result: map[string]interface{}{"status": "applied"}},
			RolloutVerifier:       &fakeRuntimeRunner{name: "verify", order: &order, result: stackKitIdentityHandoffResult("stack-queue-resume")},
			RestoreDrill:          &fakeRuntimeRunner{name: "restore", order: &order, result: map[string]interface{}{"status": "verified"}},
		},
	}

	queue := NewQueue(1, nil)
	queue.RegisterHandler(JobTypeProvision, ProvisionHandler(cfg))
	queue.RegisterHandler(JobTypeDeploy, DeployHandler(cfg))
	job := &Job{
		ID:       "test-job-managed-cloud-queue-resume",
		Type:     JobTypeProvision,
		TargetID: "stack-queue-resume",
		Payload: map[string]interface{}{
			"auto_deploy": true,
			"owner_id":    "user-1",
			"tenant_id":   "org-1",
			"spec": map[string]interface{}{
				"name":        "managed-stack",
				"provider":    "cloud",
				"provider_id": "centron",
			},
		},
	}
	if err := queue.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	queue.Start(context.Background())
	defer queue.Stop()

	deadline := time.Now().Add(5 * time.Second)
	var snapshot JobSnapshot
	for time.Now().Before(deadline) {
		snapshot = job.Snapshot()
		if isTerminalJobState(snapshot.State) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if snapshot.State != JobStateCompleted {
		t.Fatalf("queue-resumed auto-deploy state=%q error=%q details=%q", snapshot.State, snapshot.Error, snapshot.ErrorDetails)
	}
	if snapshot.Type != JobTypeDeploy || len(leaseManager.requests) != 1 {
		t.Fatalf("handler transition type=%q lease requests=%d, want deploy and one provision pass", snapshot.Type, len(leaseManager.requests))
	}
	if len(resolver.requests) != 2 {
		t.Fatalf("resolver requests=%d, want one observation per queue attempt", len(resolver.requests))
	}
	if got := stringFromMap(snapshot.Result, leaseIDField); got != "lease-queue-resume" {
		t.Fatalf("preserved lease_id=%q, want lease-queue-resume", got)
	}
	if got := stringFromMap(snapshot.Result, "owner_id"); got != "user-1" {
		t.Fatalf("preserved owner_id=%q, want user-1", got)
	}
	if got := stringFromMap(snapshot.Result, tenantIDField); got != "org-1" {
		t.Fatalf("preserved tenant_id=%q, want org-1", got)
	}
	if token := stringFromMap(snapshot.Result, "registration_token"); token == "" {
		t.Fatal("prepared registration_token was lost across queue resume")
	}
	if snapshot.Result["requirements"] == nil {
		t.Fatal("prepared requirements were lost across queue resume")
	}
	if got := stringFromMap(snapshot.Result, metadataKeyBillingMode); got != billingModeSubscription {
		t.Fatalf("preserved billing_mode=%q, want %q", got, billingModeSubscription)
	}
	if strings.Join(order, ",") != "simulate,rollout,verify,restore" {
		t.Fatalf("runtime action order = %v", order)
	}
}

func TestProvisionHandler_AutoDeployGuardResumeReusesPreparedLease(t *testing.T) {
	order := []string{}
	leaseManager := &fakeManagedLeaseManager{result: &ManagedLeaseResult{
		LeaseID:      "lease-guard-resume",
		Provider:     "ionos",
		DesiredState: "running",
		Phase:        RuntimePhaseLeaseReady,
	}}
	admissionCalls := 0
	cfg := &ProvisionConfig{
		WorkDir:                          t.TempDir(),
		SpecBaseDir:                      t.TempDir(),
		StackKitsDir:                     writeJobsTestStackKitsDir(t),
		ManagedRuntimeTargetWaitTimeout:  250 * time.Millisecond,
		ManagedRuntimeTargetPollInterval: time.Millisecond,
		AutoDeployAdmission: func(context.Context, AutoDeployAdmissionRequest) error {
			admissionCalls++
			if admissionCalls == 1 {
				return errors.New("guard heartbeat is not fresh yet")
			}
			return nil
		},
		RuntimeActions: RuntimeActions{
			StackKitGenerator: &fakeStackKitArtifactGenerator{},
			LeaseManager:      leaseManager,
			RuntimeTargetResolver: &sequenceManagedRuntimeTargetResolver{targets: []*ManagedRuntimeTarget{{
				Host:          "203.0.113.57",
				PublicIP:      "203.0.113.57",
				SSHUser:       "ubuntu",
				SSHPort:       22,
				SSHPrivateKey: "test-private-key",
				Source:        "test-enrollment",
			}}},
			SimulationGate:  &fakeRuntimeRunner{name: "simulate", order: &order, result: map[string]interface{}{"status": "accepted"}},
			RolloutRunner:   &fakeRuntimeRunner{name: "rollout", order: &order, result: map[string]interface{}{"status": "applied"}},
			RolloutVerifier: &fakeRuntimeRunner{name: "verify", order: &order, result: stackKitIdentityHandoffResult("stack-guard-resume")},
			RestoreDrill:    &fakeRuntimeRunner{name: "restore", order: &order, result: map[string]interface{}{"status": "verified"}},
		},
	}

	queue := NewQueue(1, nil)
	queue.RegisterHandler(JobTypeProvision, ProvisionHandler(cfg))
	queue.RegisterHandler(JobTypeDeploy, DeployHandler(cfg))
	job := &Job{
		ID:       "test-job-managed-cloud-guard-resume",
		Type:     JobTypeProvision,
		TargetID: "stack-guard-resume",
		Payload: map[string]interface{}{
			"auto_deploy": true,
			"owner_id":    "user-1",
			"tenant_id":   "org-1",
			"spec": map[string]interface{}{
				"name":        "managed-stack",
				"provider":    "cloud",
				"provider_id": "ionos",
			},
		},
	}
	if err := queue.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	queue.Start(context.Background())
	defer queue.Stop()

	deadline := time.Now().Add(5 * time.Second)
	var snapshot JobSnapshot
	for time.Now().Before(deadline) {
		snapshot = job.Snapshot()
		if isTerminalJobState(snapshot.State) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if snapshot.State != JobStateCompleted {
		t.Fatalf("guard-resumed auto-deploy state=%q error=%q details=%q", snapshot.State, snapshot.Error, snapshot.ErrorDetails)
	}
	if admissionCalls < 2 {
		t.Fatalf("admission calls=%d, want initial wait and resumed admission", admissionCalls)
	}
	if len(leaseManager.requests) != 1 {
		t.Fatalf("lease requests=%d, want prepared lease reused after guard wait", len(leaseManager.requests))
	}
	if got := stringFromMap(snapshot.Result, leaseIDField); got != "lease-guard-resume" {
		t.Fatalf("preserved lease_id=%q, want lease-guard-resume", got)
	}
	if strings.Join(order, ",") != "simulate,rollout,verify,restore" {
		t.Fatalf("runtime action order = %v", order)
	}
}

func TestProvisionHandler_AutoDeployBoundsManagedRuntimeTargetResolverHang(t *testing.T) {
	t.Setenv("TECHSTACK_MANAGED_RUNTIME_RESOLVE_TIMEOUT", "10ms")
	resolver := &blockingManagedRuntimeTargetResolver{}
	cfg := &ProvisionConfig{
		WorkDir:                          t.TempDir(),
		SpecBaseDir:                      t.TempDir(),
		StackKitsDir:                     writeJobsTestStackKitsDir(t),
		ManagedRuntimeTargetWaitTimeout:  50 * time.Millisecond,
		ManagedRuntimeTargetPollInterval: 10 * time.Millisecond,
		AutoDeployAdmission:              allowAutoDeployAdmissionForTest,
		RuntimeActions: RuntimeActions{
			LeaseManager: &fakeManagedLeaseManager{result: &ManagedLeaseResult{
				LeaseID:      "lease-hanging-target",
				Provider:     "centron",
				DesiredState: "running",
				Phase:        RuntimePhaseLeaseReady,
			}},
			RuntimeTargetResolver: resolver,
			SimulationGate:        &fakeRuntimeRunner{name: "simulate"},
			RolloutRunner:         &fakeRuntimeRunner{name: "rollout"},
			RolloutVerifier:       &fakeRuntimeRunner{name: "verify"},
			RestoreDrill:          &fakeRuntimeRunner{name: "restore"},
		},
	}

	handler := ProvisionHandler(cfg)
	job := &Job{
		ID:       "test-job-managed-cloud-hanging-target",
		Type:     JobTypeProvision,
		TargetID: "stack-hanging-target",
		Payload: map[string]interface{}{
			"auto_deploy": true,
			"owner_id":    "user-1",
			"tenant_id":   "org-1",
			"spec": map[string]interface{}{
				"name":        "managed-stack",
				"provider":    "cloud",
				"provider_id": "centron",
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	done := make(chan error, 1)
	go func() {
		done <- handler(context.Background(), job, queue)
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("hanging resolver did not fail within bounded test window")
	}
	if err == nil {
		t.Fatal("expected hanging resolver to return a non-terminal wait signal")
	}
	var pending *ManagedRuntimeEnrollmentPendingError
	if !errors.As(err, &pending) || pending.LeaseID != "lease-hanging-target" {
		t.Fatalf("error = %T %v, want typed enrollment pending error", err, err)
	}
	var waitErr *JobWaitError
	if !errors.As(err, &waitErr) || waitErr.Reason != WaitReasonManagedRuntimeEnrollment || waitErr.ResumeAfter <= 0 || waitErr.ResumeAfter > 30*time.Second {
		t.Fatalf("wait error = %#v, want bounded waiting_enrollment resume", waitErr)
	}
	var provisionErr *ProvisionError
	if errors.As(err, &provisionErr) {
		t.Fatalf("enrollment wait was flattened into ProvisionError: %#v", provisionErr)
	}
	if !strings.Contains(fmt.Sprintf("%+v", waitErr.Cause), "managed runtime lease target is not available yet") {
		t.Fatalf("cause = %v, want managed runtime pending diagnostic", waitErr.Cause)
	}
	if len(resolver.requests) == 0 {
		t.Fatal("expected resolver to be called")
	}
	if job.Step != StepPrepareRollout {
		t.Fatalf("job step = %q, want %q", job.Step, StepPrepareRollout)
	}
}

func TestManagedRuntimeTargetWaitStopsAfterCumulativeWindow(t *testing.T) {
	resolver := &fakeManagedRuntimeTargetResolver{err: errors.New("enrollment still pending")}
	cfg := &ProvisionConfig{
		ManagedRuntimeTargetWaitTimeout:  50 * time.Millisecond,
		ManagedRuntimeTargetPollInterval: time.Millisecond,
		RuntimeActions: RuntimeActions{
			RuntimeTargetResolver: resolver,
		},
	}
	job := &Job{
		ID:         "job-enrollment-wait-expired",
		Type:       JobTypeDeploy,
		TargetID:   "stack-enrollment-wait-expired",
		TargetName: "Expired Enrollment",
		Payload: map[string]interface{}{
			"owner_id":  "user-1",
			"tenant_id": "tenant-1",
		},
		Result: map[string]interface{}{
			leaseIDField: "lease-enrollment-wait-expired",
			managedRuntimeEnrollmentWaitStartedAtField: time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano),
		},
	}

	target, err := resolveManagedRuntimeTargetWithWait(context.Background(), cfg, job, &core.KombinationSpec{}, nil)
	if target != nil {
		t.Fatalf("target = %#v, want nil after cumulative wait timeout", target)
	}
	if !errors.Is(err, ErrManagedRuntimeEnrollmentFailed) {
		t.Fatalf("error = %T %v, want ErrManagedRuntimeEnrollmentFailed", err, err)
	}
	var pending *ManagedRuntimeEnrollmentPendingError
	if errors.As(err, &pending) {
		t.Fatalf("expired wait returned non-terminal signal: %#v", pending)
	}
	if len(resolver.requests) != 0 {
		t.Fatalf("resolver requests = %d, want no new call after the cumulative deadline", len(resolver.requests))
	}
	if waitStartedAt := stringFromMap(job.Snapshot().Result, managedRuntimeEnrollmentWaitStartedAtField); waitStartedAt != "" {
		t.Fatalf("terminal timeout retained wait start %q", waitStartedAt)
	}
}

func TestProvisionHandler_AutoDeployStopsWaitingWhenManagedRuntimeEnrollmentFailed(t *testing.T) {
	resolver := &fakeManagedRuntimeTargetResolver{
		err: fmt.Errorf("%w for lease %q: ionos quota exhausted", ErrManagedRuntimeEnrollmentFailed, "lease-failed"),
	}
	cfg := &ProvisionConfig{
		WorkDir:                          t.TempDir(),
		SpecBaseDir:                      t.TempDir(),
		StackKitsDir:                     writeJobsTestStackKitsDir(t),
		ManagedRuntimeTargetWaitTimeout:  time.Second,
		ManagedRuntimeTargetPollInterval: 10 * time.Millisecond,
		AutoDeployAdmission:              allowAutoDeployAdmissionForTest,
		RuntimeActions: RuntimeActions{
			LeaseManager: &fakeManagedLeaseManager{result: &ManagedLeaseResult{
				LeaseID:      "lease-failed",
				Provider:     "ionos",
				DesiredState: "running",
				Phase:        RuntimePhaseLeaseReady,
			}},
			RuntimeTargetResolver: resolver,
			SimulationGate:        &fakeRuntimeRunner{name: "simulate"},
			RolloutRunner:         &fakeRuntimeRunner{name: "rollout"},
			RolloutVerifier:       &fakeRuntimeRunner{name: "verify"},
			RestoreDrill:          &fakeRuntimeRunner{name: "restore"},
		},
	}

	handler := ProvisionHandler(cfg)
	job := &Job{
		ID:       "test-job-managed-cloud-terminal-enrollment-failure",
		Type:     JobTypeProvision,
		TargetID: "stack-terminal-enrollment-failure",
		Payload: map[string]interface{}{
			"auto_deploy": true,
			"owner_id":    "user-1",
			"tenant_id":   "org-1",
			"spec": map[string]interface{}{
				"name":        "managed-stack",
				"provider":    "cloud",
				"provider_id": "centron",
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	err := handler(context.Background(), job, queue)
	if err == nil {
		t.Fatal("expected terminal enrollment failure")
	}
	if !strings.Contains(err.Error(), "ionos quota exhausted") {
		t.Fatalf("error = %v, want provider failure cause", err)
	}
	if got := len(resolver.requests); got != 1 {
		t.Fatalf("resolver requests = %d, want immediate terminal failure without retry", got)
	}
	if job.Step != StepPrepareRollout {
		t.Fatalf("job step = %q, want %q", job.Step, StepPrepareRollout)
	}
}

func TestProvisionHandler_AutoDeployStopsWaitingWhenMonthlyRuntimeFeatureDisabled(t *testing.T) {
	resolver := &fakeManagedRuntimeTargetResolver{
		err: fmt.Errorf("%w: %s", monthlyruntime.ErrFeatureDisabled, "sim.monthly.runtime.standard"),
	}
	cfg := &ProvisionConfig{
		WorkDir:                          t.TempDir(),
		SpecBaseDir:                      t.TempDir(),
		StackKitsDir:                     writeJobsTestStackKitsDir(t),
		ManagedRuntimeTargetWaitTimeout:  time.Second,
		ManagedRuntimeTargetPollInterval: 10 * time.Millisecond,
		AutoDeployAdmission:              allowAutoDeployAdmissionForTest,
		RuntimeActions: RuntimeActions{
			LeaseManager: &fakeManagedLeaseManager{result: &ManagedLeaseResult{
				LeaseID:      "lease-feature-disabled",
				Provider:     "ionos",
				DesiredState: "running",
				Phase:        RuntimePhaseLeaseReady,
			}},
			RuntimeTargetResolver: resolver,
			SimulationGate:        &fakeRuntimeRunner{name: "simulate"},
			RolloutRunner:         &fakeRuntimeRunner{name: "rollout"},
			RolloutVerifier:       &fakeRuntimeRunner{name: "verify"},
			RestoreDrill:          &fakeRuntimeRunner{name: "restore"},
		},
	}

	handler := ProvisionHandler(cfg)
	job := &Job{
		ID:       "test-job-managed-cloud-feature-disabled",
		Type:     JobTypeProvision,
		TargetID: "stack-feature-disabled",
		Payload: map[string]interface{}{
			"auto_deploy": true,
			"owner_id":    "user-1",
			"tenant_id":   "org-1",
			"spec": map[string]interface{}{
				"name":        "managed-stack",
				"provider":    "cloud",
				"provider_id": "centron",
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	err := handler(context.Background(), job, queue)
	if err == nil {
		t.Fatal("expected feature-disabled resolver error")
	}
	if !strings.Contains(err.Error(), monthlyruntime.ErrFeatureDisabled.Error()) {
		t.Fatalf("error = %v, want ErrFeatureDisabled details", err)
	}
	if got := len(resolver.requests); got != 1 {
		t.Fatalf("resolver requests = %d, want immediate terminal failure without retry", got)
	}
}

func TestProvisionHandler_AutoDeployManagedCloudUsesOwnerAsLocalTenantFallback(t *testing.T) {
	order := []string{}
	lease := &fakeManagedLeaseManager{result: &ManagedLeaseResult{
		LeaseID:      "lease-local-tenant",
		Provider:     "centron",
		DesiredState: "running",
		Phase:        RuntimePhaseLeaseReady,
	}}
	resolver := &fakeManagedRuntimeTargetResolver{}
	cfg := &ProvisionConfig{
		WorkDir:             t.TempDir(),
		SpecBaseDir:         t.TempDir(),
		StackKitsDir:        writeJobsTestStackKitsDir(t),
		AutoDeployAdmission: allowAutoDeployAdmissionForTest,
		RuntimeActions: RuntimeActions{
			StackKitGenerator:     &fakeStackKitArtifactGenerator{},
			LeaseManager:          lease,
			RuntimeTargetResolver: resolver,
			SimulationGate:        &fakeRuntimeRunner{name: "simulate", order: &order},
			RolloutRunner:         &fakeRuntimeRunner{name: "rollout", order: &order},
			RolloutVerifier:       &fakeRuntimeRunner{name: "verify", order: &order, result: stackKitIdentityHandoffResult("stack-managed-local-tenant")},
			RestoreDrill:          &fakeRuntimeRunner{name: "restore", order: &order},
		},
	}

	handler := ProvisionHandler(cfg)
	job := &Job{
		ID:       "test-job-managed-cloud-local-tenant",
		Type:     JobTypeProvision,
		TargetID: "stack-managed-local-tenant",
		Payload: map[string]interface{}{
			"auto_deploy": true,
			"owner_id":    "user-1",
			"spec": map[string]interface{}{
				"name":        "managed-stack",
				"provider":    "cloud",
				"provider_id": "centron",
				"goals": map[string]interface{}{
					"storage": true,
				},
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	if err := handler(context.Background(), job, queue); err != nil {
		t.Fatalf("ProvisionHandler auto deploy failed: %v", err)
	}
	if len(lease.requests) != 1 {
		t.Fatalf("lease requests = %d, want 1", len(lease.requests))
	}
	if got := lease.requests[0].TenantID; got != "user-1" {
		t.Fatalf("lease tenant_id = %q, want owner fallback", got)
	}
	if len(resolver.requests) != 1 {
		t.Fatalf("resolver requests = %d, want 1", len(resolver.requests))
	}
	if got := resolver.requests[0].TenantID; got != "user-1" {
		t.Fatalf("resolver tenant_id = %q, want owner fallback", got)
	}
	if got := resolver.requests[0].OwnerID; got != "user-1" {
		t.Fatalf("resolver owner_id = %q, want user-1", got)
	}
	if strings.Join(order, ",") != "simulate,rollout,verify,restore" {
		t.Fatalf("runtime action order = %v", order)
	}
}

func TestProvisionHandler_AutoDeploySurfacesMissingRolloutRunner(t *testing.T) {
	cfg := &ProvisionConfig{
		WorkDir:             t.TempDir(),
		SpecBaseDir:         t.TempDir(),
		StackKitsDir:        writeJobsTestStackKitsDir(t),
		AutoDeployAdmission: allowAutoDeployAdmissionForTest,
		RuntimeActions: RuntimeActions{
			StackKitGenerator: &fakeStackKitArtifactGenerator{},
			LeaseManager:      &fakeManagedLeaseManager{},
			SimulationGate:    &fakeRuntimeRunner{name: "simulate"},
			RolloutVerifier:   &fakeRuntimeRunner{name: "verify"},
			RestoreDrill:      &fakeRuntimeRunner{name: "restore"},
		},
	}

	handler := ProvisionHandler(cfg)
	job := &Job{
		ID:       "test-job-managed-cloud-auto-deploy-missing-runner",
		Type:     JobTypeProvision,
		TargetID: "stack-managed-auto-missing-runner",
		Payload: map[string]interface{}{
			"auto_deploy": true,
			"owner_id":    "user-1",
			"tenant_id":   "org-1",
			"spec": map[string]interface{}{
				"name":        "managed-stack",
				"provider":    "cloud",
				"provider_id": "centron",
				"goals": map[string]interface{}{
					"storage": true,
				},
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	err := handler(context.Background(), job, queue)
	if err == nil {
		t.Fatal("expected missing rollout runner to fail auto deploy")
	}
	if !strings.Contains(err.Error(), "StackKits rollout runner is not configured") {
		t.Fatalf("error = %v, want missing rollout runner diagnostic", err)
	}
	if job.Step != StepRolloutRunner {
		t.Fatalf("job step = %q, want %q", job.Step, StepRolloutRunner)
	}
}

func TestProvisionHandler_AutoDeployFailsWhenOwnerHandoffMissing(t *testing.T) {
	// When the orchestrator issues an owner-spec bootstrap token, StackKit
	// is contractually required to return identity, login_gateway, and
	// recovery outputs. A "verified" response without these fields means
	// the freshly provisioned stack has no usable owner login, so the
	// provision job must fail loudly rather than report success.
	specBaseDir := t.TempDir()
	cfg := &ProvisionConfig{
		WorkDir:             t.TempDir(),
		SpecBaseDir:         specBaseDir,
		StackKitsDir:        writeJobsTestStackKitsDir(t),
		AutoDeployAdmission: allowAutoDeployAdmissionForTest,
		RuntimeActions: RuntimeActions{
			StackKitGenerator: &fakeStackKitArtifactGenerator{},
			LeaseManager:      &fakeManagedLeaseManager{},
			SimulationGate:    &fakeRuntimeRunner{name: "simulate", result: map[string]interface{}{"status": "accepted"}},
			RolloutRunner:     &fakeRuntimeRunner{name: "rollout", result: map[string]interface{}{"status": "applied"}},
			RolloutVerifier:   &fakeRuntimeRunner{name: "verify", result: map[string]interface{}{"status": "verified"}},
			RestoreDrill:      &fakeRuntimeRunner{name: "restore", result: map[string]interface{}{"status": "verified"}},
		},
	}

	handler := ProvisionHandler(cfg)
	job := &Job{
		ID:       "test-job-handoff-missing",
		Type:     JobTypeProvision,
		TargetID: "stack-handoff-missing",
		Payload: map[string]interface{}{
			"auto_deploy": true,
			"owner_id":    "user-1",
			"tenant_id":   "org-1",
			"owner_spec_bootstrap": &OwnerSpecBootstrap{
				Endpoint:  "https://techstack.kombify.io/api/v1/stacks/stack-handoff-missing/owner-spec",
				Token:     "test-bootstrap-token",
				ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				Scopes:    []string{"owner_spec:read"},
			},
			"spec": map[string]interface{}{
				"name":        "managed-stack",
				"provider":    "cloud",
				"provider_id": "centron",
				"goals": map[string]interface{}{
					"storage": true,
				},
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	err := handler(context.Background(), job, queue)
	if err == nil {
		t.Fatal("expected provision to fail when stackkit_outputs missing identity handoff")
	}
	if !strings.Contains(err.Error(), "identity.owner.username") {
		t.Fatalf("error must mention missing identity.owner.username, got %v", err)
	}
	if !strings.Contains(err.Error(), "login_gateway.url") {
		t.Fatalf("error must mention missing login_gateway.url, got %v", err)
	}
	if !strings.Contains(err.Error(), "identity.recovery") {
		t.Fatalf("error must mention missing identity.recovery, got %v", err)
	}
	if job.Step != StepVerifyRollout {
		t.Fatalf("expected job.Step=%q, got %q", StepVerifyRollout, job.Step)
	}
}

func TestProvisionHandler_AutoDeploySucceedsWhenHandoffComplete(t *testing.T) {
	// Mirror image of the failing test: when the RolloutVerifier returns the
	// full identity handoff, provision must complete and surface
	// stackkit_outputs on the job result for the frontend to render.
	specBaseDir := t.TempDir()
	completeHandoff := map[string]interface{}{
		"status": "verified",
		"stackkit_outputs": map[string]interface{}{
			"identity": map[string]interface{}{
				"owner": map[string]interface{}{
					"username":    "owner@example.com",
					"email":       "owner@example.com",
					"displayName": "Test Owner",
				},
				"recovery": map[string]interface{}{
					"bundle_ref":              "vault:recovery/stack-handoff-ok",
					"passphrase_hash_present": true,
				},
			},
			"login_gateway": map[string]interface{}{
				"url":   "https://techstack.kombify.io/login",
				"label": "Open first login",
			},
			"services": []interface{}{
				map[string]interface{}{
					"name":   "whoami",
					"url":    "https://whoami.stack-handoff-ok.kombify.me",
					"status": "healthy",
				},
			},
		},
	}
	cfg := &ProvisionConfig{
		WorkDir:             t.TempDir(),
		SpecBaseDir:         specBaseDir,
		StackKitsDir:        writeJobsTestStackKitsDir(t),
		AutoDeployAdmission: allowAutoDeployAdmissionForTest,
		RuntimeActions: RuntimeActions{
			StackKitGenerator: &fakeStackKitArtifactGenerator{},
			LeaseManager:      &fakeManagedLeaseManager{},
			SimulationGate:    &fakeRuntimeRunner{name: "simulate", result: map[string]interface{}{"status": "accepted"}},
			RolloutRunner:     &fakeRuntimeRunner{name: "rollout", result: map[string]interface{}{"status": "applied"}},
			RolloutVerifier:   &fakeRuntimeRunner{name: "verify", result: completeHandoff},
			RestoreDrill:      &fakeRuntimeRunner{name: "restore", result: map[string]interface{}{"status": "verified"}},
		},
	}

	handler := ProvisionHandler(cfg)
	job := &Job{
		ID:       "test-job-handoff-ok",
		Type:     JobTypeProvision,
		TargetID: "stack-handoff-ok",
		Payload: map[string]interface{}{
			"auto_deploy": true,
			"owner_id":    "user-1",
			"tenant_id":   "org-1",
			"owner_spec_bootstrap": &OwnerSpecBootstrap{
				Endpoint:  "https://techstack.kombify.io/api/v1/stacks/stack-handoff-ok/owner-spec",
				Token:     "test-bootstrap-token",
				ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				Scopes:    []string{"owner_spec:read"},
			},
			"spec": map[string]interface{}{
				"name":        "managed-stack",
				"provider":    "cloud",
				"provider_id": "centron",
				"goals": map[string]interface{}{
					"storage": true,
				},
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	if err := handler(context.Background(), job, queue); err != nil {
		t.Fatalf("expected handoff-complete provision to succeed, got: %v", err)
	}
	outputs, ok := job.Result["stackkit_outputs"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected job.Result[stackkit_outputs] to be a map, got %#v", job.Result["stackkit_outputs"])
	}
	identity, _ := outputs["identity"].(map[string]interface{})
	owner, _ := identity["owner"].(map[string]interface{})
	if owner["username"] != "owner@example.com" {
		t.Fatalf("expected identity.owner.username=owner@example.com, got %v", owner["username"])
	}
	gateway, _ := outputs["login_gateway"].(map[string]interface{})
	if gateway["url"] != "https://techstack.kombify.io/login" {
		t.Fatalf("expected login_gateway.url, got %v", gateway["url"])
	}
	recovery, _ := identity["recovery"].(map[string]interface{})
	if recovery["bundle_ref"] != "vault:recovery/stack-handoff-ok" {
		t.Fatalf("expected identity.recovery.bundle_ref, got %v", recovery["bundle_ref"])
	}
	services, _ := outputs["services"].([]interface{})
	if len(services) != 1 {
		t.Fatalf("expected one StackKit service output, got %#v", outputs["services"])
	}
	service, _ := services[0].(map[string]interface{})
	if service["name"] != "whoami" || service["url"] != "https://whoami.stack-handoff-ok.kombify.me" {
		t.Fatalf("service output = %#v", service)
	}
}

func TestDeployHandler_RunsStackKitsRuntimeActionsAndRecordsE2EProof(t *testing.T) {
	stackID := "stack-rollout"
	specBaseDir := t.TempDir()
	persistDeployFixture(t, specBaseDir, stackID)

	order := []string{}
	sim := &fakeRuntimeRunner{name: "simulate", order: &order, result: map[string]interface{}{"status": "accepted", "mode": "dry-run"}}
	rollout := &fakeRuntimeRunner{name: "rollout", order: &order, result: map[string]interface{}{"status": "applied", "mode": "apply"}}
	verify := &fakeRuntimeRunner{name: "verify", order: &order, result: map[string]interface{}{"status": "verified", "mode": "apply"}}
	restore := &fakeRuntimeRunner{name: "restore", order: &order, result: map[string]interface{}{"status": "verified", "mode": "apply"}}

	handler := DeployHandler(&ProvisionConfig{
		WorkDir:      t.TempDir(),
		SpecBaseDir:  specBaseDir,
		StackKitsDir: writeJobsTestStackKitsDir(t),
		RuntimeActions: RuntimeActions{
			StackKitGenerator: &fakeStackKitArtifactGenerator{},
			SimulationGate:    sim,
			RolloutRunner:     rollout,
			RolloutVerifier:   verify,
			RestoreDrill:      restore,
		},
	})
	job := &Job{
		ID:       "deploy-job",
		Type:     JobTypeDeploy,
		TargetID: stackID,
		Payload: map[string]interface{}{
			"workers": deployWorkerPayload(),
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	if err := handler(context.Background(), job, queue); err != nil {
		t.Fatalf("DeployHandler failed: %v", err)
	}

	wantOrder := []string{"simulate", "rollout", "verify", "restore"}
	if strings.Join(order, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("runtime action order = %v, want %v", order, wantOrder)
	}
	if got := job.Result["runtime_phase"]; got != string(RuntimePhaseVerified) {
		t.Fatalf("runtime_phase = %v, want %s", got, RuntimePhaseVerified)
	}
	if got := job.Result["verification_status"]; got != string(RuntimePhaseVerified) {
		t.Fatalf("verification_status = %v, want %s", got, RuntimePhaseVerified)
	}
	proof, ok := job.Result["e2e_proof"].(map[string]any)
	if !ok {
		t.Fatalf("expected e2e_proof map in job result, got %#v", job.Result["e2e_proof"])
	}
	if proof["stackkit_ref"] != "basement-kit" {
		t.Fatalf("e2e_proof stackkit_ref = %v, want basement-kit", proof["stackkit_ref"])
	}
	if proof["target_kind"] != "unknown" {
		t.Fatalf("e2e_proof target_kind = %v, want unknown", proof["target_kind"])
	}
	if proof["simulation_result"] != "accepted" ||
		proof["rollout_result"] != "applied" ||
		proof["verification_result"] != "verified" ||
		proof["restore_result"] != "verified" {
		t.Fatalf("e2e proof results = %+v", proof)
	}
	runtimeProof, ok := job.Result["runtime_proof"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected runtime_proof map, got %#v", job.Result["runtime_proof"])
	}
	for key, wantStatus := range map[string]string{
		"simulation":   "accepted",
		"rollout":      "applied",
		"verification": "verified",
		"restore":      "verified",
	} {
		entry, ok := runtimeProof[key].(map[string]interface{})
		if !ok {
			t.Fatalf("runtime_proof[%s] = %#v", key, runtimeProof[key])
		}
		if entry["status"] != wantStatus {
			t.Fatalf("runtime_proof[%s].status = %v, want %s", key, entry["status"], wantStatus)
		}
	}
	if len(sim.calls) != 1 || len(rollout.calls) != 1 || len(verify.calls) != 1 || len(restore.calls) != 1 {
		t.Fatalf("expected each runtime action once, got sim=%d rollout=%d verify=%d restore=%d", len(sim.calls), len(rollout.calls), len(verify.calls), len(restore.calls))
	}
}

func TestDeployHandler_PassesSupplementalPlatformNodesToStackKits(t *testing.T) {
	stackID := "stack-platform-nodes"
	specBaseDir := t.TempDir()
	persistDeployFixture(t, specBaseDir, stackID)
	persister, err := unifier.NewSpecPersisterWithPath(filepath.Join(specBaseDir, stackID))
	if err != nil {
		t.Fatalf("create persister: %v", err)
	}
	if _, _, err := persister.SaveStackSpecBytes([]byte(`name: basekit-test
stackkit: basement-kit
mode: simple
runtime: docker
context: local
domain: home.localhost
nodes:
  - name: main
    role: standalone
services:
  immich:
    enabled: true
`)); err != nil {
		t.Fatalf("save stack spec: %v", err)
	}

	order := []string{}
	rollout := &fakeRuntimeRunner{name: "rollout", order: &order, result: map[string]interface{}{"status": "applied", "mode": "apply"}}
	stackKitGenerator := &fakeStackKitArtifactGenerator{}
	handler := DeployHandler(&ProvisionConfig{
		WorkDir:      t.TempDir(),
		SpecBaseDir:  specBaseDir,
		StackKitsDir: writeJobsTestStackKitsDir(t),
		RuntimeActions: RuntimeActions{
			StackKitGenerator: stackKitGenerator,
			SimulationGate:    &fakeRuntimeRunner{name: "simulate", order: &order, result: map[string]interface{}{"status": "accepted", "mode": "dry-run"}},
			RolloutRunner:     rollout,
			RolloutVerifier:   &fakeRuntimeRunner{name: "verify", order: &order, result: map[string]interface{}{"status": "verified", "mode": "apply"}},
			RestoreDrill:      &fakeRuntimeRunner{name: "restore", order: &order, result: map[string]interface{}{"status": "verified", "mode": "apply"}},
		},
	})
	job := &Job{
		ID:       "deploy-job-platform-nodes",
		Type:     JobTypeDeploy,
		TargetID: stackID,
		Payload: map[string]interface{}{
			"workers": []interface{}{
				map[string]interface{}{
					"id":       "main-1",
					"name":     "main-server",
					"type":     "main",
					"provider": "local",
					"ip":       "203.0.113.10",
					"status":   "online",
					"capabilities": map[string]interface{}{
						"cpu":           float64(4),
						"ram":           float64(8192),
						"disk":          float64(160),
						"arch":          "amd64",
						"os":            "ubuntu",
						"dockerVersion": "24.0.0",
					},
				},
				map[string]interface{}{
					"id":       "worker-1",
					"name":     "worker-1",
					"type":     "worker",
					"provider": "local",
					"ip":       "203.0.113.11",
					"services": []interface{}{"immich"},
					"platform": map[string]interface{}{
						"serverId":        "server-worker",
						"destinationUuid": "destination-worker",
					},
					"bootstrap": map[string]interface{}{
						"komodo_core_address":   "https://komodo.example.test",
						"komodo_onboarding_key": "real-onboarding-key",
						"ssh": map[string]interface{}{
							"host":               "203.0.113.11",
							"user":               "root",
							"client_private_key": "worker-key",
						},
					},
					"capabilities": map[string]interface{}{
						"cpu":           float64(4),
						"ram":           float64(8192),
						"disk":          float64(160),
						"arch":          "amd64",
						"os":            "ubuntu",
						"dockerVersion": "24.0.0",
					},
				},
			},
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	if err := handler(context.Background(), job, queue); err != nil {
		t.Fatalf("DeployHandler failed: %v", err)
	}
	if len(stackKitGenerator.requests) != 1 {
		t.Fatalf("StackKits artifact requests = %d, want 1", len(stackKitGenerator.requests))
	}
	if len(rollout.calls) != 1 || len(rollout.calls[0].PlatformNodes) != 1 {
		t.Fatalf("rollout platform nodes = %+v", rollout.calls)
	}
	node := rollout.calls[0].PlatformNodes[0]
	if node.Name != "worker-1" || node.Role != "worker" || node.IP != "203.0.113.11" || node.Platform.ServerID != "server-worker" {
		t.Fatalf("platform node = %+v", node)
	}
	if node.Bootstrap == nil || node.Bootstrap.SSH == nil || node.Bootstrap.SSH.ClientPrivateKey != "worker-key" {
		t.Fatalf("platform node bootstrap = %+v", node.Bootstrap)
	}
	if node.Bootstrap.KomodoOnboardingKey != "real-onboarding-key" {
		t.Fatalf("komodo onboarding key was not forwarded transiently: %+v", node.Bootstrap)
	}

	stackSpecBytes, err := os.ReadFile(stackKitGenerator.requests[0].StackSpecPath)
	if err != nil {
		t.Fatalf("read hydrated stack spec: %v", err)
	}
	stackSpecText := string(stackSpecBytes)
	for _, want := range []string{"worker-1", "role: worker", "ip: 203.0.113.11", "serverId: server-worker", "destinationUuid: destination-worker"} {
		if !strings.Contains(stackSpecText, want) {
			t.Fatalf("hydrated stack spec missing %q:\n%s", want, stackSpecText)
		}
	}
	if strings.Contains(stackSpecText, "worker-key") || strings.Contains(stackSpecText, "client_private_key") || strings.Contains(stackSpecText, "real-onboarding-key") {
		t.Fatalf("hydrated stack spec leaked bootstrap key material:\n%s", stackSpecText)
	}
	resultJSON, err := json.Marshal(job.Result)
	if err != nil {
		t.Fatalf("marshal job result: %v", err)
	}
	if strings.Contains(string(resultJSON), "worker-key") || strings.Contains(string(resultJSON), "real-onboarding-key") {
		t.Fatalf("job result leaked bootstrap key material: %s", resultJSON)
	}
}

func TestDeployHandler_RestoreSkippedDoesNotMarkRuntimeVerified(t *testing.T) {
	stackID := "stack-restore-skipped"
	specBaseDir := t.TempDir()
	persistDeployFixture(t, specBaseDir, stackID)

	handler := DeployHandler(&ProvisionConfig{
		WorkDir:      t.TempDir(),
		SpecBaseDir:  specBaseDir,
		StackKitsDir: writeJobsTestStackKitsDir(t),
		RuntimeActions: RuntimeActions{
			StackKitGenerator: &fakeStackKitArtifactGenerator{},
			SimulationGate:    &fakeRuntimeRunner{name: "simulate", result: map[string]interface{}{"status": "accepted"}},
			RolloutRunner:     &fakeRuntimeRunner{name: "rollout", result: map[string]interface{}{"status": "applied"}},
			RolloutVerifier:   &fakeRuntimeRunner{name: "verify", result: map[string]interface{}{"status": "verified"}},
			RestoreDrill:      &fakeRuntimeRunner{name: "restore", result: map[string]interface{}{"status": "skipped", "checks": []map[string]string{{"name": "restore_drill_adapter", "status": "skipped"}}}},
		},
	})
	job := &Job{
		ID:       "deploy-job-restore-skipped",
		Type:     JobTypeDeploy,
		TargetID: stackID,
		Payload: map[string]interface{}{
			"workers": deployWorkerPayload(),
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	if err := handler(context.Background(), job, queue); err != nil {
		t.Fatalf("DeployHandler failed: %v", err)
	}
	if got := job.Result["runtime_phase"]; got != string(RuntimePhaseDeployed) {
		t.Fatalf("runtime_phase = %v, want %s when restore is skipped", got, RuntimePhaseDeployed)
	}
	proof := job.Result["e2e_proof"].(map[string]any)
	if proof["restore_result"] != "skipped" {
		t.Fatalf("restore_result = %v, want skipped", proof["restore_result"])
	}
	runtimeProof := job.Result["runtime_proof"].(map[string]interface{})
	restoreProof := runtimeProof["restore"].(map[string]interface{})
	if restoreProof["status"] != "skipped" {
		t.Fatalf("restore proof = %+v, want skipped status", restoreProof)
	}
}

func TestDeployHandler_RejectsLegacyHTTPRolloutBeforeMutation(t *testing.T) {
	stackID := "stack-http-rollout"
	specBaseDir := t.TempDir()
	persistDeployFixture(t, specBaseDir, stackID)
	writeCanonicalTemplate(t, DefaultBasementKitRef)

	var mu sync.Mutex
	order := []string{}
	record := func(action string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, action)
	}

	makeServer := func(serviceName string, paths map[string]string) *httptest.Server {
		return httptest.NewServer(servicecall.RequireServiceAuth(servicecall.Config{
			ServiceName:    serviceName,
			Secret:         "auth-secret",
			AllowedCallers: []string{"techstack"},
			Enabled:        true,
		})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			caller := servicecall.FromContext(r.Context())
			if caller == nil || caller.Service != "techstack" {
				t.Fatalf("caller = %+v, want techstack", caller)
			}
			got := runtimeaction.Request{}
			action := ""
			if strings.HasPrefix(r.URL.Path, runtimeaction.ArchitectureV2PathPrefix) {
				var v2 runtimeaction.ArchitectureV2ExecutionRequest
				if err := json.NewDecoder(r.Body).Decode(&v2); err != nil {
					t.Fatalf("decode v2: %v", err)
				}
				if err := runtimeaction.ValidateArchitectureV2ExecutionRequest(v2); err != nil {
					t.Fatalf("validate v2 request: %v", err)
				}
				action = string(v2.Action)
				got.StackID = v2.StackID
				got.StackKit = DefaultBasementKitRef
				got.TofuDir = v2.TofuDir
				if got.StackID != stackID || got.TofuDir == "" || len(v2.StackSpec) == 0 {
					t.Fatalf("v2 payload = %+v", v2)
				}
			} else {
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Fatalf("decode: %v", err)
				}
				action = string(got.Action)
				if got.StackID != stackID || got.StackKit != DefaultBasementKitRef || got.UnifiedPath == "" || got.TofuDir == "" {
					t.Fatalf("payload = %+v", got)
				}
			}
			wantPath, ok := paths[action]
			if !ok {
				t.Fatalf("unexpected action %q", action)
			}
			if r.URL.Path != wantPath {
				t.Fatalf("path = %q, want %q", r.URL.Path, wantPath)
			}
			record(action)
			status := runtimeaction.StatusAccepted
			switch runtimeaction.NormalizeAction(action) {
			case runtimeaction.ActionStackKitRollout:
				status = runtimeaction.StatusApplied
			case runtimeaction.ActionVerifyRollout, runtimeaction.ActionRestoreDrill:
				status = runtimeaction.StatusVerified
			}
			_ = json.NewEncoder(w).Encode(runtimeaction.Response{
				Status:      status,
				Action:      runtimeaction.NormalizeAction(action),
				StackID:     got.StackID,
				StackName:   got.StackName,
				StackKit:    got.StackKit,
				TofuDir:     got.TofuDir,
				UnifiedPath: got.UnifiedPath,
				Mode:        runtimeaction.ModeApply,
			})
		})))
	}

	simulateServer := makeServer(runtimeActionTargetSimulate, map[string]string{
		runtimeActionSimulateUpdate: defaultSimulationGatePath,
	})
	defer simulateServer.Close()
	stackKitsServer := makeServer(runtimeActionTargetStackKits, map[string]string{
		string(StepRolloutRunner): defaultStackKitsRolloutPath,
		string(StepVerifyRollout): defaultStackKitsVerifyPath,
		string(StepRestoreDrill):  defaultRestoreDrillPath,
	})
	defer stackKitsServer.Close()

	newRunner := func(baseURL, target, action, path string) RuntimeActionRunner {
		runner, err := NewHTTPRuntimeActionRunner(HTTPRuntimeActionRunnerConfig{
			BaseURL:           baseURL,
			Target:            target,
			Action:            action,
			Path:              path,
			ServiceAuthSecret: "auth-secret",
		})
		if err != nil {
			t.Fatalf("NewHTTPRuntimeActionRunner(%s): %v", action, err)
		}
		return runner
	}

	handler := DeployHandler(&ProvisionConfig{
		WorkDir:      t.TempDir(),
		SpecBaseDir:  specBaseDir,
		StackKitsDir: writeJobsTestStackKitsDir(t),
		RuntimeActions: RuntimeActions{
			StackKitGenerator: &fakeStackKitArtifactGenerator{},
			SimulationGate:    newRunner(simulateServer.URL, runtimeActionTargetSimulate, runtimeActionSimulateUpdate, defaultSimulationGatePath),
			RolloutRunner:     newRunner(stackKitsServer.URL, runtimeActionTargetStackKits, string(StepRolloutRunner), defaultStackKitsRolloutPath),
			RolloutVerifier:   newRunner(stackKitsServer.URL, runtimeActionTargetStackKits, string(StepVerifyRollout), defaultStackKitsVerifyPath),
			RestoreDrill:      newRunner(stackKitsServer.URL, runtimeActionTargetStackKits, string(StepRestoreDrill), defaultRestoreDrillPath),
		},
	})
	job := &Job{
		ID:       "deploy-job-http-runtime-actions",
		Type:     JobTypeDeploy,
		TargetID: stackID,
		Payload: map[string]interface{}{
			"workers": deployWorkerPayload(),
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	err := handler(context.Background(), job, queue)
	if err == nil || !strings.Contains(err.Error(), "cannot bind the admitted ResolvedPlan hash") {
		t.Fatalf("DeployHandler error = %v, want fail-closed legacy HTTP rejection", err)
	}

	mu.Lock()
	gotOrder := append([]string(nil), order...)
	mu.Unlock()
	wantOrder := []string{runtimeActionSimulateUpdate}
	if strings.Join(gotOrder, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("runtime action order = %v, want %v", gotOrder, wantOrder)
	}
	if got := job.Result["runtime_phase"]; got == string(RuntimePhaseDeployed) || got == string(RuntimePhaseVerified) {
		t.Fatalf("runtime_phase = %v, legacy HTTP path must not report deployment", got)
	}
}

func TestDeployHandler_FailsWhenStackKitsRuntimeRunnerIsMissing(t *testing.T) {
	stackID := "stack-missing-runner"
	specBaseDir := t.TempDir()
	persistDeployFixture(t, specBaseDir, stackID)

	handler := DeployHandler(&ProvisionConfig{
		WorkDir:      t.TempDir(),
		SpecBaseDir:  specBaseDir,
		StackKitsDir: writeJobsTestStackKitsDir(t),
		RuntimeActions: RuntimeActions{
			StackKitGenerator: &fakeStackKitArtifactGenerator{},
			SimulationGate:    &fakeRuntimeRunner{name: "simulate"},
			RolloutVerifier:   &fakeRuntimeRunner{name: "verify"},
			RestoreDrill:      &fakeRuntimeRunner{name: "restore"},
		},
	})
	job := &Job{
		ID:       "deploy-job-missing-runner",
		Type:     JobTypeDeploy,
		TargetID: stackID,
		Payload: map[string]interface{}{
			"workers": deployWorkerPayload(),
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	err := handler(context.Background(), job, queue)
	if err == nil {
		t.Fatal("expected missing StackKits rollout runner to fail")
	}
	if !strings.Contains(err.Error(), "StackKits rollout runner is not configured") {
		t.Fatalf("error = %v, want StackKits rollout runner message", err)
	}
}

func TestDestroyHandler_DecommissionsManagedRuntimeBeforeWorkspaceCheck(t *testing.T) {
	decommissioner := &fakeManagedLeaseDecommissioner{result: &ManagedLeaseDecommissionResult{
		Decommissioned: 2,
		LeaseIDs:       []string{"lease-centron", "lease-ionos"},
		Proofs: []ManagedLeaseDecommissionProof{
			testManagedLeaseDecommissionProof("stack-managed", "org-1", "lease-centron", ManagedLeaseDecommissionObservedDecommissioned, ""),
			testManagedLeaseDecommissionProof("stack-managed", "org-1", "lease-ionos", ManagedLeaseDecommissionObservedDecommissioned, ""),
		},
	}}
	cfg := &ProvisionConfig{
		WorkDir: t.TempDir(),
		RuntimeActions: RuntimeActions{
			LeaseDecommissioner: decommissioner,
		},
	}
	handler := DestroyHandler(cfg)
	job := &Job{
		ID:         "test-destroy-managed-runtime",
		Type:       JobTypeDestroy,
		TargetID:   "stack-managed",
		TargetName: "managed-stack",
		Payload: map[string]interface{}{
			"owner_id":                              "user-1",
			"tenant_id":                             "org-1",
			ManagedRuntimeDecommissionRequiredField: true,
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	if err := handler(context.Background(), job, queue); err != nil {
		t.Fatalf("DestroyHandler: %v", err)
	}
	if len(decommissioner.requests) != 1 {
		t.Fatalf("decommission requests = %d, want 1", len(decommissioner.requests))
	}
	req := decommissioner.requests[0]
	if req.StackID != "stack-managed" || req.TenantID != "org-1" || req.OwnerID != "user-1" {
		t.Fatalf("decommission request = %+v", req)
	}
	if job.Progress != 100 {
		t.Fatalf("progress = %d, want 100", job.Progress)
	}
}

func TestManagedRuntimeDestroyHandlersPreserveDurableWait(t *testing.T) {
	waitErr := &JobWaitError{
		Reason:      "waiting_provider_decommission",
		Message:     "provider absence is pending",
		ResumeAfter: time.Second,
	}
	for _, testCase := range []struct {
		name    string
		jobType JobType
		handler JobHandler
	}{
		{
			name:    "stack destroy",
			jobType: JobTypeDestroy,
			handler: DestroyHandler(&ProvisionConfig{
				WorkDir: t.TempDir(),
				RuntimeActions: RuntimeActions{LeaseDecommissioner: &fakeManagedLeaseDecommissioner{
					err: waitErr,
				}},
			}),
		},
		{
			name:    "lease reconciliation",
			jobType: JobTypeReconcileLease,
			handler: ReconcileLeaseHandler(&ProvisionConfig{
				WorkDir: t.TempDir(),
				RuntimeActions: RuntimeActions{LeaseDecommissioner: &fakeManagedLeaseDecommissioner{
					err: waitErr,
				}},
			}),
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			job := &Job{
				ID: "wait-" + testCase.name, Type: testCase.jobType, TargetID: "stack-managed",
				Payload: map[string]interface{}{
					"owner_id": "user-1", "tenant_id": "org-1",
					"lease_id":                              "lease-1",
					ManagedRuntimeDecommissionRequiredField: true,
				},
			}
			queue := &Queue{jobs: map[string]*Job{job.ID: job}}
			err := testCase.handler(context.Background(), job, queue)
			var got *JobWaitError
			if !errors.As(err, &got) || got != waitErr {
				t.Fatalf("handler error = %T %v, want original JobWaitError", err, err)
			}
		})
	}
}

func TestDestroyHandler_FailsClosedBeforeWorkspaceSuccessWithoutNativeProof(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload map[string]interface{}
		actions RuntimeActions
		wantErr error
	}{
		{
			name:    "old job has no classification",
			payload: map[string]interface{}{"owner_id": "user-1", "tenant_id": "org-1"},
			wantErr: ErrManagedLeaseDecommissionProofRequired,
		},
		{
			name: "managed stack has no native decommissioner",
			payload: map[string]interface{}{
				"owner_id": "user-1", "tenant_id": "org-1",
				ManagedRuntimeDecommissionRequiredField: true,
			},
			wantErr: ErrManagedLeaseDecommissionUnavailable,
		},
		{
			name: "adapter returns a count without terminal readback",
			payload: map[string]interface{}{
				"owner_id": "user-1", "tenant_id": "org-1",
				ManagedRuntimeDecommissionRequiredField: true,
			},
			actions: RuntimeActions{LeaseDecommissioner: &fakeManagedLeaseDecommissioner{
				result: &ManagedLeaseDecommissionResult{Decommissioned: 1, LeaseIDs: []string{"lease-unproven"}},
			}},
			wantErr: ErrManagedLeaseDecommissionProofRequired,
		},
		{
			name: "duplicate proof cannot hide an uncovered lease",
			payload: map[string]interface{}{
				"owner_id": "user-1", "tenant_id": "org-1",
				ManagedRuntimeDecommissionRequiredField: true,
			},
			actions: RuntimeActions{LeaseDecommissioner: &fakeManagedLeaseDecommissioner{
				result: &ManagedLeaseDecommissionResult{
					Decommissioned: 2,
					LeaseIDs:       []string{"lease-a", "lease-b"},
					Proofs: []ManagedLeaseDecommissionProof{
						testManagedLeaseDecommissionProof("stack-managed", "org-1", "lease-a", ManagedLeaseDecommissionObservedDecommissioned, ""),
						testManagedLeaseDecommissionProof("stack-managed", "org-1", "lease-a", ManagedLeaseDecommissionObservedDecommissioned, ""),
					},
				},
			}},
			wantErr: ErrManagedLeaseDecommissionProofRequired,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := &Job{
				ID: "destroy-fail-closed", Type: JobTypeDestroy, TargetID: "stack-managed",
				Payload: tc.payload,
			}
			queue := &Queue{jobs: map[string]*Job{job.ID: job}}
			err := DestroyHandler(&ProvisionConfig{WorkDir: t.TempDir(), RuntimeActions: tc.actions})(context.Background(), job, queue)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr.Error()) {
				t.Fatalf("DestroyHandler error = %v, want %v", err, tc.wantErr)
			}
			if job.Progress == 100 {
				t.Fatal("destroy reported terminal success without a native provider proof")
			}
		})
	}
}

func TestReconcileLeaseHandler_DecommissionsLeaseWithoutWorkspaceDestroy(t *testing.T) {
	decommissioner := &fakeManagedLeaseDecommissioner{result: &ManagedLeaseDecommissionResult{
		Decommissioned: 1,
		LeaseIDs:       []string{"lease-centron"},
		Proofs: []ManagedLeaseDecommissionProof{
			testManagedLeaseDecommissionProof("stack-managed", "org-1", "lease-centron", ManagedLeaseDecommissionObservedDecommissioned, strings.Repeat("a", 64)),
		},
	}}
	workDir := t.TempDir()
	// A stack OpenTofu workspace exists; the lease-only reconcile must NOT touch
	// it (unlike JobTypeDestroy, which would tear down the whole stack).
	stackWorkspace := filepath.Join(workDir, "stack-managed")
	if err := os.MkdirAll(stackWorkspace, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := &ProvisionConfig{
		WorkDir:        workDir,
		RuntimeActions: RuntimeActions{LeaseDecommissioner: decommissioner},
	}
	// Payload keys match what Orchestrator.EnqueueManagedLeaseReconciliation writes.
	job := &Job{
		ID:       "test-reconcile-lease",
		Type:     JobTypeReconcileLease,
		TargetID: "stack-managed",
		Payload: map[string]interface{}{
			"owner_id":                    "user-1",
			"tenant_id":                   "org-1",
			"lease_id":                    "lease-centron",
			resourceGenerationDigestField: strings.Repeat("a", 64),
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	if err := ReconcileLeaseHandler(cfg)(context.Background(), job, queue); err != nil {
		t.Fatalf("ReconcileLeaseHandler: %v", err)
	}
	if len(decommissioner.requests) != 1 {
		t.Fatalf("decommission requests = %d, want 1 (VM must be freed)", len(decommissioner.requests))
	}
	req := decommissioner.requests[0]
	if req.StackID != "stack-managed" || req.TenantID != "org-1" || req.OwnerID != "user-1" || req.LeaseID != "lease-centron" || req.ResourceGenerationDigest != strings.Repeat("a", 64) {
		t.Fatalf("decommission request = %+v", req)
	}
	// Must NOT have run OpenTofu / marked the stack workspace destroyed.
	if _, err := os.Stat(filepath.Join(stackWorkspace, ".destroyed")); !os.IsNotExist(err) {
		t.Fatalf("reconcile must not touch the stack OpenTofu workspace (found .destroyed marker)")
	}
	if job.Progress != 100 {
		t.Fatalf("progress = %d, want 100", job.Progress)
	}
}

func TestReconcileLeaseHandler_ProviderVerifiedAbsenceIsSafeNoop(t *testing.T) {
	decommissioner := &fakeManagedLeaseDecommissioner{result: &ManagedLeaseDecommissionResult{
		Decommissioned: 0,
		LeaseIDs:       []string{"lease-gone"},
		Proofs: []ManagedLeaseDecommissionProof{
			testManagedLeaseDecommissionProof("stack-x", "org-1", "lease-gone", ManagedLeaseDecommissionObservedNotFound, ""),
		},
	}}
	cfg := &ProvisionConfig{WorkDir: t.TempDir(), RuntimeActions: RuntimeActions{LeaseDecommissioner: decommissioner}}
	job := &Job{
		ID: "test-reconcile-none", Type: JobTypeReconcileLease, TargetID: "stack-x",
		Payload: map[string]interface{}{"owner_id": "user-1", "tenant_id": "org-1", "lease_id": "lease-gone"},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}
	if err := ReconcileLeaseHandler(cfg)(context.Background(), job, queue); err != nil {
		t.Fatalf("ReconcileLeaseHandler no-lease: %v", err)
	}
	if job.Progress != 100 {
		t.Fatalf("progress = %d, want 100", job.Progress)
	}
}

func TestRegisterDefaultHandlers_RegistersReconcileLease(t *testing.T) {
	q := NewQueue(1, nil)
	RegisterDefaultHandlers(q, &ProvisionConfig{WorkDir: t.TempDir()})
	if _, ok := q.handlers[JobTypeReconcileLease]; !ok {
		t.Fatal("JobTypeReconcileLease handler not registered by RegisterDefaultHandlers")
	}
}

func TestDestroyHandler_NonExistentWorkspace(t *testing.T) {
	var reconcileRequests []NoWorkspaceDestroyReconcileRequest
	cfg := &ProvisionConfig{
		WorkDir: t.TempDir(),
		NoWorkspaceDestroyReconciler: func(_ context.Context, request NoWorkspaceDestroyReconcileRequest) error {
			reconcileRequests = append(reconcileRequests, request)
			return nil
		},
	}

	handler := DestroyHandler(cfg)

	job := &Job{
		ID:         "test-destroy-1",
		Type:       JobTypeDestroy,
		TargetID:   "nonexistent-stack",
		TargetName: "ghost-stack",
		Payload: map[string]interface{}{
			"owner_id":                              "owner-1",
			"tenant_id":                             "tenant-1",
			ManagedRuntimeDecommissionRequiredField: false,
		},
	}

	queue := &Queue{
		jobs: map[string]*Job{job.ID: job},
	}

	// Should succeed - no workspace to destroy
	err := handler(context.Background(), job, queue)
	if err != nil {
		t.Errorf("expected success for nonexistent workspace, got: %v", err)
	}

	// Progress should be 100
	if job.Progress != 100 {
		t.Errorf("expected progress 100, got %d", job.Progress)
	}
	if len(reconcileRequests) != 1 {
		t.Fatalf("no-workspace reconcile calls = %d, want 1", len(reconcileRequests))
	}
	if got := reconcileRequests[0]; got.StackID != "nonexistent-stack" || got.TenantID != "tenant-1" || got.OwnerID != "owner-1" {
		t.Fatalf("no-workspace reconcile request = %+v", got)
	}
	result := job.Snapshot().Result
	if got := result[DestroyWorkspaceStateResultField]; got != DestroyWorkspaceStateAbsent {
		t.Fatalf("destroy workspace state = %v, want %q", got, DestroyWorkspaceStateAbsent)
	}
	if got := result[DestroyProjectionReconciledResultField]; got != true {
		t.Fatalf("destroy projection reconciled = %v, want true", got)
	}
}

func TestDestroyHandler_ExistingWorkspace(t *testing.T) {
	cfg := &ProvisionConfig{
		WorkDir: t.TempDir(),
	}

	// Create a mock workspace
	stackID := "existing-stack"
	workDir := filepath.Join(cfg.WorkDir, stackID)
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("failed to create test workspace: %v", err)
	}

	// Create a minimal state file to simulate an initialized workspace
	stateFile := filepath.Join(workDir, "terraform.tfstate")
	if err := os.WriteFile(stateFile, []byte(`{"version": 4, "resources": []}`), 0644); err != nil {
		t.Fatalf("failed to create state file: %v", err)
	}

	handler := DestroyHandler(cfg)

	job := &Job{
		ID:         "test-destroy-2",
		Type:       JobTypeDestroy,
		TargetID:   stackID,
		TargetName: "existing-stack",
		Payload: map[string]interface{}{
			ManagedRuntimeDecommissionRequiredField: false,
		},
	}

	queue := &Queue{
		jobs: map[string]*Job{job.ID: job},
	}

	err := handler(context.Background(), job, queue)

	// If tofu destroy failed (expected in CI without tofu), that's OK
	if err != nil && contains(err.Error(), "tofu destroy failed") {
		t.Logf("OpenTofu not installed, skipping destroy execution test: %v", err)
		return
	}

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRegisterDefaultHandlers(t *testing.T) {
	queue := NewQueue(1, nil)

	RegisterDefaultHandlers(queue, nil)

	// Check that handlers are registered
	if _, ok := queue.handlers[JobTypeProvision]; !ok {
		t.Error("provision handler not registered")
	}

	if _, ok := queue.handlers[JobTypeDestroy]; !ok {
		t.Error("destroy handler not registered")
	}
}

func TestQueue_EnqueueAndProcess(t *testing.T) {
	queue := NewQueue(1, nil)

	// Register a simple test handler
	var handlerCalled atomic.Bool
	queue.RegisterHandler(JobTypeCommand, func(ctx context.Context, job *Job, q *Queue) error {
		handlerCalled.Store(true)
		time.Sleep(10 * time.Millisecond)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	queue.Start(ctx)
	defer queue.Stop()

	job := &Job{
		ID:   "test-command-1",
		Type: JobTypeCommand,
	}

	if err := queue.Enqueue(job); err != nil {
		t.Fatalf("failed to enqueue job: %v", err)
	}

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	if !handlerCalled.Load() {
		t.Error("handler was not called")
	}

	// Check job state
	processedJob, ok := queue.Get(job.ID)
	if !ok {
		t.Fatal("job not found")
	}

	processedJob.mu.Lock()
	processedState := processedJob.State
	processedJob.mu.Unlock()
	if processedState != JobStateCompleted {
		t.Errorf("expected state completed, got %s", processedState)
	}
}

func TestQueue_Stats(t *testing.T) {
	queue := NewQueue(1, nil)

	// Add some jobs
	queue.jobs["job1"] = &Job{ID: "job1", State: JobStatePending}
	queue.jobs["job2"] = &Job{ID: "job2", State: JobStateRunning}
	queue.jobs["job3"] = &Job{ID: "job3", State: JobStateCompleted}
	queue.jobs["job4"] = &Job{ID: "job4", State: JobStateFailed}

	stats := queue.Stats()

	if stats["total"] != 4 {
		t.Errorf("expected total 4, got %d", stats["total"])
	}
	if stats["pending"] != 1 {
		t.Errorf("expected pending 1, got %d", stats["pending"])
	}
	if stats["running"] != 1 {
		t.Errorf("expected running 1, got %d", stats["running"])
	}
	if stats["completed"] != 1 {
		t.Errorf("expected completed 1, got %d", stats["completed"])
	}
	if stats["failed"] != 1 {
		t.Errorf("expected failed 1, got %d", stats["failed"])
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Tests for convertUIConfigToSpec and related helpers

func TestMapServicesArrayToSpec(t *testing.T) {
	tests := []struct {
		name     string
		input    []interface{}
		wantLen  int
		wantName string
		wantType string
	}{
		{
			name:     "traefik service",
			input:    []interface{}{"traefik"},
			wantLen:  1,
			wantName: "traefik",
			wantType: "reverse-proxy",
		},
		{
			name:     "multiple services",
			input:    []interface{}{"traefik", "pocketbase", "nextcloud"},
			wantLen:  3,
			wantName: "traefik",
			wantType: "reverse-proxy",
		},
		{
			name:     "legacy monitoring alias maps to otel collector",
			input:    []interface{}{"monitoring"},
			wantLen:  1,
			wantName: "otel-collector",
			wantType: "monitoring",
		},
		{
			name:    "empty array",
			input:   []interface{}{},
			wantLen: 0,
		},
		{
			name:     "invalid types ignored",
			input:    []interface{}{"traefik", 123, nil, "pocketbase"},
			wantLen:  2,
			wantName: "traefik",
			wantType: "reverse-proxy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mapServicesArrayToSpec(tt.input)
			if len(result) != tt.wantLen {
				t.Errorf("mapServicesArrayToSpec() got %d services, want %d", len(result), tt.wantLen)
			}
			if tt.wantLen > 0 && result[0].Name != tt.wantName {
				t.Errorf("mapServicesArrayToSpec() first service name = %s, want %s", result[0].Name, tt.wantName)
			}
			if tt.wantLen > 0 && result[0].Type != tt.wantType {
				t.Errorf("mapServicesArrayToSpec() first service type = %s, want %s", result[0].Type, tt.wantType)
			}
		})
	}
}

func TestMapServiceNameToType(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"traefik", "reverse-proxy"},
		{"nginx", "reverse-proxy"},
		{"caddy", "reverse-proxy"},
		{"caprover", "paas"},
		{"pocketbase", "backend"},
		{"pocket-id", "auth"},
		{"pocket_id", "auth"},
		{"nextcloud", "storage"},
		{"headscale", "vpn"},
		{"tailscale", "vpn"},
		{"otel-collector", "monitoring"},
		{"monitoring", "monitoring"},
		{"victoriametrics", "monitoring"},
		{"grafana", "monitoring"},
		{"nextcloud", "storage"},
		{"vaultwarden", "auth"},
		{"immich-server", "media"},
		{"immich-ml", "media"},
		{"immich-postgres", "database"},
		{"immich-redis", "cache"},
		{"unknown-service", "service"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapServiceNameToType(tt.name)
			if got != tt.want {
				t.Errorf("mapServiceNameToType(%s) = %s, want %s", tt.name, got, tt.want)
			}
		})
	}
}

func TestMapOptionsToServices(t *testing.T) {
	tests := []struct {
		name    string
		options map[string]interface{}
		wantLen int
	}{
		{
			name: "enable traefik",
			options: map[string]interface{}{
				"enable_traefik": true,
			},
			wantLen: 1,
		},
		{
			name: "enable monitoring adds collector baseline",
			options: map[string]interface{}{
				"enable_monitoring": true,
			},
			wantLen: 1,
		},
		{
			name: "disabled services",
			options: map[string]interface{}{
				"enable_traefik": false,
			},
			wantLen: 0,
		},
		{
			name: "multiple enabled",
			options: map[string]interface{}{
				"enable_traefik":    true,
				"enable_pocketbase": true,
			},
			wantLen: 2,
		},
		{
			name: "pocket id default identity",
			options: map[string]interface{}{
				"identity_head": "pocket_id",
			},
			wantLen: 1,
		},
		{
			name: "pocketbase backend plus passkeys adds pocket id",
			options: map[string]interface{}{
				"enable_pocketbase_backend": true,
				"requires_passkeys":         true,
			},
			wantLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mapOptionsToServices(tt.options)
			if len(result) != tt.wantLen {
				t.Errorf("mapOptionsToServices() got %d services, want %d", len(result), tt.wantLen)
			}
		})
	}
}

func TestSanitizeDNSName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"my-homelab", "my-homelab"},
		{"My HomeStack", "my-homestack"},
		{"Test_Stack_123", "test-stack-123"},
		{"ABC", "abc"},
		{"123-start", "ks-123-start"}, // Must start with letter, prefixed with ks-
		{"", "techstack"},             // Empty defaults
		{"---", "techstack"},          // Invalid defaults
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeDNSName(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeDNSName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestConvertUIConfigToSpec_WizardFormat(t *testing.T) {
	// Test wizard format with goals
	wizardConfig := map[string]interface{}{
		"name": "my-homelab",
		"goals": map[string]interface{}{
			"website": true,
			"storage": true,
		},
		"provider": "local",
		"network": map[string]interface{}{
			"accessMode": "local",
		},
	}

	spec, err := convertUIConfigToSpec(wizardConfig)
	if err != nil {
		t.Fatalf("convertUIConfigToSpec() failed: %v", err)
	}

	if spec.Name != "my-homelab" {
		t.Errorf("expected name 'my-homelab', got '%s'", spec.Name)
	}

	if len(spec.Services) == 0 {
		t.Error("expected services to be populated from goals")
	}

	if len(spec.Nodes) == 0 {
		t.Error("expected at least one node")
	}
}

func TestConvertUIConfigToSpec_DirectServicesFormat(t *testing.T) {
	// Test direct services array format
	directConfig := map[string]interface{}{
		"name":     "test-stack",
		"provider": "local",
		"services": []interface{}{"traefik", "pocketbase", "nextcloud"},
	}

	spec, err := convertUIConfigToSpec(directConfig)
	if err != nil {
		t.Fatalf("convertUIConfigToSpec() failed: %v", err)
	}

	if len(spec.Services) != 3 {
		t.Errorf("expected 3 services, got %d", len(spec.Services))
	}

	// Check service types
	serviceTypes := make(map[string]string)
	for _, svc := range spec.Services {
		serviceTypes[svc.Name] = svc.Type
	}

	if serviceTypes["traefik"] != "reverse-proxy" {
		t.Errorf("expected traefik type 'reverse-proxy', got '%s'", serviceTypes["traefik"])
	}
	if serviceTypes["pocketbase"] != "backend" {
		t.Errorf("expected pocketbase type 'backend', got '%s'", serviceTypes["pocketbase"])
	}
	if serviceTypes["nextcloud"] != "storage" {
		t.Errorf("expected nextcloud type 'storage', got '%s'", serviceTypes["nextcloud"])
	}
}

func TestConvertUIConfigToSpec_OptionsFormat(t *testing.T) {
	// Test options format with enable_* flags
	optionsConfig := map[string]interface{}{
		"name":     "test-stack",
		"provider": "local",
		"options": map[string]interface{}{
			"enable_traefik":    true,
			"enable_monitoring": true,
			"vpn":               "headscale",
		},
	}

	spec, err := convertUIConfigToSpec(optionsConfig)
	if err != nil {
		t.Fatalf("convertUIConfigToSpec() failed: %v", err)
	}

	// Should have traefik + OTel Collector = 2 services
	if len(spec.Services) != 2 {
		t.Errorf("expected 2 services, got %d", len(spec.Services))
	}

	// Check VPN was set
	if spec.Network.VPN != "headscale" {
		t.Errorf("expected VPN 'headscale', got '%s'", spec.Network.VPN)
	}
}

func TestTechStackEnrollmentForManagedRolloutUsesCanonicalSignedLeaseIdentity(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "managed-enrollment-secret")
	t.Setenv("TECHSTACK_WORKER_TOKEN_SECRET", "")
	t.Setenv("SERVICE_AUTH_SECRET", "")
	const leaseID = "lease-centron-contract"
	job := &Job{
		TargetID: "stack-contract",
		Result:   map[string]interface{}{leaseIDField: leaseID, "server_id": "server-caller-supplied"},
	}
	prep := &deployPreparation{
		kombSpec:       &core.KombinationSpec{Name: "Contract Stack"},
		managedRuntime: true,
	}
	enrollment, err := techStackEnrollmentForRollout(job, prep, "tenant-contract", "owner-contract")
	if err != nil {
		t.Fatalf("techStackEnrollmentForRollout: %v", err)
	}
	wantServerID := runtimeidentity.LeaseServerID(leaseID)
	if enrollment.ServerID != wantServerID || enrollment.LeaseID != leaseID {
		t.Fatalf("enrollment lease identity = %#v, want server %q", enrollment, wantServerID)
	}
	claims, verifyErr := workerauth.Verify(workerauth.SecretFromEnv(), enrollment.AgentToken, time.Now().UTC())
	if verifyErr != nil {
		t.Fatalf("managed enrollment must issue a signed token: %v", verifyErr)
	}
	if claims.LeaseID != leaseID || claims.ServerID != wantServerID || claims.StackID != job.TargetID {
		t.Fatalf("token claims lost canonical enrollment identity: %#v", claims)
	}
}

func TestTechStackEnrollmentForLocalRolloutUsesCanonicalWorkerServerIdentity(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "local-enrollment-secret")
	workers := deployWorkerPayload()
	workers[0].(map[string]interface{})["server_id"] = "server-canonical"
	job := &Job{
		TargetID: "stack-local",
		Result:   map[string]interface{}{"server_id": "server-canonical"},
		Payload:  map[string]interface{}{"workers": workers, "server_id": "server-canonical"},
	}
	enrollment, err := techStackEnrollmentForRollout(job, &deployPreparation{
		kombSpec: &core.KombinationSpec{Name: "Local Stack"},
	}, "tenant-local", "owner-local")
	if err != nil {
		t.Fatalf("techStackEnrollmentForRollout() error = %v", err)
	}
	if enrollment.ServerID != "server-canonical" {
		t.Fatalf("enrollment server = %q, want canonical worker server", enrollment.ServerID)
	}
}

func TestDeployHandlerRejectsRuntimeServerIdentityMismatchBeforeAdmissionOrMutation(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "local-enrollment-secret")
	specBaseDir := t.TempDir()
	stackID := "stack-server-mismatch"
	prepareStackSpecsForDeploy(t, specBaseDir, stackID)
	workers := deployWorkerPayload()
	workers[0].(map[string]interface{})["server_id"] = "server-canonical"
	order := []string{}
	authority := newRecordingCurrentPortAuthority(&order)
	rollout := &fakeRuntimeRunner{name: "rollout", order: &order}
	handler := DeployHandler(&ProvisionConfig{
		WorkDir:       t.TempDir(),
		StackKitsDir:  createTestBasementKitStackKitsDir(t),
		SpecBaseDir:   specBaseDir,
		PortInventory: authority,
		RuntimeActions: RuntimeActions{
			StackKitGenerator: &fakeStackKitArtifactGenerator{},
			RolloutRunner:     rollout,
		},
	})
	job := &Job{
		ID: "deploy-server-mismatch", Type: JobTypeDeploy, TargetID: stackID, TargetName: "Mismatch Stack",
		Payload: map[string]interface{}{
			"workers": workers, "server_id": "server-caller-supplied", "apply": true,
		},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	err := handler(t.Context(), job, queue)
	if err == nil || job.Step != StepPortAdmission {
		t.Fatalf("DeployHandler() error = %v step = %q, want port admission identity denial", err, job.Step)
	}
	if !reflect.DeepEqual(order, []string{}) || len(rollout.calls) != 0 {
		t.Fatalf("calls after identity mismatch = %v rollout=%d, want zero admission and mutation", order, len(rollout.calls))
	}
	details := mapFromInterface(job.Result["port_admission_error"])
	if details["error_code"] != "runtime_server_identity_mismatch" || details["retryable"] != false {
		t.Fatalf("stable identity denial = %#v", details)
	}
}

func TestTechStackEnrollmentForRolloutFailsClosedWithoutSigningSecret(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "")
	t.Setenv("TECHSTACK_WORKER_TOKEN_SECRET", "")
	t.Setenv("SERVICE_AUTH_SECRET", "")
	t.Setenv(envAllowUnsignedWorkerToken, "")
	// Both lanes must fail closed: an unsigned agent token cannot authenticate
	// the node phone-home, leaving the dashboard blind to the provisioned server.
	// The BYOS/self-hosted case additionally has no explicit tenant/owner, which
	// previously degraded silently to an opaque token.
	for _, tc := range []struct {
		name           string
		managedRuntime bool
		job            *Job
		tenantID       string
		ownerID        string
	}{
		{
			name:           "managed lane",
			managedRuntime: true,
			job:            &Job{TargetID: "stack-contract", Result: map[string]interface{}{leaseIDField: "lease-contract"}},
			tenantID:       "tenant-contract",
			ownerID:        "owner-contract",
		},
		{
			name:           "byos self-hosted lane without tenant identity",
			managedRuntime: false,
			job:            &Job{TargetID: "stack-byos"},
			tenantID:       "",
			ownerID:        "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := techStackEnrollmentForRollout(tc.job, &deployPreparation{
				kombSpec:       &core.KombinationSpec{Name: "Contract Stack"},
				managedRuntime: tc.managedRuntime,
			}, tc.tenantID, tc.ownerID)
			if err == nil || !strings.Contains(err.Error(), "signed worker agent token") {
				t.Fatalf("%s enrollment must fail closed without signing secret, got %v", tc.name, err)
			}
		})
	}
}

func TestTechStackEnrollmentForByosRolloutSignsWithSyntheticIdentity(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "byos-signing-secret")
	t.Setenv(envAllowUnsignedWorkerToken, "")
	// A tenant-less BYOS rollout must still mint a *signed* token: the synthetic
	// self-hosted identity satisfies workerauth.Issue so fail-closed does not
	// break tenant-less self-hosted deploys when a secret is configured.
	enrollment, err := techStackEnrollmentForRollout(&Job{TargetID: "stack-byos"}, &deployPreparation{
		kombSpec:       &core.KombinationSpec{Name: "BYOS Stack"},
		managedRuntime: false,
	}, "", "")
	if err != nil {
		t.Fatalf("byos enrollment with a signing secret must succeed, got %v", err)
	}
	if enrollment == nil || enrollment.AgentToken == "" {
		t.Fatalf("byos enrollment missing agent token: %#v", enrollment)
	}
	claims, verifyErr := workerauth.Verify(workerauth.SecretFromEnv(), enrollment.AgentToken, time.Now().UTC())
	if verifyErr != nil {
		t.Fatalf("byos agent token must be a verifiable signed token, got %v", verifyErr)
	}
	if claims.TenantID == "" || claims.OwnerID == "" {
		t.Fatalf("byos token claims must carry a synthetic identity, got %#v", claims)
	}
}

func TestTechStackEnrollmentAllowsUnsignedTokenOnlyWithExplicitOptIn(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", "")
	t.Setenv("TECHSTACK_WORKER_TOKEN_SECRET", "")
	t.Setenv("SERVICE_AUTH_SECRET", "")
	t.Setenv(envAllowUnsignedWorkerToken, "1")
	enrollment, err := techStackEnrollmentForRollout(&Job{TargetID: "stack-dev"}, &deployPreparation{
		kombSpec:       &core.KombinationSpec{Name: "Dev Stack"},
		managedRuntime: false,
	}, "tenant-dev", "owner-dev")
	if err != nil {
		t.Fatalf("explicit unsigned opt-in must not fail, got %v", err)
	}
	if enrollment == nil || enrollment.AgentToken == "" {
		t.Fatalf("unsigned opt-in must still produce an opaque token: %#v", enrollment)
	}
}

func TestRuntimeActionObservationIsSanitizedAndPersistedWithStackKitOutputs(t *testing.T) {
	result := map[string]interface{}{
		metadataKeyStackKitOutputs: map[string]interface{}{
			"services": map[string]interface{}{"vaultwarden": "https://vault.example.test"},
			"observation": map[string]interface{}{
				"version":     "stackkit.runtime-observation/v1",
				"observed_at": time.Now().UTC().Format(time.RFC3339Nano),
				"host": map[string]interface{}{
					"reachable": true,
					"api_token": "must-not-persist",
				},
				"services": []interface{}{map[string]interface{}{
					"name": "vaultwarden", "status": "healthy", "platform_app_id": "app-1",
					"probe": map[string]interface{}{"url": "https://vault.example.test/health", "reached": true, "status_code": 200},
				}},
			},
		},
	}
	outputs := map[string]interface{}{}
	mergeStackKitOutputs(outputs, result)
	observation := resultMap(outputs, "observation")
	if observation == nil || resultString(observation, "version") != "stackkit.runtime-observation/v1" {
		t.Fatalf("versioned observation was not persisted: %#v", outputs)
	}
	host := resultMap(observation, "host")
	if host == nil || host["api_token"] != nil || host["reachable"] != true {
		t.Fatalf("observation was not sanitized: %#v", observation)
	}
	proof := runtimeActionProof("stackkit_rollout", result, "applied")
	if resultMap(proof, "observation") == nil {
		t.Fatalf("runtime action proof dropped the observation: %#v", proof)
	}
}
