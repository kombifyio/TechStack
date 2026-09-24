// Package jobs provides async job processing for kombifyTechstack.
package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/identity"
	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/kombifyio/techstack/internal/portinventory"
	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/internal/runtimeproduct/runtimeaction"
	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/stackrouting"
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
	requests            []StackKitArtifactGenerateRequest
	resultStackSpecPath string
	err                 error
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
	resultStackSpecPath := firstNonEmpty(f.resultStackSpecPath, req.StackSpecPath)
	return &StackKitArtifactGenerateResult{
		StackSpecPath:    resultStackSpecPath,
		OutputDir:        req.OutputDir,
		ResolvedPlanPath: resolvedPlanPath,
		Metadata: map[string]string{
			"artifact_generator": "fake-stackkit-cli",
			"resolved_plan_hash": "sha256:" + strings.Repeat("a", 64),
		},
	}, nil
}

// Regression: managed address binding returns a derived StackSpec and the
// rollout must execute that document, not regenerate from the original intent.
func TestDeployHandlerRolloutUsesGeneratedStackSpec(t *testing.T) {
	stackID := "stack-generated-spec"
	specBaseDir := t.TempDir()
	persistDeployFixture(t, specBaseDir, stackID)
	generatedSpec := filepath.Join(specBaseDir, stackID, "stack-spec.address-bound.v2.json")
	rollout := &fakeRuntimeRunner{name: "rollout", result: map[string]interface{}{"status": "applied"}}
	handler := DeployHandler(&ProvisionConfig{
		WorkDir: t.TempDir(), SpecBaseDir: specBaseDir, StackKitsDir: writeJobsTestStackKitsDir(t),
		RuntimeActions: RuntimeActions{
			StackKitGenerator: &fakeStackKitArtifactGenerator{resultStackSpecPath: generatedSpec},
			SimulationGate:    &fakeRuntimeRunner{name: "simulate", result: map[string]interface{}{"status": "accepted"}},
			RolloutRunner:     rollout,
			RolloutVerifier:   &fakeRuntimeRunner{name: "verify", result: map[string]interface{}{"status": "verified"}},
			RestoreDrill:      &fakeRuntimeRunner{name: "restore", result: map[string]interface{}{"status": "verified"}},
		},
	})
	job := &Job{ID: "deploy-generated-spec", Type: JobTypeDeploy, TargetID: stackID, Payload: map[string]interface{}{"workers": deployWorkerPayload()}}
	if err := handler(context.Background(), job, &Queue{jobs: map[string]*Job{job.ID: job}}); err != nil {
		t.Fatalf("DeployHandler: %v", err)
	}
	if len(rollout.calls) != 1 || rollout.calls[0].StackSpecPath != generatedSpec {
		t.Fatalf("rollout StackSpec = %+v, want generated %q", rollout.calls, generatedSpec)
	}
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
	tfvars := []byte("{\n  \"domain\": \"home\",\n  \"enable_coolify\": true\n}\n")
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
domain: home
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

func provisionInstallCommand(t *testing.T, stackID string, simulation RuntimeActionRunner) (*Job, error) {
	t.Helper()
	cfg := &ProvisionConfig{
		WorkDir: t.TempDir(), SpecBaseDir: t.TempDir(), StackKitsDir: writeJobsTestStackKitsDir(t),
		RuntimeActions: RuntimeActions{SimulationGate: simulation},
	}
	job := &Job{
		ID: "test-job-" + stackID, Type: JobTypeProvision, TargetID: stackID, TargetName: stackID,
		Payload: map[string]interface{}{
			"owner_id": "auth0|staff", "tenant_id": "org-1",
			"actor": map[string]interface{}{
				"user_id": "auth0|staff", "tenant_id": "org-1", "roles": []interface{}{"developer"},
			},
			"spec": map[string]interface{}{
				"name": stackID, "kit": "modern-homelab",
				"nodes": []interface{}{
					map[string]interface{}{"name": "main-server", "type": "main", "provider": "local"},
				},
				"metadata": map[string]interface{}{
					"server_provisioning_mode":        "install-command",
					"server_connection_mode":          "agent-oneliner",
					"server_install_command_required": "true",
				},
			},
		},
	}
	return job, ProvisionHandler(cfg)(context.Background(), job, &Queue{jobs: map[string]*Job{job.ID: job}})
}

func TestProvisionHandler_InstallCommandDoesNotBlockWithoutSimulatePreview(t *testing.T) {
	job, err := provisionInstallCommand(t, "stack-oneliner-preview-missing", nil)
	if err != nil {
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
	job, err := provisionInstallCommand(t, "stack-oneliner-preview", simulation)
	if err != nil {
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
	startedAt := time.Now()
	job, err := provisionInstallCommand(t, "stack-oneliner-timeout", simulation)
	if err != nil {
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

func newManagedProvisionFixture(t *testing.T, stackID, stackName, provider string, leaseManager *fakeManagedLeaseManager) (JobHandler, *Job, *Queue) {
	t.Helper()

	job := &Job{
		ID:         "test-job-" + stackID,
		Type:       JobTypeProvision,
		TargetID:   stackID,
		TargetName: stackName,
		Payload: map[string]interface{}{
			"owner_id":  "user-1",
			"tenant_id": "org-1",
			"spec": map[string]interface{}{
				"name":        stackName,
				"provider":    "cloud",
				"provider_id": provider,
			},
		},
	}
	return ProvisionHandler(&ProvisionConfig{
		WorkDir:      t.TempDir(),
		SpecBaseDir:  t.TempDir(),
		StackKitsDir: writeJobsTestStackKitsDir(t),
		RuntimeActions: RuntimeActions{
			StackKitGenerator: &fakeStackKitArtifactGenerator{},
			LeaseManager:      leaseManager,
		},
	}), job, &Queue{jobs: map[string]*Job{job.ID: job}}
}

func TestProvisionHandler_ManagedCloudCreatesLeaseAndReturnsRuntimePhase(t *testing.T) {
	leaseManager := &fakeManagedLeaseManager{}
	handler, job, queue := newManagedProvisionFixture(t, "stack-managed", "managed-stack", "centron", leaseManager)
	spec := job.Payload["spec"].(map[string]interface{})
	spec["goals"] = map[string]interface{}{"storage": true}

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
	handler, job, queue := newManagedProvisionFixture(t, "stack-managed-pending", "managed-stack-pending", "ionos", leaseManager)

	err := handler(context.Background(), job, queue)
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
	prepared := ManagedLeaseRequest{
		StackID: "stack-managed-pending", StackName: "managed-stack-2", StackKit: DefaultCloudKitRef,
		TenantID: "org-1", OwnerID: "user-1", Provider: "ionos",
		OperationKey: PrimaryManagedLeaseOperationKey, RuntimeSlotKey: PrimaryManagedRuntimeSlotKey,
		RuntimeSlotGeneration: 1, NodeRole: "foundation",
	}
	handler, job, queue := newManagedProvisionFixture(t, "stack-managed-pending", "managed-stack-2", "ionos", leaseManager)
	job.Payload[PreparedManagedLeaseRequestPayloadKey] = ManagedLeaseRequestPayload(prepared)
	job.Payload["spec"].(map[string]interface{})["name"] = "managed-stack"
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

func provisionWithOwnerHandoffResult(t *testing.T, stackID string, verifierResult map[string]interface{}) (*Job, error) {
	t.Helper()
	cfg := &ProvisionConfig{
		WorkDir:             t.TempDir(),
		SpecBaseDir:         t.TempDir(),
		StackKitsDir:        writeJobsTestStackKitsDir(t),
		AutoDeployAdmission: allowAutoDeployAdmissionForTest,
		RuntimeActions: RuntimeActions{
			StackKitGenerator: &fakeStackKitArtifactGenerator{},
			LeaseManager:      &fakeManagedLeaseManager{},
			SimulationGate:    &fakeRuntimeRunner{name: "simulate", result: map[string]interface{}{"status": "accepted"}},
			RolloutRunner:     &fakeRuntimeRunner{name: "rollout", result: map[string]interface{}{"status": "applied"}},
			RolloutVerifier:   &fakeRuntimeRunner{name: "verify", result: verifierResult},
			RestoreDrill:      &fakeRuntimeRunner{name: "restore", result: map[string]interface{}{"status": "verified"}},
		},
	}
	configureRestoreOnlyCommander(t, cfg, nil)
	handler := ProvisionHandler(cfg)
	job := &Job{
		ID:       "test-job-handoff-" + stackID,
		Type:     JobTypeProvision,
		TargetID: stackID,
		Payload: map[string]interface{}{
			"auto_deploy": true,
			"owner_id":    "user-1",
			"tenant_id":   "org-1",
			"owner_spec_bootstrap": &OwnerSpecBootstrap{
				Endpoint:  "https://techstack.kombify.io/api/v1/stacks/" + stackID + "/owner-spec",
				Token:     "test-bootstrap-token",
				ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				Scopes:    []string{"owner_spec:read"},
			},
			"spec": map[string]interface{}{
				"name":        "managed-stack",
				"provider":    "cloud",
				"provider_id": "centron",
				"goals":       map[string]interface{}{"storage": true},
			},
		},
	}
	err := handler(t.Context(), job, &Queue{jobs: map[string]*Job{job.ID: job}})
	return job, err
}

func TestDeployHandler_SimulationFailureBlocksRollout(t *testing.T) {
	specBaseDir := t.TempDir()
	stackID := "stack-simulation-blocks"
	prepareStackSpecsForDeploy(t, specBaseDir, stackID)

	simulationErr := errors.New("simulation boom")
	simulation := &fakeRuntimeActionRunner{err: simulationErr}
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
	if !errors.Is(err, simulationErr) {
		t.Fatalf("simulation failure cause was lost: %v", err)
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

// Regression: native admission conflicts must remain identifiable across the
// provision-handler boundary so callers can choose a safe recovery path.
func TestProvisionHandler_PreservesNativeAdmissionConflict(t *testing.T) {
	handler := ProvisionHandler(&ProvisionConfig{
		WorkDir: t.TempDir(), SpecBaseDir: t.TempDir(), StackKitsDir: writeJobsTestStackKitsDir(t),
		RuntimeActions: RuntimeActions{LeaseManager: &fakeManagedLeaseManager{
			err: fmt.Errorf("admit managed lease: %w", providercontrol.ErrNativeAdmissionConflict),
		}},
	})
	job := &Job{ID: "native-conflict", Type: JobTypeProvision, TargetID: "stack-managed", Payload: map[string]interface{}{
		"owner_id": "user-1", "tenant_id": "org-1",
		"spec": map[string]interface{}{"name": "managed-stack", "provider": "cloud", "provider_id": "centron"},
	}}
	err := handler(context.Background(), job, &Queue{jobs: map[string]*Job{job.ID: job}})
	if !errors.Is(err, providercontrol.ErrNativeAdmissionConflict) || job.Step != StepCreateLease {
		t.Fatalf("native admission conflict = %#v at step %q", err, job.Step)
	}
}

func TestProvisionHandler_AutoDeploysManagedCloudStack(t *testing.T) {
	order := []string{}
	diagnostics := &fakeRuntimeDiagnosticsCollector{}
	simulation := &fakeRuntimeRunner{name: "simulate", order: &order}
	rollout := &fakeRuntimeRunner{name: "rollout", order: &order}
	// Native-v2 managed rollout does not request an owner bootstrap envelope,
	// so verified runtime evidence is sufficient without synthetic login data.
	verify := &fakeRuntimeRunner{name: "verify", order: &order, result: map[string]interface{}{"status": "verified"}}
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
		OperationID:  "operation-test",
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
			DiagnosticsCollector:  diagnostics,
			SimulationGate:        simulation,
			RolloutRunner:         rollout,
			RolloutVerifier:       verify,
			RestoreDrill:          restore,
		},
	}
	configureRestoreOnlyCommander(t, cfg, &order)

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
		update.Metadata[metadataKeyRuntimePublicIP] != "203.0.113.30" {
		t.Fatalf("metadata update = %+v, want managed target identity", update.Metadata)
	}
	if len(resolver.requests) != 1 {
		t.Fatalf("resolver requests = %d, want 1", len(resolver.requests))
	}
	if len(diagnostics.requests) != 1 || diagnostics.requests[0].Reason != "managed_runtime_enrollment_succeeded" || diagnostics.requests[0].OperationID != "operation-test" {
		t.Fatalf("successful enrollment diagnostics = %+v", diagnostics.requests)
	}
	diagnosticBinding := mapFromInterface(mapFromInterface(job.Result["runtime_diagnostics"])["binding"])
	if diagnosticBinding["operation_id"] != "operation-test" || diagnosticBinding["server_id"] != runtimeidentity.LeaseServerID("lease-test") {
		t.Fatalf("successful enrollment diagnostic binding = %+v", diagnosticBinding)
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

func TestProvisionHandler_ManagedRestoreDoesNotInvokeLegacyReadinessRetry(t *testing.T) {
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
	configureRestoreOnlyCommander(t, cfg, &order)
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
	wantOrder := []string{"simulate", "rollout", "verify", "restore"}
	if strings.Join(order, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("runtime action order = %v, want %v", order, wantOrder)
	}
	if len(restore.calls) != 0 {
		t.Fatalf("legacy restore calls = %d, want native commander only", len(restore.calls))
	}
	if got := job.Result["runtime_phase"]; got != string(RuntimePhaseVerified) {
		t.Fatalf("runtime_phase = %v, want %s", got, RuntimePhaseVerified)
	}
}

type managedKombifyMeFixture struct {
	handler      JobHandler
	job          *Job
	queue        *Queue
	leaseManager *fakeManagedLeaseManager
	order        *[]string
	specBaseDir  string
}

func newManagedKombifyMeFixture(t *testing.T, stackID, stackName string, generator StackKitArtifactGenerator, routingStore stackrouting.Store) managedKombifyMeFixture {
	t.Helper()

	order := []string{}
	leaseManager := &fakeManagedLeaseManager{result: &ManagedLeaseResult{
		LeaseID: "lease-ionos", Provider: "ionos", DesiredState: "running", Phase: RuntimePhaseLeaseReady,
		Target: &ManagedRuntimeTarget{
			Host: "203.0.113.10", PublicIP: "203.0.113.10", SSHUser: "ubuntu", SSHPort: 22, Source: "test-lease",
		},
	}}
	job := &Job{
		ID: "test-job-" + stackID, Type: JobTypeProvision, TargetID: stackID,
		Payload: map[string]interface{}{
			"auto_deploy": true, "owner_id": "user-1", "tenant_id": "org-1",
			"spec": map[string]interface{}{
				"name": stackName, "stackkit": "cloud-kit", "provider": "cloud", "context": "cloud", "domain": "kombify.me",
				"network": map[string]interface{}{"mode": "public"},
				"metadata": map[string]interface{}{
					"address_mode": "kombify-me", "provider_id": "ionos", "server_provisioning_mode": "kombify-cloud",
					"server_connection_mode": "managed-subscription", "runtime_lane": "monthly-runtime", "stackkit_catalog_ref": "cloud-kit",
				},
				"services": map[string]interface{}{"coolify": map[string]interface{}{"enabled": true}},
			},
		},
	}
	specBaseDir := t.TempDir()
	cfg := &ProvisionConfig{
		WorkDir: t.TempDir(), SpecBaseDir: specBaseDir, StackKitsDir: writeJobsTestStackKitsDir(t),
		AutoDeployAdmission: allowAutoDeployAdmissionForTest, RoutingStore: routingStore,
		RuntimeActions: RuntimeActions{
			LeaseManager: leaseManager, RuntimeTargetResolver: &fakeManagedRuntimeTargetResolver{}, StackKitGenerator: generator,
			SimulationGate:  &fakeRuntimeRunner{name: "simulate", order: &order},
			RolloutRunner:   &fakeRuntimeRunner{name: "rollout", order: &order},
			RolloutVerifier: &fakeRuntimeRunner{name: "verify", order: &order, result: stackKitIdentityHandoffResult(stackID)},
			RestoreDrill:    &fakeRuntimeRunner{name: "restore", order: &order, result: map[string]interface{}{"status": "verified"}},
		},
	}
	configureRestoreOnlyCommander(t, cfg, &order)
	return managedKombifyMeFixture{
		handler: ProvisionHandler(cfg), job: job, queue: &Queue{jobs: map[string]*Job{job.ID: job}},
		leaseManager: leaseManager, order: &order, specBaseDir: specBaseDir,
	}
}

func TestProvisionHandler_AutoDeployManagedKombifyMeDefersRouteRegistrationToStackKits(t *testing.T) {
	stackKitGenerator := &fakeStackKitArtifactGenerator{}
	fixture := newManagedKombifyMeFixture(t, "stack-managed-kombify-me", "managed-kombify-me", stackKitGenerator, nil)
	handler, job, queue := fixture.handler, fixture.job, fixture.queue
	leaseManager, specBaseDir := fixture.leaseManager, fixture.specBaseDir

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

func TestProvisionHandler_ManagedKombifyMeWithoutRoutingAllocationFailsBeforeArtifacts(t *testing.T) {
	stackKitGenerator := &kombifyMeOutageStackKitGenerator{}
	fixture := newManagedKombifyMeFixture(
		t, "stack-managed-kombify-me-outage", "managed-kombify-me-provider-offline", stackKitGenerator, stackrouting.NewMemoryStore(),
	)

	err := fixture.handler(context.Background(), fixture.job, fixture.queue)
	if err == nil {
		t.Fatal("ProvisionHandler should fail before generating artifacts without managed routing")
	}
	var provisionErr *ProvisionError
	if !errors.As(err, &provisionErr) || provisionErr.Step != StepPrepareRollout || strings.TrimSpace(provisionErr.Details) == "" {
		t.Fatalf("ProvisionHandler error = %#v, want actionable domain assignment guidance", err)
	}
	if len(stackKitGenerator.requests) != 0 {
		t.Fatalf("StackKits artifact requests = %d, want none before routing assignment", len(stackKitGenerator.requests))
	}
	if got := strings.Join(*fixture.order, ","); got != "" {
		t.Fatalf("runtime action order = %s, want no runtime rollout after address failure", got)
	}
}

func TestProvisionHandler_AutoDeployManagedKombifyMeQuotaFailsAtGenerateIaCWithLeaseMetadata(t *testing.T) {
	incidentError := strings.Join([]string{
		"StackKits CLI generate failed: exit status 1: WARN legacy stackkit install mode normalized from=simple to=bootstrapped",
		"Registering subdomains on kombify.me...",
		`Error: kombify.me registration failed and no subdomainPrefix is configured: auto-register base subdomain: API error 429: {"error":"base subdomain limit reached (max 5 per user)"}`,
	}, "\n")
	stackKitGenerator := &kombifyMeOutageStackKitGenerator{errText: incidentError}
	fixture := newManagedKombifyMeFixture(t, "stack-managed-kombify-me-quota", "managed-kombify-me-quota", stackKitGenerator, nil)
	job := fixture.job

	err := fixture.handler(context.Background(), job, fixture.queue)
	if err == nil {
		t.Fatal("ProvisionHandler should fail when kombify.me base subdomain quota blocks artifact generation")
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
	if got := strings.Join(*fixture.order, ","); got != "" {
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
	if job.Step != StepPrepareRollout {
		t.Fatalf("job step = %q, want %q", job.Step, StepPrepareRollout)
	}
}

type queuedManagedAutoDeployFixture struct {
	stackID, leaseID, provider string
	waitTimeout                time.Duration
	admission                  AutoDeployAdmission
	resolver                   ManagedRuntimeTargetResolver
}

func runQueuedManagedAutoDeploy(t *testing.T, fixture queuedManagedAutoDeployFixture) JobSnapshot {
	t.Helper()
	order := []string{}
	leaseManager := &fakeManagedLeaseManager{result: &ManagedLeaseResult{
		LeaseID: fixture.leaseID, Provider: fixture.provider,
		DesiredState: "running", Phase: RuntimePhaseLeaseReady,
	}}
	cfg := &ProvisionConfig{
		WorkDir: t.TempDir(), SpecBaseDir: t.TempDir(), StackKitsDir: writeJobsTestStackKitsDir(t),
		ManagedRuntimeTargetWaitTimeout: fixture.waitTimeout, ManagedRuntimeTargetPollInterval: time.Millisecond,
		AutoDeployAdmission: fixture.admission,
		RuntimeActions: RuntimeActions{
			StackKitGenerator: &fakeStackKitArtifactGenerator{}, LeaseManager: leaseManager,
			RuntimeTargetResolver: fixture.resolver,
			SimulationGate:        &fakeRuntimeRunner{name: "simulate", order: &order, result: map[string]interface{}{"status": "accepted"}},
			RolloutRunner:         &fakeRuntimeRunner{name: "rollout", order: &order, result: map[string]interface{}{"status": "applied"}},
			RolloutVerifier:       &fakeRuntimeRunner{name: "verify", order: &order, result: stackKitIdentityHandoffResult(fixture.stackID)},
			RestoreDrill:          &fakeRuntimeRunner{name: "restore", order: &order, result: map[string]interface{}{"status": "verified"}},
		},
	}
	configureRestoreOnlyCommander(t, cfg, &order)
	queue := NewQueue(1, nil)
	queue.RegisterHandler(JobTypeProvision, ProvisionHandler(cfg))
	queue.RegisterHandler(JobTypeDeploy, DeployHandler(cfg))
	job := &Job{
		ID: "test-job-managed-cloud-" + fixture.stackID, Type: JobTypeProvision, TargetID: fixture.stackID,
		Payload: map[string]interface{}{
			"auto_deploy": true, "owner_id": "user-1", "tenant_id": "org-1",
			"spec": map[string]interface{}{"name": "managed-stack", "provider": "cloud", "provider_id": fixture.provider},
		},
	}
	if err := queue.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	queue.Start(context.Background())
	defer queue.Stop()

	deadline := time.Now().Add(5 * time.Second)
	snapshot := job.Snapshot()
	for !isTerminalJobState(snapshot.State) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		snapshot = job.Snapshot()
	}
	if snapshot.State != JobStateCompleted || snapshot.Type != JobTypeDeploy {
		t.Fatalf("queued auto-deploy state/type=%q/%q error=%q details=%q", snapshot.State, snapshot.Type, snapshot.Error, snapshot.ErrorDetails)
	}
	if len(leaseManager.requests) != 1 {
		t.Fatalf("lease requests=%d, want prepared lease reused", len(leaseManager.requests))
	}
	if strings.Join(order, ",") != "simulate,rollout,verify,restore" {
		t.Fatalf("runtime action order = %v", order)
	}
	return snapshot
}

func TestProvisionHandler_AutoDeployQueueResumePreservesPreparedResult(t *testing.T) {
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
	snapshot := runQueuedManagedAutoDeploy(t, queuedManagedAutoDeployFixture{
		stackID: "stack-queue-resume", leaseID: "lease-queue-resume", provider: "centron",
		waitTimeout: 100 * time.Millisecond, admission: allowAutoDeployAdmissionForTest, resolver: resolver,
	})
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
}

func TestProvisionHandler_AutoDeployGuardResumeReusesPreparedLease(t *testing.T) {
	admissionCalls := 0
	resolver := &sequenceManagedRuntimeTargetResolver{targets: []*ManagedRuntimeTarget{{
		Host: "203.0.113.57", PublicIP: "203.0.113.57", SSHUser: "ubuntu", SSHPort: 22,
		SSHPrivateKey: "test-private-key", Source: "test-enrollment",
	}}}
	snapshot := runQueuedManagedAutoDeploy(t, queuedManagedAutoDeployFixture{
		stackID: "stack-guard-resume", leaseID: "lease-guard-resume", provider: "ionos", waitTimeout: 250 * time.Millisecond,
		resolver: resolver,
		admission: func(context.Context, AutoDeployAdmissionRequest) error {
			admissionCalls++
			if admissionCalls == 1 {
				return errors.New("guard heartbeat is not fresh yet")
			}
			return nil
		},
	})
	if admissionCalls < 2 {
		t.Fatalf("admission calls=%d, want initial wait and resumed admission", admissionCalls)
	}
	if got := stringFromMap(snapshot.Result, leaseIDField); got != "lease-guard-resume" {
		t.Fatalf("preserved lease_id=%q, want lease-guard-resume", got)
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
	if len(resolver.requests) == 0 {
		t.Fatal("expected resolver to be called")
	}
	if job.Step != StepPrepareRollout {
		t.Fatalf("job step = %q, want %q", job.Step, StepPrepareRollout)
	}
}

func TestProvisionHandler_AutoDeployStopsWaitingOnTerminalResolverErrors(t *testing.T) {
	for _, tc := range []struct {
		name, leaseID, operationID string
		resolverErr                error
		wantErr                    error
		wantDiagnostics            bool
	}{
		{
			name: "managed runtime enrollment failed", leaseID: "lease-failed", operationID: "operation-failed",
			resolverErr: fmt.Errorf("%w for lease %q: ionos quota exhausted", ErrManagedRuntimeEnrollmentFailed, "lease-failed"),
			wantErr:     ErrManagedRuntimeEnrollmentFailed, wantDiagnostics: true,
		},
		{
			name: "monthly runtime feature disabled", leaseID: "lease-feature-disabled",
			resolverErr: fmt.Errorf("%w: %s", monthlyruntime.ErrFeatureDisabled, "sim.monthly.runtime.standard"),
			wantErr:     monthlyruntime.ErrFeatureDisabled,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := &fakeManagedRuntimeTargetResolver{err: tc.resolverErr}
			collector := &fakeRuntimeDiagnosticsCollector{}
			lease := &ManagedLeaseResult{
				LeaseID: tc.leaseID, OperationID: tc.operationID, Provider: "ionos",
				DesiredState: "running", Phase: RuntimePhaseLeaseReady,
			}
			if tc.wantDiagnostics {
				lease.Target = &ManagedRuntimeTarget{
					Host: "203.0.113.12", SSHUser: "ubuntu", SSHPort: 22, SSHPrivateKey: "diagnostic-key",
				}
			}
			cfg := &ProvisionConfig{
				WorkDir: t.TempDir(), SpecBaseDir: t.TempDir(), StackKitsDir: writeJobsTestStackKitsDir(t),
				ManagedRuntimeTargetWaitTimeout: time.Second, ManagedRuntimeTargetPollInterval: 10 * time.Millisecond,
				AutoDeployAdmission: allowAutoDeployAdmissionForTest,
				RuntimeActions: RuntimeActions{
					LeaseManager: &fakeManagedLeaseManager{result: lease}, RuntimeTargetResolver: resolver,
					DiagnosticsCollector: collector, SimulationGate: &fakeRuntimeRunner{name: "simulate"},
					RolloutRunner: &fakeRuntimeRunner{name: "rollout"}, RolloutVerifier: &fakeRuntimeRunner{name: "verify"},
					RestoreDrill: &fakeRuntimeRunner{name: "restore"},
				},
			}
			job := &Job{
				ID: "test-job-" + tc.leaseID, Type: JobTypeProvision, TargetID: "stack-terminal-resolver",
				Payload: map[string]interface{}{
					"auto_deploy": true, "owner_id": "user-1", "tenant_id": "org-1",
					"spec": map[string]interface{}{"name": "managed-stack", "provider": "cloud", "provider_id": "centron"},
				},
			}
			err := ProvisionHandler(cfg)(context.Background(), job, &Queue{jobs: map[string]*Job{job.ID: job}})
			if !errors.Is(err, tc.wantErr) || len(resolver.requests) != 1 || job.Step != StepPrepareRollout {
				t.Fatalf("error=%T %v resolver requests=%d step=%q, want immediate %v at %s",
					err, err, len(resolver.requests), job.Step, tc.wantErr, StepPrepareRollout)
			}
			if !tc.wantDiagnostics {
				return
			}
			if len(collector.requests) != 1 {
				t.Fatalf("diagnostic requests = %d, want one terminal enrollment receipt", len(collector.requests))
			}
			diagnosticsRequest := collector.requests[0]
			if diagnosticsRequest.Action != "managed_runtime_enrollment" || diagnosticsRequest.Reason != "managed_runtime_enrollment_failed" ||
				diagnosticsRequest.OperationID != tc.operationID || diagnosticsRequest.LeaseID != tc.leaseID ||
				diagnosticsRequest.ServerID != runtimeidentity.LeaseServerID(tc.leaseID) ||
				diagnosticsRequest.RuntimeAgentID != runtimeidentity.LeaseRuntimeAgentID("org-1", tc.leaseID) {
				t.Fatalf("diagnostic request = %+v", diagnosticsRequest)
			}
			binding := mapFromInterface(mapFromInterface(job.Result["runtime_diagnostics"])["binding"])
			if binding["operation_id"] != tc.operationID || binding["server_id"] != runtimeidentity.LeaseServerID(tc.leaseID) {
				t.Fatalf("durable diagnostic binding = %+v", binding)
			}
		})
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
	configureRestoreOnlyCommander(t, cfg, &order)

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

func TestProvisionHandler_AutoDeployFailsWhenOwnerHandoffMissing(t *testing.T) {
	// When the orchestrator issues an owner-spec bootstrap token, StackKit
	// is contractually required to return identity, login_gateway, and
	// recovery outputs. A "verified" response without these fields means
	// the freshly provisioned stack has no usable owner login, so the
	// provision job must fail loudly rather than report success.
	job, err := provisionWithOwnerHandoffResult(t, "stack-handoff-missing", map[string]interface{}{"status": "verified"})
	if err == nil {
		t.Fatal("expected provision to fail when stackkit_outputs missing identity handoff")
	}
	if job.Step != StepVerifyRollout {
		t.Fatalf("expected job.Step=%q, got %q", StepVerifyRollout, job.Step)
	}
}

func TestProvisionHandler_AutoDeploySucceedsWhenHandoffComplete(t *testing.T) {
	// Mirror image of the failing test: when the RolloutVerifier returns the
	// full identity handoff, provision must complete and surface
	// stackkit_outputs on the job result for the frontend to render.
	stackID := "stack-handoff-ok"
	completeHandoff := stackKitIdentityHandoffResult(stackID)
	completeHandoff["stackkit_outputs"].(map[string]interface{})["services"] = []interface{}{
		map[string]interface{}{
			"name":   "whoami",
			"url":    "https://whoami.stack-handoff-ok.kombify.me",
			"status": "healthy",
		},
	}
	job, err := provisionWithOwnerHandoffResult(t, stackID, completeHandoff)
	if err != nil {
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

func newPersistedDeployFixture(t *testing.T, stackID string, actions RuntimeActions) (JobHandler, *Job, *Queue, string) {
	t.Helper()

	specBaseDir := t.TempDir()
	persistDeployFixture(t, specBaseDir, stackID)
	job := &Job{
		ID: "deploy-job-" + stackID, Type: JobTypeDeploy, TargetID: stackID,
		Payload: map[string]interface{}{"workers": deployWorkerPayload()},
	}
	handler := DeployHandler(&ProvisionConfig{
		WorkDir: t.TempDir(), SpecBaseDir: specBaseDir, StackKitsDir: writeJobsTestStackKitsDir(t), RuntimeActions: actions,
	})
	return handler, job, &Queue{jobs: map[string]*Job{job.ID: job}}, specBaseDir
}

func TestDeployHandler_RunsStackKitsRuntimeActionsAndRecordsE2EProof(t *testing.T) {
	stackID := "stack-rollout"
	order := []string{}
	sim := &fakeRuntimeRunner{name: "simulate", order: &order, result: map[string]interface{}{"status": "accepted", "mode": "dry-run"}}
	rollout := &fakeRuntimeRunner{name: "rollout", order: &order, result: map[string]interface{}{"status": "applied", "mode": "apply"}}
	verify := &fakeRuntimeRunner{name: "verify", order: &order, result: map[string]interface{}{"status": "verified", "mode": "apply"}}
	restore := &fakeRuntimeRunner{name: "restore", order: &order, result: map[string]interface{}{"status": "verified", "mode": "apply"}}
	handler, job, queue, _ := newPersistedDeployFixture(t, stackID, RuntimeActions{
		StackKitGenerator: &fakeStackKitArtifactGenerator{},
		SimulationGate:    sim,
		RolloutRunner:     rollout,
		RolloutVerifier:   verify,
		RestoreDrill:      restore,
	})

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
	if got := job.Result["status"]; got != "deployed" {
		t.Fatalf("status = %v, want deployed", got)
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
	order := []string{}
	rollout := &fakeRuntimeRunner{name: "rollout", order: &order, result: map[string]interface{}{"status": "applied", "mode": "apply"}}
	stackKitGenerator := &fakeStackKitArtifactGenerator{}
	handler, job, queue, specBaseDir := newPersistedDeployFixture(t, stackID, RuntimeActions{
		StackKitGenerator: stackKitGenerator,
		SimulationGate:    &fakeRuntimeRunner{name: "simulate", order: &order, result: map[string]interface{}{"status": "accepted", "mode": "dry-run"}},
		RolloutRunner:     rollout,
		RolloutVerifier:   &fakeRuntimeRunner{name: "verify", order: &order, result: map[string]interface{}{"status": "verified", "mode": "apply"}},
		RestoreDrill:      &fakeRuntimeRunner{name: "restore", order: &order, result: map[string]interface{}{"status": "verified", "mode": "apply"}},
	})
	persister, err := unifier.NewSpecPersisterWithPath(filepath.Join(specBaseDir, stackID))
	if err != nil {
		t.Fatalf("create persister: %v", err)
	}
	if _, _, err := persister.SaveStackSpecBytes([]byte(`name: basekit-test
stackkit: basement-kit
mode: simple
runtime: docker
context: local
domain: home
nodes:
  - name: main
    role: standalone
services:
  immich:
    enabled: true
`)); err != nil {
		t.Fatalf("save stack spec: %v", err)
	}

	job.Payload["workers"] = []interface{}{
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
	}

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
	handler, job, queue, _ := newPersistedDeployFixture(t, stackID, RuntimeActions{
		StackKitGenerator: &fakeStackKitArtifactGenerator{},
		SimulationGate:    &fakeRuntimeRunner{name: "simulate", result: map[string]interface{}{"status": "accepted"}},
		RolloutRunner:     &fakeRuntimeRunner{name: "rollout", result: map[string]interface{}{"status": "applied"}},
		RolloutVerifier:   &fakeRuntimeRunner{name: "verify", result: map[string]interface{}{"status": "verified"}},
		RestoreDrill:      &fakeRuntimeRunner{name: "restore", result: map[string]interface{}{"status": "skipped", "checks": []map[string]string{{"name": "restore_drill_adapter", "status": "skipped"}}}},
	})

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

	handler, job, queue, _ := newPersistedDeployFixture(t, stackID, RuntimeActions{
		StackKitGenerator: &fakeStackKitArtifactGenerator{},
		SimulationGate:    newRunner(simulateServer.URL, runtimeActionTargetSimulate, runtimeActionSimulateUpdate, defaultSimulationGatePath),
		RolloutRunner:     newRunner(stackKitsServer.URL, runtimeActionTargetStackKits, string(StepRolloutRunner), defaultStackKitsRolloutPath),
		RolloutVerifier:   newRunner(stackKitsServer.URL, runtimeActionTargetStackKits, string(StepVerifyRollout), defaultStackKitsVerifyPath),
		RestoreDrill:      newRunner(stackKitsServer.URL, runtimeActionTargetStackKits, string(StepRestoreDrill), defaultRestoreDrillPath),
	})

	err := handler(context.Background(), job, queue)
	if err == nil {
		t.Fatal("expected fail-closed legacy HTTP rejection")
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
	handler, job, queue, _ := newPersistedDeployFixture(t, stackID, RuntimeActions{
		StackKitGenerator: &fakeStackKitArtifactGenerator{},
		SimulationGate:    &fakeRuntimeRunner{name: "simulate"},
		RolloutVerifier:   &fakeRuntimeRunner{name: "verify"},
		RestoreDrill:      &fakeRuntimeRunner{name: "restore"},
	})

	err := handler(context.Background(), job, queue)
	if err == nil {
		t.Fatal("expected missing StackKits rollout runner to fail")
	}
	if job.Step != StepRolloutRunner {
		t.Fatalf("job step = %q, want %q", job.Step, StepRolloutRunner)
	}
}

func TestDestroyHandler_DecommissionsManagedRuntimeBeforeWorkspaceCheck(t *testing.T) {
	order := []string{}
	authority := newRecordingCurrentPortAuthority(&order)
	portSnapshot := testManagedPortTeardownSnapshot("org-1", "user-1", "stack-managed")
	decommissioner := &fakeManagedLeaseDecommissioner{result: &ManagedLeaseDecommissionResult{
		Decommissioned: 2,
		LeaseIDs:       []string{"lease-centron", "lease-ionos"},
		Proofs: []ManagedLeaseDecommissionProof{
			testManagedLeaseDecommissionProof("stack-managed", "org-1", "lease-centron", ManagedLeaseDecommissionObservedDecommissioned, ""),
			testManagedLeaseDecommissionProof("stack-managed", "org-1", "lease-ionos", ManagedLeaseDecommissionObservedDecommissioned, ""),
		},
	}}
	cfg := &ProvisionConfig{
		WorkDir:       t.TempDir(),
		PortInventory: authority,
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
		Result: map[string]interface{}{PortTeardownSnapshotResultField: portSnapshot},
	}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}

	if err := handler(context.Background(), job, queue); err != nil {
		t.Fatalf("DestroyHandler: %v", err)
	}
	if err := handler(context.Background(), job, queue); err != nil {
		t.Fatalf("DestroyHandler replay: %v", err)
	}
	if len(decommissioner.requests) != 2 {
		t.Fatalf("decommission requests = %d, want replay-safe 2", len(decommissioner.requests))
	}
	req := decommissioner.requests[0]
	if req.StackID != "stack-managed" || req.TenantID != "org-1" || req.OwnerID != "user-1" {
		t.Fatalf("decommission request = %+v", req)
	}
	if job.Progress != 100 {
		t.Fatalf("progress = %d, want 100", job.Progress)
	}
	if !reflect.DeepEqual(order, []string{"release_snapshot", "release_snapshot"}) || len(authority.releasedSnapshots) != 2 || authority.releasedSnapshots[1].SnapshotDigest != portSnapshot.SnapshotDigest {
		t.Fatalf("port teardown release = order %v snapshots %+v, want exact durable snapshot", order, authority.releasedSnapshots)
	}
	if job.Result[PortTeardownReleasedResultField] != portSnapshot.SnapshotDigest {
		t.Fatalf("port teardown release receipt = %#v, want snapshot digest", job.Result[PortTeardownReleasedResultField])
	}
}

func testManagedPortTeardownSnapshot(tenantID, ownerID, stackID string) portinventory.TeardownSnapshot {
	snapshot := portinventory.TeardownSnapshot{
		APIVersion: portinventory.TeardownSnapshotAPIVersion, TenantID: tenantID,
		OwnerSubjectID: ownerID, TechstackID: stackID, Generations: []portinventory.TeardownGeneration{},
	}
	data, _ := json.Marshal(snapshot)
	digest := sha256.Sum256(data)
	snapshot.SnapshotDigest = "sha256:" + hex.EncodeToString(digest[:])
	return snapshot
}

func testPortTeardownSnapshotForPlan(tenantID, ownerID, stackID, planHash string) portinventory.TeardownSnapshot {
	return testPortTeardownSnapshotForNodes(tenantID, ownerID, stackID, planHash, map[string]string{"server-local": "main"})
}

func testPortTeardownSnapshotForNodes(tenantID, ownerID, stackID, planHash string, serverNodes map[string]string) portinventory.TeardownSnapshot {
	snapshot := portinventory.TeardownSnapshot{
		APIVersion: portinventory.TeardownSnapshotAPIVersion, TenantID: tenantID,
		OwnerSubjectID: ownerID, TechstackID: stackID, Generations: make([]portinventory.TeardownGeneration, 0, len(serverNodes)),
	}
	servers := make([]string, 0, len(serverNodes))
	for serverID := range serverNodes {
		servers = append(servers, serverID)
	}
	sort.Strings(servers)
	for index, serverID := range servers {
		snapshot.Generations = append(snapshot.Generations, portinventory.TeardownGeneration{
			GenerationRef: portinventory.GenerationRef{
				ServerRef: portinventory.ServerRef{TenantID: tenantID, ServerID: serverID, ServerGeneration: int64(index + 1)},
				StackID:   stackID, ResolvedPlanHash: planHash,
			},
			ClaimSetDigest: "sha256:" + strings.Repeat(string(rune('a'+index)), 64), NodeRefs: []string{serverNodes[serverID]},
		})
	}
	data, _ := json.Marshal(snapshot)
	digest := sha256.Sum256(data)
	snapshot.SnapshotDigest = "sha256:" + hex.EncodeToString(digest[:])
	return snapshot
}

func TestDestroyHandler_FailsClosedWithoutExactManagedAbsenceProof(t *testing.T) {
	validProof := &ManagedLeaseDecommissionResult{
		Decommissioned: 1, LeaseIDs: []string{"lease-managed"},
		Proofs: []ManagedLeaseDecommissionProof{
			testManagedLeaseDecommissionProof("stack-managed", "org-1", "lease-managed", ManagedLeaseDecommissionObservedDecommissioned, ""),
		},
	}
	validSnapshot := testManagedPortTeardownSnapshot("org-1", "user-1", "stack-managed")
	for _, tc := range []struct {
		name             string
		classified       bool
		managed          bool
		result           *ManagedLeaseDecommissionResult
		decommissionErr  error
		noDecommissioner bool
		jobResult        map[string]interface{}
		wantErr          error
	}{
		{name: "missing durable snapshot", classified: true, managed: true, result: validProof},
		{name: "mismatched durable snapshot", classified: true, managed: true, result: validProof, jobResult: map[string]interface{}{PortTeardownSnapshotResultField: testManagedPortTeardownSnapshot("org-1", "user-1", "another-stack")}},
		{name: "missing provider proof", classified: true, managed: true, result: &ManagedLeaseDecommissionResult{Decommissioned: 1, LeaseIDs: []string{"lease-managed"}}, jobResult: map[string]interface{}{PortTeardownSnapshotResultField: validSnapshot}, wantErr: ErrManagedLeaseDecommissionProofRequired},
		{name: "waiting provider proof", classified: true, managed: true, decommissionErr: &JobWaitError{Reason: "waiting_provider_decommission", ResumeAfter: time.Second}, jobResult: map[string]interface{}{PortTeardownSnapshotResultField: validSnapshot}},
		{name: "local destroy cannot use provider proof", classified: true, jobResult: map[string]interface{}{PortTeardownSnapshotResultField: testPortTeardownSnapshotForPlan("org-1", "user-1", "stack-managed", "sha256:"+strings.Repeat("a", 64))}},
		{name: "old job has no classification", wantErr: ErrManagedLeaseDecommissionProofRequired},
		{name: "managed stack has no native decommissioner", classified: true, managed: true, noDecommissioner: true, wantErr: ErrManagedLeaseDecommissionUnavailable},
		{name: "adapter returns a count without terminal readback", classified: true, managed: true, result: &ManagedLeaseDecommissionResult{Decommissioned: 1, LeaseIDs: []string{"lease-unproven"}}, wantErr: ErrManagedLeaseDecommissionProofRequired},
		{
			name: "duplicate proof cannot hide an uncovered lease", classified: true, managed: true,
			result: &ManagedLeaseDecommissionResult{
				Decommissioned: 2,
				LeaseIDs:       []string{"lease-a", "lease-b"},
				Proofs: []ManagedLeaseDecommissionProof{
					testManagedLeaseDecommissionProof("stack-managed", "org-1", "lease-a", ManagedLeaseDecommissionObservedDecommissioned, ""),
					testManagedLeaseDecommissionProof("stack-managed", "org-1", "lease-a", ManagedLeaseDecommissionObservedDecommissioned, ""),
				},
			},
			wantErr: ErrManagedLeaseDecommissionProofRequired,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			order := []string{}
			authority := newRecordingCurrentPortAuthority(&order)
			payload := map[string]interface{}{"owner_id": "user-1", "tenant_id": "org-1"}
			if tc.classified {
				payload[ManagedRuntimeDecommissionRequiredField] = tc.managed
			}
			job := &Job{
				ID: "destroy-port-retain", Type: JobTypeDestroy, TargetID: "stack-managed",
				Payload: payload,
				Result:  tc.jobResult,
			}
			queue := &Queue{jobs: map[string]*Job{job.ID: job}}
			var decommissioner ManagedLeaseDecommissioner
			if !tc.noDecommissioner {
				decommissioner = &fakeManagedLeaseDecommissioner{result: tc.result, err: tc.decommissionErr}
			}
			err := DestroyHandler(&ProvisionConfig{
				WorkDir: t.TempDir(), PortInventory: authority,
				RuntimeActions: RuntimeActions{LeaseDecommissioner: decommissioner},
			})(t.Context(), job, queue)
			if err == nil {
				t.Fatal("DestroyHandler() error = nil, want fail-closed error")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("DestroyHandler() error = %v, want %v", err, tc.wantErr)
			}
			if len(authority.releasedSnapshots) != 0 || job.Result[PortTeardownReleasedResultField] != nil {
				t.Fatalf("unproven release mutated claims: snapshots=%+v receipt=%#v", authority.releasedSnapshots, job.Result[PortTeardownReleasedResultField])
			}
			if job.Progress == 100 {
				t.Fatal("destroy reported terminal success without an exact native provider proof")
			}
		})
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
			if err == nil {
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
		"command_result": map[string]interface{}{
			"schemaVersion": "stackkit.command-result/v1",
			"data": map[string]interface{}{
				"schemaVersion": "stackkit.apply-result/v2",
				"apply": map[string]interface{}{
					"resultHash": "sha256:apply", "planHash": "sha256:plan", "appliedRequestDigest": "sha256:request",
					"appliedWorkloads": []interface{}{map[string]interface{}{
						"workloadRef": "photos", "runtimeOwnerRef": "coolify", "api_token": "must-not-persist",
					}},
				},
				"observations": []interface{}{map[string]interface{}{
					"schemaVersion": "stackkit.runtime-observation/v2",
					"identity": map[string]interface{}{
						"stackId": "stack-1", "planHash": "sha256:plan", "api_token": "must-not-persist",
					},
					"runtime": []interface{}{map[string]interface{}{
						"requirementId": "runtime-photos", "instanceRef": "immich-server-node-main",
						"siteRef": "home", "nodeRef": "main", "executionChannelRef": "local-home-main",
					}},
				}},
			},
		},
	}
	outputs := map[string]interface{}{}
	mergeStackKitOutputs(outputs, result)
	observation := mapFromInterface(outputs["observation"])
	if resultString(observation, "version") != "stackkit.runtime-observation/v1" {
		t.Fatalf("versioned observation was not persisted: %#v", outputs)
	}
	host := mapFromInterface(observation["host"])
	if host["api_token"] != nil || host["reachable"] != true {
		t.Fatalf("observation was not sanitized: %#v", observation)
	}
	observations, ok := outputs["observations"].([]interface{})
	if !ok || len(observations) == 0 {
		t.Fatalf("typed runtime observations were not persisted: %#v", outputs)
	}
	typedObservation, ok := observations[0].(map[string]interface{})
	identity := mapFromInterface(typedObservation["identity"])
	if !ok || resultString(identity, "stackId") != "stack-1" || identity["api_token"] != nil {
		t.Fatalf("typed runtime observations were not sanitized: %#v", observations)
	}
	apply := mapFromInterface(outputs["apply"])
	workloads, ok := apply["appliedWorkloads"].([]interface{})
	if !ok || len(workloads) == 0 {
		t.Fatalf("typed Apply summary was not persisted: %#v", outputs)
	}
	workload, ok := workloads[0].(map[string]interface{})
	if !ok || resultString(apply, "resultHash") != "sha256:apply" || resultString(workload, "workloadRef") != "photos" || workload["api_token"] != nil {
		t.Fatalf("typed Apply summary was not sanitized: %#v", apply)
	}
	proof := runtimeActionProof("stackkit_rollout", result, "applied")
	if len(mapFromInterface(proof["observation"])) == 0 {
		t.Fatalf("runtime action proof dropped the observation: %#v", proof)
	}
	if proof["observations"] == nil || len(mapFromInterface(proof["apply"])) == 0 {
		t.Fatalf("runtime action proof dropped typed Apply evidence: %#v", proof)
	}
}

// Regression: the Windows client's bundled StackKits catalog ships the shared
// schema as `foundation` (StackKits renamed `base`), and rollouts from the
// installed client failed in generate_iac with "workspace source ...\base
// missing" (journey B2 run 35868302392).
func TestStackKitCLIWorkspaceLinksRenamedFoundationSchema(t *testing.T) {
	catalog := t.TempDir()
	for _, dir := range []string{DefaultBasementKitRef, "modules", "foundation", "cue.mod"} {
		if err := os.MkdirAll(filepath.Join(catalog, dir), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	workDir := t.TempDir()
	if err := ensureStackKitCLIWorkspace(workDir, catalog, DefaultBasementKitRef); err != nil {
		t.Fatalf("bundled catalog without base was refused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "foundation")); err != nil {
		t.Fatalf("shared schema is not available in the CLI workspace: %v", err)
	}
}

// Regression: the pinned CLI refuses `address plan` for a StackSpec without
// public routes, which failed every local-access rollout from the installed
// Windows client in generate_iac (journey B2 run 35869333137).
func TestCanonicalStackKitCLISkipsAddressPlanWithoutPublicRoutes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake CLI is a POSIX shell script")
	}
	workDir := t.TempDir()
	binary := filepath.Join(workDir, "stackkit")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ncase \"$*\" in *address*) exit 1 ;; esac\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	spec := filepath.Join(workDir, "stack-spec.v2.json")
	if err := os.WriteFile(spec, []byte(`{"apiVersion":"stackkit/v2alpha1","kind":"StackSpec","routes":{"dashboard":{"exposure":"local","serviceRef":"dashboard","host":"dash.home.example"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--no-log", "--chdir", workDir, "--spec", filepath.Base(spec)}
	executed, _, address, err := prepareCanonicalStackKitCLI(context.Background(), binary, workDir, spec, args, time.Minute, StackKitArtifactGenerateRequest{})
	if err != nil {
		t.Fatalf("a StackSpec without public routes was refused: %v", err)
	}
	if executed != spec || address.Prefix != "" || address.Zone != "" {
		t.Fatalf("a StackSpec without public routes was rebound: %q %#v", executed, address)
	}
}
