package stacks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	productnotifications "github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/internal/routes/trust"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/specv2"
)

type wizardRunFakeFeatures struct {
	enabled bool
	err     error
}

func (f wizardRunFakeFeatures) IsEnabled(context.Context, string, string) (bool, error) {
	return f.enabled, f.err
}

type wizardRunFakeSeeds struct {
	seeds map[string]map[string]any
}

func (f wizardRunFakeSeeds) Seed(kitSlug string) (map[string]any, error) {
	seed, ok := f.seeds[kitSlug]
	if !ok {
		return nil, fmt.Errorf("no seed for %s", kitSlug)
	}
	return seed, nil
}

type wizardRunFakeValidator struct {
	err   error
	calls int
}

func (f *wizardRunFakeValidator) ValidateSpec(context.Context, map[string]any) error {
	f.calls++
	return f.err
}

type wizardRunFakeMigrator struct {
	result    *specv2.MigrationResult
	err       error
	calls     int
	targetKit string
	legacy    map[string]any
	candidate map[string]any
}

func (f *wizardRunFakeMigrator) CompleteMigration(_ context.Context, legacy, candidate map[string]any, targetKit string) (*specv2.MigrationResult, error) {
	f.calls++
	f.targetKit = targetKit
	f.legacy = legacy
	f.candidate = candidate
	if f.result != nil || f.err != nil {
		return f.result, f.err
	}
	return &specv2.MigrationResult{Status: "completed-v2", CanonicalSpec: candidate}, nil
}

type wizardRunFakeGoalAuthor struct{}

func (wizardRunFakeGoalAuthor) AuthorGoals(_ context.Context, _, _, _, _ string, goals []string) (specv2.GoalAuthoring, error) {
	workloads := map[string]any{}
	var unmapped []string
	for _, goal := range goals {
		if goal != "photos" {
			unmapped = append(unmapped, goal)
			continue
		}
		workloads[goal] = map[string]any{
			"alternative": "immich",
			"placement": map[string]any{
				"siteRefs": []any{"home"}, "nodeRefs": []any{}, "requiresRoles": []any{},
			},
			"secretRefs": map[string]any{"database-password": "secret://workloads/photos/database-password"},
		}
	}
	return specv2.GoalAuthoring{Workloads: workloads, UnmappedGoals: unmapped}, nil
}

type wizardRunReleaseGoalAuthor struct {
	supported map[string]bool
}

func (author wizardRunReleaseGoalAuthor) AuthorGoals(_ context.Context, _, _, _, _ string, goals []string) (specv2.GoalAuthoring, error) {
	workloads := map[string]any{}
	var unmapped []string
	for _, goal := range goals {
		if !author.supported[goal] {
			unmapped = append(unmapped, goal)
			continue
		}
		workloads[goal] = map[string]any{
			"alternative": goal + "-service",
			"placement": map[string]any{
				"siteRefs": []any{"home"}, "nodeRefs": []any{}, "requiresRoles": []any{},
			},
			"secretRefs": map[string]any{"database-password": "secret://workloads/" + goal + "/database-password"},
		}
	}
	return specv2.GoalAuthoring{Workloads: workloads, UnmappedGoals: unmapped}, nil
}

type wizardRunNotificationCapture struct {
	events []productnotifications.ProductEvent
}

func (capture *wizardRunNotificationCapture) Enqueue(_ context.Context, event productnotifications.ProductEvent) error {
	capture.events = append(capture.events, event)
	return nil
}

func wizardRunTestSeed(kitSlug string) map[string]any {
	return map[string]any{
		"apiVersion": "stackkit/v2alpha1",
		"kind":       "StackSpec",
		"kit":        map[string]any{"slug": kitSlug},
		"metadata":   map[string]any{"name": "seed-template"},
		"generation": map[string]any{"outputRoot": "deploy", "strategy": "kit-template", "target": "compose"},
		"network":    map[string]any{"domain": map[string]any{"base": "example.homelab"}},
		"nodes": []any{map[string]any{
			"id": "main", "roles": []any{"controller", "worker"}, "siteRef": "home", "enabled": true,
		}},
		"sites": []any{map[string]any{"id": "home", "kind": "home"}},
	}
}

func wizardRunTestIntent(runKind string) specv2.WizardIntent {
	return specv2.WizardIntent{
		Schema:  specv2.WizardIntentSchema,
		RunKind: runKind,
		Name:    "My Homelab",
		Goals:   []string{"photos", "smart-home"},
		KitAssignment: specv2.KitAssignment{
			Mode:    specv2.KitAssignmentFound,
			KitSlug: specv2.KitSlugBasement,
		},
	}
}

func newWizardRunTestHandlers(store *controlplane.MemoryStore, validator specv2.SpecValidator) wizardRunHandlers {
	return wizardRunHandlers{
		crud: crudRouteHandlers{
			deploymentMode:  config.ModeSelfHosted,
			stackStore:      store,
			homelabStore:    store,
			jobStore:        store,
			walletStore:     store,
			serverStore:     store,
			runtimeFeatures: wizardRunFakeFeatures{enabled: true},
		},
		wizardRuns: store,
		cfg: WizardRunRouteConfig{
			DeploymentMode: config.ModeSelfHosted,
			Features:       wizardRunFakeFeatures{enabled: true},
			Seeds: wizardRunFakeSeeds{seeds: map[string]map[string]any{
				specv2.KitSlugBasement: wizardRunTestSeed(specv2.KitSlugBasement),
			}},
			Projector:      specv2.NewReleaseProjector(wizardRunFakeGoalAuthor{}),
			Validator:      validator,
			ReleaseVersion: "v0.9.9-test",
			Trust:          trust.RouteStores{Stacks: store, Workers: store, Jobs: store},
			Wallet:         store,
		},
	}
}

func wizardRunTestEvent(t *testing.T, payload any, idempotencyKey string) (*httpx.Event, *httptest.ResponseRecorder) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/wizard/runs", bytes.NewReader(body))
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{
		UserID: "auth0|user-1",
		OrgID:  "tenant-1",
	}))
	if idempotencyKey != "" {
		req.Header.Set("X-Idempotency-Key", idempotencyKey)
	}
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

func decodeWizardRunSuccess(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
	}
	return envelope.Data
}

func decodeWizardRunError(t *testing.T, rec *httptest.ResponseRecorder) (string, map[string]any) {
	t.Helper()
	var envelope struct {
		Error struct {
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error response: %v (body=%s)", err, rec.Body.String())
	}
	return envelope.Error.Message, envelope.Error.Details
}

func TestWizardRunFirstRunFoundPersistsHomelabSpecAndPairing(t *testing.T) {
	store := controlplane.NewMemoryStore()
	validator := &wizardRunFakeValidator{}
	h := newWizardRunTestHandlers(store, validator)

	intent := wizardRunTestIntent(specv2.RunKindFirstRun)
	intent.Access = specv2.AccessIntent{
		Mode: specv2.AccessModeRemotePrivate,
	}
	intent.Household = specv2.HouseholdIntent{
		Profile: specv2.HouseholdProfileShared,
		PlannedPeople: []specv2.PlannedPersonIntent{{
			ClientRef: "person-1", Name: "Alex", Email: "alex@example.com",
		}},
	}
	e, rec := wizardRunTestEvent(t, wizardRunRequest{Intent: intent}, "wizard-key-1")
	if err := h.createWizardRun(e); err != nil {
		t.Fatalf("createWizardRun: %v", err)
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	data := decodeWizardRunSuccess(t, rec)
	if data["run_kind"] != specv2.RunKindFirstRun || data["coerced"] != false {
		t.Fatalf("unexpected run kind fields: %#v", data)
	}
	if data["node_id"] != "main" || data["kit_slug"] != specv2.KitSlugBasement {
		t.Fatalf("unexpected projection fields: %#v", data)
	}
	stackID, _ := data["stack_id"].(string)
	if stackID == "" || data["job_id"] == "" || data["pairing_job_id"] == "" {
		t.Fatalf("missing ids in response: %#v", data)
	}
	if validator.calls != 1 {
		t.Fatalf("validator calls = %d, want 1", validator.calls)
	}

	ctx := context.Background()
	stack, err := store.GetStack(ctx, "tenant-1", stackID)
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	if stack.HomelabID == "" || stack.HomelabID != data["homelab_id"] {
		t.Fatalf("stack not linked to homelab: %#v vs %v", stack.HomelabID, data["homelab_id"])
	}
	spec, ok := stack.Config[stackConfigKeySpecV2].(map[string]any)
	if !ok {
		t.Fatalf("stack config missing %s: %#v", stackConfigKeySpecV2, stack.Config)
	}
	metadata, _ := spec["metadata"].(map[string]any)
	if metadata["name"] != "my-homelab" {
		t.Fatalf("projected metadata.name = %v, want my-homelab", metadata["name"])
	}
	if metadata["stackId"] != "my-homelab" || stack.StackKitInstanceID != "my-homelab" {
		t.Fatalf("StackKits instance identity = metadata %v, stored %q", metadata["stackId"], stack.StackKitInstanceID)
	}
	if metadata["fleetRef"] != stack.HomelabID {
		t.Fatalf("fleetRef = %v, want %v", metadata["fleetRef"], stack.HomelabID)
	}

	// The pairing job carries the raw registration token for the wizard UI.
	pairingJob, err := store.GetJob(ctx, "tenant-1", data["pairing_job_id"].(string))
	if err != nil {
		t.Fatalf("GetJob pairing: %v", err)
	}
	if pairingJob.Result["registration_token"] == "" || pairingJob.Result["creation_operation"] != "add-server" {
		t.Fatalf("pairing job result incomplete: %#v", pairingJob.Result)
	}

	// Unmapped goals persist on the homelab intent, never in the spec.
	homelab, err := store.GetHomelabByOwner(ctx, "tenant-1", "auth0|user-1")
	if err != nil {
		t.Fatalf("GetHomelabByOwner: %v", err)
	}
	wizard, _ := homelab.Intent["wizard"].(map[string]any)
	unmapped, _ := wizard["unmapped_goals"].([]any)
	if len(unmapped) != 1 || unmapped[0] != "smart-home" {
		t.Fatalf("unmapped goals not persisted: %#v", homelab.Intent)
	}
	persistedIntent, err := json.Marshal(wizard)
	if err != nil {
		t.Fatalf("marshal persisted wizard intent: %v", err)
	}
	if !bytes.Contains(persistedIntent, []byte(`"mode":"remote-private"`)) ||
		!bytes.Contains(persistedIntent, []byte(`"profile":"shared"`)) ||
		!bytes.Contains(persistedIntent, []byte(`"client_ref":"person-1"`)) {
		t.Fatalf("access/household intent not persisted: %s", persistedIntent)
	}
	if bytes.Contains(persistedIntent, []byte("activation")) {
		t.Fatalf("creation intent must not contain activation material: %s", persistedIntent)
	}
	if _, exists := spec["workloads"].(map[string]any)["smart-home"]; exists {
		t.Fatal("unmapped goal leaked into the spec")
	}

	// The ledger replays the identical request on the same key.
	replayEvent, replayRec := wizardRunTestEvent(t, wizardRunRequest{Intent: intent}, "wizard-key-1")
	if err := h.createWizardRun(replayEvent); err != nil {
		t.Fatalf("replay createWizardRun: %v", err)
	}
	if replayRec.Code != http.StatusAccepted {
		t.Fatalf("replay status = %d, want 202; body=%s", replayRec.Code, replayRec.Body.String())
	}
	replayData := decodeWizardRunSuccess(t, replayRec)
	if replayData["idempotent_replay"] != true || replayData["stack_id"] != stackID {
		t.Fatalf("replay did not resume the run: %#v", replayData)
	}

	// The same key with a different payload is a conflict.
	conflictIntent := wizardRunTestIntent(specv2.RunKindFirstRun)
	conflictIntent.Name = "Different Homelab"
	conflictEvent, conflictRec := wizardRunTestEvent(t, wizardRunRequest{Intent: conflictIntent}, "wizard-key-1")
	if err := h.createWizardRun(conflictEvent); err != nil {
		t.Fatalf("conflict createWizardRun: %v", err)
	}
	if conflictRec.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, want 409; body=%s", conflictRec.Code, conflictRec.Body.String())
	}
	_, conflictDetails := decodeWizardRunError(t, conflictRec)
	if conflictDetails["reason_code"] != "wizard_idempotency_conflict" {
		t.Fatalf("conflict reason_code = %#v", conflictDetails["reason_code"])
	}
	if conflictDetails["retryable"] != true {
		t.Fatalf("conflict retryable = %#v, want true", conflictDetails["retryable"])
	}
	if conflictDetails[creationStackIDField] != stackID {
		t.Fatalf("conflict stack_id = %#v, want %q", conflictDetails[creationStackIDField], stackID)
	}
	if conflictDetails[creationJobIDField] == "" {
		t.Fatalf("conflict missing job_id: %#v", conflictDetails)
	}
	if conflictDetails["completed_run_id"] == "" {
		t.Fatalf("conflict missing completed_run_id: %#v", conflictDetails)
	}
}

func TestWizardRunConflictDetailsIncludesRecoveryIds(t *testing.T) {
	stored := &controlplane.WizardRun{
		ID:           "run-completed-1",
		StackID:      "stack-abc",
		NodeID:       "node-worker-1",
		JobID:        "job-provision-1",
		PairingJobID: "job-pairing-1",
	}
	details := wizardRunConflictDetails(stored)
	if details["reason_code"] != "wizard_idempotency_conflict" || details["retryable"] != true {
		t.Fatalf("details = %#v", details)
	}
	if details["completed_run_id"] != stored.ID {
		t.Fatalf("completed_run_id = %#v", details["completed_run_id"])
	}
	if details[creationStackIDField] != stored.StackID || details["node_id"] != stored.NodeID {
		t.Fatalf("stack/node ids = %#v", details)
	}
	if details[creationJobIDField] != stored.JobID || details["pairing_job_id"] != stored.PairingJobID {
		t.Fatalf("job ids = %#v", details)
	}
}

func TestWizardRunResumeContextExcludesOwnerCredential(t *testing.T) {
	intent := wizardRunTestIntent(specv2.RunKindFirstRun)
	intent.Server = specv2.ServerIntent{Roles: []string{"worker"}, Transport: "install-command"}
	request := wizardRunRequest{
		Intent: intent,
		Owner: map[string]any{
			"owner_bootstrap_mode":     ownerBootstrapModeAuto,
			"owner_source":             ownerSourceLocal,
			"owner_email":              "owner@example.com",
			"owner_username":           "owner",
			"owner_display_name":       "Owner",
			"recovery_passphrase_hash": "$argon2id$must-not-enter-ledger-result",
		},
	}
	result := map[string]any{}
	addWizardResumeContext(result, request, specv2.RunKindFirstRun)
	resume := result["resume_context"].(map[string]any)
	if resume["server_provisioning_mode"] != "install-command" || resume["expected_device_name"] != "My Homelab-worker" {
		t.Fatalf("resume routing context = %#v", resume)
	}
	owner := resume["owner_seed_summary"].(map[string]any)
	if resume["owner_seed_expected"] != true || owner["email"] != "owner@example.com" || owner["source"] != ownerSourceLocal {
		t.Fatalf("resume owner summary = %#v", resume)
	}
	encoded, err := json.Marshal(resume)
	if err != nil {
		t.Fatalf("marshal resume context: %v", err)
	}
	if strings.Contains(string(encoded), "must-not-enter-ledger-result") {
		t.Fatal("resume context exposed the recovery credential")
	}
}

func TestWizardRunManagedAdmissionDenialStopsBeforeDispatch(t *testing.T) {
	store := controlplane.NewMemoryStore()
	manager := &crudManagedLeaseManager{preflightErr: providercontrol.ProviderCreateBlockedError{
		ProviderID: "centron", ReasonCode: providercontrol.ProviderCreateReasonKillSwitchDisabled,
	}}
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})
	h.crud.managedLeases = manager
	h.crud.deploymentMode = config.ModeSaaS

	intent := wizardRunTestIntent(specv2.RunKindFirstRun)
	intent.Server.Transport = specv2.TransportKombifyCloud
	e, rec := wizardRunTestEvent(t, wizardRunRequest{
		Intent:  intent,
		Managed: &wizardRunManagedParams{ProviderID: "centron"},
	}, "wizard-managed-blocked")
	if err := h.createWizardRun(e); err != nil {
		t.Fatalf("createWizardRun: %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	_, details := decodeWizardRunError(t, rec)
	if details["reason_code"] != providercontrol.ProviderCreateReasonKillSwitchDisabled || details["admission_phase"] != "preflight" {
		t.Fatalf("details = %#v, want provider-control preflight denial", details)
	}

	stacks, err := store.ListStacksByTenant(t.Context(), "tenant-1")
	if err != nil || len(stacks) != 1 || stacks[0].Status != "failed" {
		t.Fatalf("stacks = %#v, err=%v; want one failed audit row", stacks, err)
	}
	queued, err := store.ListJobsByStack(t.Context(), "tenant-1", stacks[0].ID, 10)
	if err != nil {
		t.Fatalf("ListJobsByStack: %v", err)
	}
	servers, err := store.ListServerRuntimesByTenant(t.Context(), "tenant-1", stacks[0].ID)
	if err != nil {
		t.Fatalf("ListServerRuntimesByTenant: %v", err)
	}
	if len(queued) != 0 || len(servers) != 0 || len(manager.requests) != 1 {
		t.Fatalf("jobs=%d servers=%d admission_calls=%d; want no dispatch/server and one preflight", len(queued), len(servers), len(manager.requests))
	}
}

func TestWizardRunSecondFirstRunIsCoercedToExpansion(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})

	first, firstRec := wizardRunTestEvent(t, wizardRunRequest{Intent: wizardRunTestIntent(specv2.RunKindFirstRun)}, "")
	if err := h.createWizardRun(first); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if firstRec.Code != http.StatusAccepted {
		t.Fatalf("first run status = %d; body=%s", firstRec.Code, firstRec.Body.String())
	}
	firstData := decodeWizardRunSuccess(t, firstRec)

	secondIntent := wizardRunTestIntent(specv2.RunKindFirstRun)
	secondIntent.Name = "Second Server"
	second, secondRec := wizardRunTestEvent(t, wizardRunRequest{Intent: secondIntent}, "")
	if err := h.createWizardRun(second); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if secondRec.Code != http.StatusAccepted {
		t.Fatalf("second run status = %d; body=%s", secondRec.Code, secondRec.Body.String())
	}
	secondData := decodeWizardRunSuccess(t, secondRec)
	if secondData["run_kind"] != specv2.RunKindExpansion || secondData["coerced"] != true {
		t.Fatalf("second first-run must coerce to expansion: %#v", secondData)
	}
	if secondData["requested_run_kind"] != specv2.RunKindFirstRun {
		t.Fatalf("requested kind must be preserved: %#v", secondData)
	}
	if secondData["homelab_id"] != firstData["homelab_id"] {
		t.Fatalf("expansion must reuse the singleton homelab: %v vs %v", secondData["homelab_id"], firstData["homelab_id"])
	}
	if secondData["stack_id"] == firstData["stack_id"] {
		t.Fatal("expansion-found must create a second kit deployment")
	}
}

func TestWizardRunValidatorRejectionPersistsNothingAndKeepsKeyUsable(t *testing.T) {
	store := controlplane.NewMemoryStore()
	validator := &wizardRunFakeValidator{err: fmt.Errorf("kit binding rejected")}
	h := newWizardRunTestHandlers(store, validator)

	e, rec := wizardRunTestEvent(t, wizardRunRequest{Intent: wizardRunTestIntent(specv2.RunKindFirstRun)}, "wizard-key-invalid")
	if err := h.createWizardRun(e); err != nil {
		t.Fatalf("createWizardRun: %v", err)
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	_, details := decodeWizardRunError(t, rec)
	if details[detailsKeyReasonCode] != "wizard_spec_rejected" || details["validate_error"] == "" {
		t.Fatalf("unexpected denial details: %#v", details)
	}

	ctx := context.Background()
	stacks, err := store.ListStacksByTenant(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("ListStacksByTenant: %v", err)
	}
	if len(stacks) != 0 {
		t.Fatalf("rejected run must persist no stack rows, got %d", len(stacks))
	}
	if _, err := store.GetHomelabByOwner(ctx, "tenant-1", "auth0|user-1"); !errors.Is(err, controlplane.ErrNotFound) {
		t.Fatalf("rejected run must persist no homelab row, got %v", err)
	}

	// The key was not burned: a later valid run with the same key succeeds.
	validator.err = nil
	retry, retryRec := wizardRunTestEvent(t, wizardRunRequest{Intent: wizardRunTestIntent(specv2.RunKindFirstRun)}, "wizard-key-invalid")
	if err := h.createWizardRun(retry); err != nil {
		t.Fatalf("retry after rejection: %v", err)
	}
	if retryRec.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d, want 202; body=%s", retryRec.Code, retryRec.Body.String())
	}
}

func TestWizardRunFeatureFlagIsFailClosed(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})
	h.crud.runtimeFeatures = wizardRunFakeFeatures{enabled: false}

	e, rec := wizardRunTestEvent(t, wizardRunRequest{Intent: wizardRunTestIntent(specv2.RunKindFirstRun)}, "")
	if err := h.createWizardRun(e); err != nil {
		t.Fatalf("createWizardRun: %v", err)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	_, details := decodeWizardRunError(t, rec)
	if details[detailsKeyReasonCode] != "feature_not_enabled" {
		t.Fatalf("unexpected denial: %#v", details)
	}
}

func TestWizardRunRequiresControlPlaneStores(t *testing.T) {
	h := newWizardRunTestHandlers(controlplane.NewMemoryStore(), &wizardRunFakeValidator{})
	h.crud.stackStore = nil

	e, rec := wizardRunTestEvent(t, wizardRunRequest{Intent: wizardRunTestIntent(specv2.RunKindFirstRun)}, "")
	if err := h.createWizardRun(e); err != nil {
		t.Fatalf("createWizardRun: %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}

func wizardRunSeedV2Stack(t *testing.T, store *controlplane.MemoryStore, stackID, owner string, config map[string]any) {
	t.Helper()
	if _, err := store.CreateStack(context.Background(), controlplane.CreateStackRequest{
		ID:             stackID,
		TenantID:       "tenant-1",
		OwnerSubjectID: owner,
		Name:           "my-homelab",
		Mode:           "easy",
		Status:         "running",
		Config:         config,
	}); err != nil {
		t.Fatalf("seed stack: %v", err)
	}
}

func wizardRunJoinIntent(deploymentID string) specv2.WizardIntent {
	return specv2.WizardIntent{
		Schema:  specv2.WizardIntentSchema,
		RunKind: specv2.RunKindExpansion,
		Name:    "ignored-by-join",
		Server:  specv2.ServerIntent{Roles: []string{"worker"}},
		KitAssignment: specv2.KitAssignment{
			Mode:            specv2.KitAssignmentJoin,
			KitDeploymentID: deploymentID,
		},
	}
}

func TestWizardRunFoundKeepsKitWhenGoalValidationFails(t *testing.T) {
	store := controlplane.NewMemoryStore()
	validator := &wizardRunJoinGoalConflictValidator{}
	h := newWizardRunTestHandlers(store, validator)

	intent := wizardRunTestIntent(specv2.RunKindFirstRun)
	intent.Goals = []string{"photos"}
	e, rec := wizardRunTestEvent(t, wizardRunRequest{Intent: intent}, "found-goal-conflict")
	if err := h.createWizardRun(e); err != nil {
		t.Fatalf("createWizardRun: %v", err)
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	data := decodeWizardRunSuccess(t, rec)
	if data["stack_id"] == nil || data["stack_id"] == "" {
		t.Fatalf("found must still persist the kit: %#v", data)
	}
	unmapped, _ := data["unmapped_goals"].([]any)
	hasPhotos := false
	for _, goal := range unmapped {
		if goal == "photos" {
			hasPhotos = true
		}
	}
	if !hasPhotos {
		t.Fatalf("conflicting goal must stay unmapped: %#v", data)
	}

	stacks, err := store.ListStacksByTenant(t.Context(), "tenant-1")
	if err != nil {
		t.Fatalf("ListStacksByTenant: %v", err)
	}
	if len(stacks) != 1 {
		t.Fatalf("found must persist one kit, got %d", len(stacks))
	}
	spec, _ := stacks[0].Config[stackConfigKeySpecV2].(map[string]any)
	workloads, _ := spec["workloads"].(map[string]any)
	if _, hasWorkload := workloads["photos"]; hasWorkload {
		t.Fatal("conflicting goal must not persist a new workload")
	}
}

func TestWizardRunJoinKeepsNodeWhenGoalValidationFails(t *testing.T) {
	store := controlplane.NewMemoryStore()
	validator := &wizardRunJoinGoalConflictValidator{}
	h := newWizardRunTestHandlers(store, validator)

	base := wizardRunTestSeed(specv2.KitSlugBasement)
	wizardRunSeedV2Stack(t, store, "stack-join-goals", "auth0|user-1", map[string]any{
		stackConfigKeySpecV2: base,
	})

	intent := wizardRunJoinIntent("stack-join-goals")
	intent.Goals = []string{"photos"}
	e, rec := wizardRunTestEvent(t, wizardRunRequest{Intent: intent}, "join-goal-conflict")
	if err := h.createWizardRun(e); err != nil {
		t.Fatalf("createWizardRun: %v", err)
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	data := decodeWizardRunSuccess(t, rec)
	if data["node_id"] != "worker-1" {
		t.Fatalf("join must still add the node: %#v", data)
	}
	unmapped, _ := data["unmapped_goals"].([]any)
	hasPhotos := false
	for _, goal := range unmapped {
		if goal == "photos" {
			hasPhotos = true
		}
	}
	if !hasPhotos {
		t.Fatalf("conflicting goal must stay unmapped: %#v", data)
	}

	stack, err := store.GetStack(t.Context(), "tenant-1", "stack-join-goals")
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	spec, _ := stack.Config[stackConfigKeySpecV2].(map[string]any)
	workloads, _ := spec["workloads"].(map[string]any)
	if _, hasWorkload := workloads["photos"]; hasWorkload {
		t.Fatal("conflicting goal must not persist a new workload")
	}
	nodes, _ := spec["nodes"].([]any)
	if len(nodes) != 2 {
		t.Fatalf("joined spec must carry the new node, got %d nodes", len(nodes))
	}
	if validator.calls < 2 {
		t.Fatalf("must retry validate without goals, calls=%d", validator.calls)
	}
}

type wizardRunJoinGoalConflictValidator struct {
	calls int
}

func (v *wizardRunJoinGoalConflictValidator) ValidateSpec(_ context.Context, spec map[string]any) error {
	v.calls++
	workloads, _ := spec["workloads"].(map[string]any)
	if _, ok := workloads["photos"]; ok {
		return fmt.Errorf("service conflict: photos already placed")
	}
	return nil
}

func TestWizardRunJoinDropsUseCasesOnCanonicalV2(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})

	base := wizardRunTestSeed(specv2.KitSlugBasement)
	base["useCases"] = []any{"photos"}
	wizardRunSeedV2Stack(t, store, "stack-usecases", "auth0|user-1", map[string]any{
		stackConfigKeySpecV2: base,
	})

	e, rec := wizardRunTestEvent(t, wizardRunRequest{Intent: wizardRunJoinIntent("stack-usecases")}, "join-drop-usecases")
	if err := h.createWizardRun(e); err != nil {
		t.Fatalf("createWizardRun: %v", err)
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}

	stack, err := store.GetStack(t.Context(), "tenant-1", "stack-usecases")
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	spec, _ := stack.Config[stackConfigKeySpecV2].(map[string]any)
	if _, hasUseCases := spec["useCases"]; hasUseCases {
		t.Fatalf("persisted spec must not carry useCases: %#v", spec["useCases"])
	}
	nodes, _ := spec["nodes"].([]any)
	if len(nodes) != 2 {
		t.Fatalf("join must still add the node, got %d nodes", len(nodes))
	}
}

func TestWizardRunJoinAppendsNodeAndMintsPairing(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})

	base := wizardRunTestSeed(specv2.KitSlugBasement)
	base["metadata"] = map[string]any{"name": "my-homelab"}
	wizardRunSeedV2Stack(t, store, "stack-v2", "auth0|user-1", map[string]any{
		"user_config":        map[string]any{"name": "my-homelab"},
		stackConfigKeySpecV2: base,
	})

	e, rec := wizardRunTestEvent(t, wizardRunRequest{Intent: wizardRunJoinIntent("stack-v2")}, "")
	if err := h.createWizardRun(e); err != nil {
		t.Fatalf("createWizardRun join: %v", err)
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	data := decodeWizardRunSuccess(t, rec)
	if data["node_id"] != "worker-1" || data["run_kind"] != specv2.RunKindExpansion {
		t.Fatalf("unexpected join response: %#v", data)
	}
	if data["state"] != "awaiting_pairing" || data["pairing_job_id"] == "" {
		t.Fatalf("join must hand off to pairing: %#v", data)
	}

	ctx := context.Background()
	stack, err := store.GetStack(ctx, "tenant-1", "stack-v2")
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	spec, _ := stack.Config[stackConfigKeySpecV2].(map[string]any)
	nodes, _ := spec["nodes"].([]any)
	if len(nodes) != 2 {
		t.Fatalf("joined spec must carry 2 nodes, got %d", len(nodes))
	}
	metadata, _ := spec["metadata"].(map[string]any)
	if metadata["name"] != "my-homelab" {
		t.Fatalf("join must not rename the deployment: %v", metadata["name"])
	}
	if stack.HomelabID == "" {
		t.Fatal("join must heal the homelab link on legacy stacks")
	}
}

func TestWizardRunJoinPersistsCanonicalServerBaselineForResume(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})
	h.cfg.Wallet = store
	base := wizardRunTestSeed(specv2.KitSlugBasement)
	wizardRunSeedV2Stack(t, store, "stack-v2", "auth0|user-1", map[string]any{
		stackConfigKeySpecV2: base,
	})
	for _, server := range []controlplane.ServerRuntime{
		{ID: "server-existing", TenantID: "tenant-1", StackID: "stack-v2", OwnerSubjectID: "auth0|user-1"},
		{ID: "server-other", TenantID: "tenant-1", StackID: "stack-other", OwnerSubjectID: "auth0|user-1"},
	} {
		if _, err := store.UpsertServerRuntime(t.Context(), server); err != nil {
			t.Fatalf("seed canonical server: %v", err)
		}
	}

	intent := wizardRunJoinIntent("stack-v2")
	intent.Server.Transport = "connect-remote"
	remotePort := 2222
	if _, err := store.UpsertWalletItem(t.Context(), controlplane.WalletItem{
		ID: "wallet-remote-join", TenantID: "tenant-1", ItemType: "ssh_key",
		Metadata: map[string]any{
			"name": "homelab-key", "kind": "ssh_key", "owner_id": "auth0|user-1",
			"secret":     "PRIVATE-KEY",
			"has_secret": true,
		},
	}); err != nil {
		t.Fatalf("UpsertWalletItem: %v", err)
	}
	e, rec := wizardRunTestEvent(t, wizardRunRequest{
		Intent: intent,
		Remote: &wizardRunRemoteParams{
			Host: "node.example.test", Port: &remotePort, User: "ubuntu",
			AuthMethod: "ssh-key", SSHKeyLabel: "homelab-key",
		},
	}, "join-cross-device")
	if err := h.createWizardRun(e); err != nil {
		t.Fatalf("createWizardRun join: %v", err)
	}
	created := decodeWizardRunSuccess(t, rec)
	createdBaseline, _ := created[wizardRunExistingServerIDs].([]any)
	if len(createdBaseline) != 1 || createdBaseline[0] != "server-existing" {
		t.Fatalf("create baseline = %#v", createdBaseline)
	}

	activeEvent, activeRec := wizardRunActiveEvent(t)
	if err := h.getActiveWizardRun(activeEvent); err != nil {
		t.Fatalf("getActiveWizardRun: %v", err)
	}
	active := decodeWizardRunSuccess(t, activeRec)["run"].(map[string]any)
	result := active["result"].(map[string]any)
	resumedBaseline, _ := result[wizardRunExistingServerIDs].([]any)
	if len(resumedBaseline) != 1 || resumedBaseline[0] != "server-existing" {
		t.Fatalf("resumed baseline = %#v", resumedBaseline)
	}
	resume := result["resume_context"].(map[string]any)
	if resume["server_provisioning_mode"] != "connect-remote" || resume["remote_server_host"] != "node.example.test" || resume["remote_server_port"] != float64(2222) {
		t.Fatalf("resumed remote context = %#v", resume)
	}
	if got := stringFromAny(created[wizardRunPlannedServerID]); got == "" {
		t.Fatalf("join must reserve planned_server_id: %#v", created)
	}

	plannedID := runtimeidentity.StackServerID("stack-v2", created["node_id"].(string))
	planned, err := store.GetServerRuntime(t.Context(), "tenant-1", plannedID)
	if err != nil {
		t.Fatalf("GetServerRuntime planned join node: %v", err)
	}
	if planned.LifecycleState != "planned" || planned.WorkerID != "" {
		t.Fatalf("join must reserve a planned server before pairing: %#v", planned)
	}
}

type wizardRunStackConfigConflictStore struct {
	controlplane.StackStore
}

func (store wizardRunStackConfigConflictStore) CompareAndSwapStackConfig(context.Context, controlplane.StackConfigCAS) (*controlplane.Stack, error) {
	return nil, controlplane.ErrConflict
}

func TestWizardRunJoinRejectsConcurrentStackConfigChangeBeforePairing(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})
	h.crud.stackStore = wizardRunStackConfigConflictStore{StackStore: store}
	base := wizardRunTestSeed(specv2.KitSlugBasement)
	base["metadata"] = map[string]any{"name": "my-homelab"}
	wizardRunSeedV2Stack(t, store, "stack-cas", "auth0|user-1", map[string]any{
		stackConfigKeySpecV2: base,
	})

	e, rec := wizardRunTestEvent(t, wizardRunRequest{Intent: wizardRunJoinIntent("stack-cas")}, "join-cas")
	if err := h.createWizardRun(e); err != nil {
		t.Fatalf("createWizardRun: %v", err)
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	_, details := decodeWizardRunError(t, rec)
	if details[detailsKeyReasonCode] != "wizard_join_config_changed" || details[detailsKeyRetryable] != true {
		t.Fatalf("conflict details = %#v", details)
	}
	persisted, err := store.GetStack(t.Context(), "tenant-1", "stack-cas")
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	spec, _ := persisted.Config[stackConfigKeySpecV2].(map[string]any)
	nodes, _ := spec["nodes"].([]any)
	if len(nodes) != 1 {
		t.Fatalf("stale join changed the deployment: %#v", nodes)
	}
	jobs, err := store.ListJobsByStack(t.Context(), "tenant-1", "stack-cas", 10)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("pairing crossed the CAS conflict: jobs=%#v err=%v", jobs, err)
	}
}

func TestWizardRunExpansionReconcilesStoredGoalsAgainstPinnedRelease(t *testing.T) {
	store := controlplane.NewMemoryStore()
	homelabID := deterministicHomelabID("tenant-1", "auth0|user-1")
	oldIntent := wizardRunTestIntent(specv2.RunKindFirstRun)
	oldIntent.Goals = append(oldIntent.Goals, "music")
	oldProjection, err := specv2.NewReleaseProjector(wizardRunFakeGoalAuthor{}).Project(
		t.Context(), wizardRunTestSeed(specv2.KitSlugBasement), oldIntent, homelabID,
	)
	if err != nil {
		t.Fatalf("project old release: %v", err)
	}
	if _, err := store.CreateHomelab(t.Context(), controlplane.CreateHomelabRequest{
		ID: homelabID, TenantID: "tenant-1", OwnerSubjectID: "auth0|user-1", Name: "My Homelab",
		Intent: map[string]any{"wizard": map[string]any{
			"goals":          []string{"photos", "smart-home", "music"},
			"unmapped_goals": []string{"smart-home", "music"},
		}},
	}); err != nil {
		t.Fatalf("seed homelab intent: %v", err)
	}
	wizardRunSeedV2Stack(t, store, "stack-reconcile", "auth0|user-1", map[string]any{
		stackConfigKeySpecV2: oldProjection.Spec,
	})

	notifications := &wizardRunNotificationCapture{}
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})
	h.cfg.Projector = specv2.NewReleaseProjector(wizardRunReleaseGoalAuthor{supported: map[string]bool{
		"photos": true, "smart-home": true,
	}})
	h.cfg.ReleaseVersion = "v0.10.0-test"
	h.cfg.NotificationOutbox = notifications
	h.cfg.Trust.Workers = failingWorkerStore{WorkerStore: store}

	e, rec := wizardRunTestEvent(t, wizardRunRequest{Intent: wizardRunJoinIntent("stack-reconcile")}, "reconcile-goals")
	if err := h.createWizardRun(e); err != nil {
		t.Fatalf("reconcile stored goals: %v", err)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("first status = %d, want 500 pairing failure; body=%s", rec.Code, rec.Body.String())
	}
	stack, err := store.GetStack(t.Context(), "tenant-1", "stack-reconcile")
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	spec, _ := stack.Config[stackConfigKeySpecV2].(map[string]any)
	workloads, _ := spec["workloads"].(map[string]any)
	if workloads["photos"] == nil || workloads["smart-home"] == nil {
		t.Fatalf("stored goals were not projected through the new release: %#v", workloads)
	}
	homelab, err := store.GetHomelabByOwner(t.Context(), "tenant-1", "auth0|user-1")
	if err != nil {
		t.Fatalf("GetHomelabByOwner: %v", err)
	}
	wizard, _ := homelab.Intent["wizard"].(map[string]any)
	if unmapped := wizardStringValues(wizard["unmapped_goals"]); strings.Join(unmapped, ",") != "music" {
		t.Fatalf("remaining unmapped goals = %#v, want [music]", unmapped)
	}
	if goals := wizardStringValues(wizard["goals"]); strings.Join(goals, ",") != "music,photos,smart-home" {
		t.Fatalf("durable goals = %#v", goals)
	}
	if wizard["last_goal_projection_release"] != "v0.10.0-test" {
		t.Fatalf("projection release = %v", wizard["last_goal_projection_release"])
	}
	if len(notifications.events) != 1 {
		t.Fatalf("notification events = %d, want 1", len(notifications.events))
	}
	event := notifications.events[0]
	if event.Topic != wizardGoalActivationTopic || event.Channel != "in_app" || event.Payload["goal"] != "smart-home" {
		t.Fatalf("unexpected goal activation notification: %#v", event)
	}
	if event.IdempotencyKey != "techstack-wizard-goal-activated:tenant-1:"+homelabID+":v0.10.0-test:smart-home" {
		t.Fatalf("notification idempotency key = %q", event.IdempotencyKey)
	}

	// A same-key retry resumes the already-persisted node after pairing
	// recovers. It preserves the still-unmapped backlog and cannot enqueue a
	// second activation event.
	h.cfg.Trust.Workers = store
	replay, replayRec := wizardRunTestEvent(t, wizardRunRequest{Intent: wizardRunJoinIntent("stack-reconcile")}, "reconcile-goals")
	if err := h.createWizardRun(replay); err != nil || replayRec.Code != http.StatusAccepted {
		t.Fatalf("replay status=%d err=%v body=%s", replayRec.Code, err, replayRec.Body.String())
	}
	if len(notifications.events) != 1 {
		t.Fatalf("retry enqueued duplicate notification: %d", len(notifications.events))
	}
	afterRetry, err := store.GetHomelabByOwner(t.Context(), "tenant-1", "auth0|user-1")
	if err != nil {
		t.Fatalf("GetHomelabByOwner after retry: %v", err)
	}
	retryWizard, _ := afterRetry.Intent["wizard"].(map[string]any)
	if unmapped := wizardStringValues(retryWizard["unmapped_goals"]); strings.Join(unmapped, ",") != "music" {
		t.Fatalf("retry lost the remaining unmapped goal: %#v", retryWizard)
	}
}

func TestWizardRunJoinRecoversNonCanonicalSpecFromKitSeed(t *testing.T) {
	cases := []struct {
		name   string
		config map[string]any
	}{
		{
			name: "v1-shaped stack_spec_v2 with useCases",
			config: map[string]any{
				"stackkit_catalog_ref": specv2.KitSlugBasement,
				stackConfigKeySpecV2: map[string]any{
					"name":     "legacy",
					"stackkit": specv2.KitSlugBasement,
					"useCases": []any{"photos"},
					"nodes": []any{
						map[string]any{"id": "main", "roles": []any{"controller", "worker"}, "siteRef": "home"},
					},
				},
			},
		},
		{
			name: "legacy user_config only",
			config: map[string]any{
				"stackkit_catalog_ref": specv2.KitSlugBasement,
				"user_config": map[string]any{
					"apiVersion": "stackkit/v1", "kind": "StackSpec", "name": "legacy", "stackkit": specv2.KitSlugBasement,
				},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := controlplane.NewMemoryStore()
			validator := &wizardRunFakeValidator{}
			h := newWizardRunTestHandlers(store, validator)
			migrator := &wizardRunFakeMigrator{}
			h.cfg.Migrator = migrator
			wizardRunSeedV2Stack(t, store, "stack-recover", "auth0|user-1", tc.config)

			e, rec := wizardRunTestEvent(t, wizardRunRequest{Intent: wizardRunJoinIntent("stack-recover")}, "join-recover")
			if err := h.createWizardRun(e); err != nil {
				t.Fatalf("createWizardRun: %v", err)
			}
			if rec.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
			}
			data := decodeWizardRunSuccess(t, rec)
			if data["node_id"] != "worker-1" || data["pairing_job_id"] == "" {
				t.Fatalf("join must append a node and mint pairing: %#v", data)
			}
			stack, err := store.GetStack(t.Context(), "tenant-1", "stack-recover")
			if err != nil {
				t.Fatalf("GetStack: %v", err)
			}
			spec, _ := stack.Config[stackConfigKeySpecV2].(map[string]any)
			if err := specv2.RequireCanonicalV2(spec); err != nil {
				t.Fatalf("persisted join spec is not Architecture v2: %v", err)
			}
			if _, hasUseCasesOnV1 := spec["stackkit"]; hasUseCasesOnV1 {
				t.Fatalf("persisted spec kept v1 stackkit: %#v", spec)
			}
			if migrator.calls != 0 || validator.calls == 0 {
				t.Fatalf("recovery must seed+validate, not migrate: migrator=%d validator=%d", migrator.calls, validator.calls)
			}
			if stack.Config[stackConfigKeySpecV1State] != stackConfigSpecV1Archived {
				t.Fatalf("v1 authority must be archived after recovery: %#v", stack.Config[stackConfigKeySpecV1State])
			}
		})
	}
}

func TestWizardRunJoinManagedDeploymentDispatchesAfterCASAndReplaysLedger(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})

	base := wizardRunTestSeed(specv2.KitSlugCloud)
	wizardRunSeedV2Stack(t, store, "stack-managed", "auth0|user-1", map[string]any{
		"server_provisioning_mode": "kombify-cloud",
		"runtime_lane":             "monthly-runtime",
		stackConfigKeySpecV2:       base,
	})
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: "server-existing", TenantID: "tenant-1", StackID: "stack-managed", OwnerSubjectID: "auth0|user-1",
	}); err != nil {
		t.Fatalf("seed canonical server: %v", err)
	}

	var expansionCalls int
	var dispatched WizardManagedRuntimeExpansionRequest
	h.cfg.ManagedExpansion = WizardManagedRuntimeExpansionFunc(func(_ *httpx.Event, request WizardManagedRuntimeExpansionRequest) (*WizardManagedRuntimeExpansionResult, error) {
		expansionCalls++
		dispatched = request
		persisted, err := store.GetStack(t.Context(), "tenant-1", request.StackID)
		if err != nil {
			t.Fatalf("managed dispatch stack lookup: %v", err)
		}
		spec, _ := persisted.Config[stackConfigKeySpecV2].(map[string]any)
		if !specHasNode(spec, request.RuntimeSlotKey) || persisted.HomelabID == "" {
			t.Fatalf("managed dispatch ran before projection CAS and homelab persistence: %#v", persisted)
		}
		return &WizardManagedRuntimeExpansionResult{
			StackID: request.StackID, JobID: "managed-job-1",
			RuntimeSlotKey: request.RuntimeSlotKey, RuntimeSlotID: "slot-managed-1",
			LeaseID: "lease-managed-1", RuntimeServerID: "server-managed-1",
			ResourceGenerationID: "generation-managed-1", OperationID: "operation-managed-1",
			ProviderID: request.ProviderID, ProviderRegion: request.ProviderRegion,
			NodeRole: request.NodeRole, RuntimeOfferingID: request.RuntimeOfferingID,
			EnrollmentStatus: "pending", RuntimePhase: "lease_pending",
			Message: "Native managed runtime admission accepted; provisioning is pending",
		}, nil
	})

	intent := wizardRunJoinIntent("stack-managed")
	intent.Server.Transport = specv2.TransportKombifyCloud
	request := wizardRunRequest{
		Intent: intent,
		Managed: &wizardRunManagedParams{
			ProviderID: "centron", RuntimeOfferingID: "monthly-runtime-standard", ProviderRegion: "de",
		},
		Services: []string{"monitoring"},
	}

	e, rec := wizardRunTestEvent(t, request, "managed-join-1")
	if err := h.createWizardRun(e); err != nil {
		t.Fatalf("createWizardRun: %v", err)
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	created := decodeWizardRunSuccess(t, rec)
	if created[creationJobIDField] != "managed-job-1" || created[wizardRunStateField] != wizardRunStateProvisioning || created["pairing_job_id"] != nil {
		t.Fatalf("managed join response = %#v", created)
	}
	if dispatched.RuntimeSlotKey != "worker-1" || dispatched.NodeRole != "worker" || dispatched.ProviderID != "centron" || dispatched.StackKit != specv2.KitSlugCloud {
		t.Fatalf("managed expansion request = %#v", dispatched)
	}

	replay, replayRec := wizardRunTestEvent(t, request, "managed-join-1")
	if err := h.createWizardRun(replay); err != nil || replayRec.Code != http.StatusAccepted {
		t.Fatalf("replay status=%d err=%v body=%s", replayRec.Code, err, replayRec.Body.String())
	}
	if expansionCalls != 1 || decodeWizardRunSuccess(t, replayRec)[creationJobIDField] != "managed-job-1" {
		t.Fatalf("managed replay redispatched: calls=%d body=%s", expansionCalls, replayRec.Body.String())
	}

	activeEvent, activeRec := wizardRunActiveEvent(t)
	if err := h.getActiveWizardRun(activeEvent); err != nil {
		t.Fatalf("getActiveWizardRun: %v", err)
	}
	active := decodeWizardRunSuccess(t, activeRec)["run"].(map[string]any)
	result := active["result"].(map[string]any)
	baseline, _ := result[wizardRunExistingServerIDs].([]any)
	if active[creationJobIDField] != "managed-job-1" || result["operation_id"] != "operation-managed-1" || len(baseline) != 1 || baseline[0] != "server-existing" {
		t.Fatalf("managed active run lost correlations or baseline: %#v", active)
	}
}

func TestWizardRunJoinForeignStackIsRejected(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})

	base := wizardRunTestSeed(specv2.KitSlugBasement)
	wizardRunSeedV2Stack(t, store, "stack-foreign", "auth0|someone-else", map[string]any{
		stackConfigKeySpecV2: base,
	})

	e, _ := wizardRunTestEvent(t, wizardRunRequest{Intent: wizardRunJoinIntent("stack-foreign")}, "")
	err := h.createWizardRun(e)
	var apiErr *httpx.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("expected 403 APIError, got %v", err)
	}
}

func TestWizardRunUnknownFieldsAreRejected(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})

	payload := map[string]any{
		"intent":     wizardRunTestIntent(specv2.RunKindFirstRun),
		"surprising": true,
	}
	e, rec := wizardRunTestEvent(t, payload, "")
	if err := h.createWizardRun(e); err != nil {
		t.Fatalf("createWizardRun: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestWizardRunUnsupportedAnswersUseSafeStandard(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})
	intent := wizardRunTestIntent(specv2.RunKindFirstRun)
	intent.Name = ""
	intent.KitAssignment.KitSlug = "unsupported-premium-kit"
	intent.Server.Roles = []string{"dream-server"}

	e, rec := wizardRunTestEvent(t, wizardRunRequest{Intent: intent}, "safe-standard-1")
	if err := h.createWizardRun(e); err != nil {
		t.Fatalf("createWizardRun: %v", err)
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	data := decodeWizardRunSuccess(t, rec)
	if data["kit_slug"] != specv2.KitSlugBasement {
		t.Fatalf("kit_slug = %v, want safe standard %q", data["kit_slug"], specv2.KitSlugBasement)
	}
	if _, ok := data["intent_adjustments"].([]any); !ok {
		t.Fatalf("response omitted explainable intent adjustments: %#v", data)
	}
}

// failingWorkerStore makes the pairing mint fail while every other store
// operation keeps working - the partial-failure window the resume model must
// cover.
type failingWorkerStore struct {
	controlplane.WorkerStore
}

func (failingWorkerStore) UpsertPairingToken(context.Context, controlplane.PairingToken) (*controlplane.PairingToken, error) {
	return nil, fmt.Errorf("worker store unavailable")
}

func TestWizardRunFirstRunSameKeyRetryResumesAfterPairingFailure(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})
	h.cfg.Trust.Workers = failingWorkerStore{WorkerStore: store}

	first, firstRec := wizardRunTestEvent(t, wizardRunRequest{Intent: wizardRunTestIntent(specv2.RunKindFirstRun)}, "resume-key-1")
	if err := h.createWizardRun(first); err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	if firstRec.Code != http.StatusInternalServerError {
		t.Fatalf("first attempt status = %d, want 500; body=%s", firstRec.Code, firstRec.Body.String())
	}
	ctx := context.Background()
	stacksAfterFirst, err := store.ListStacksByTenant(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("ListStacksByTenant: %v", err)
	}
	if len(stacksAfterFirst) != 1 {
		t.Fatalf("first attempt must persist exactly one stack, got %d", len(stacksAfterFirst))
	}
	failedRun, err := store.GetWizardRunByKey(ctx, "tenant-1", "auth0|user-1", "resume-key-1")
	if err != nil {
		t.Fatalf("failed ledger row missing: %v", err)
	}
	if failedRun.Status != "failed" || failedRun.ErrorReason != "pairing_mint_failed" {
		t.Fatalf("unexpected failed ledger row: %#v", failedRun)
	}

	// Retry with the SAME key and a working pairing store: must resume the
	// SAME stack (no duplicate, no 409) and deliver a pairing job.
	h.cfg.Trust.Workers = store
	retry, retryRec := wizardRunTestEvent(t, wizardRunRequest{Intent: wizardRunTestIntent(specv2.RunKindFirstRun)}, "resume-key-1")
	if retryErr := h.createWizardRun(retry); retryErr != nil {
		t.Fatalf("retry: %v", retryErr)
	}
	if retryRec.Code != http.StatusAccepted {
		t.Fatalf("retry status = %d, want 202; body=%s", retryRec.Code, retryRec.Body.String())
	}
	data := decodeWizardRunSuccess(t, retryRec)
	if data["stack_id"] != stacksAfterFirst[0].ID {
		t.Fatalf("retry founded a new stack: %v vs %v", data["stack_id"], stacksAfterFirst[0].ID)
	}
	pairingJobID, _ := data["pairing_job_id"].(string)
	if data["idempotent_replay"] != true || pairingJobID == "" {
		t.Fatalf("retry must resume idempotently with a pairing job: %#v", data)
	}
	// The retry must NOT be coerced: it resumes the original first-run.
	if data["run_kind"] != specv2.RunKindFirstRun || data["coerced"] != false {
		t.Fatalf("resume must keep the original run kind: %#v", data)
	}
	stacksAfterRetry, err := store.ListStacksByTenant(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("ListStacksByTenant after retry: %v", err)
	}
	if len(stacksAfterRetry) != 1 {
		t.Fatalf("retry must not create a second stack, got %d", len(stacksAfterRetry))
	}
	completedRun, err := store.GetWizardRunByKey(ctx, "tenant-1", "auth0|user-1", "resume-key-1")
	if err != nil || completedRun.Status != "completed" {
		t.Fatalf("ledger must complete on resume: %#v err=%v", completedRun, err)
	}
}

func TestWizardRunJoinSameKeyRetryReusesPersistedNode(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})
	h.cfg.Trust.Workers = failingWorkerStore{WorkerStore: store}

	base := wizardRunTestSeed(specv2.KitSlugBasement)
	base["metadata"] = map[string]any{"name": "my-homelab"}
	wizardRunSeedV2Stack(t, store, "stack-join-resume", "auth0|user-1", map[string]any{
		stackConfigKeySpecV2: base,
	})

	first, firstRec := wizardRunTestEvent(t, wizardRunRequest{Intent: wizardRunJoinIntent("stack-join-resume")}, "join-key-1")
	if err := h.createWizardRun(first); err != nil {
		t.Fatalf("first join attempt: %v", err)
	}
	if firstRec.Code != http.StatusInternalServerError {
		t.Fatalf("first join status = %d, want 500; body=%s", firstRec.Code, firstRec.Body.String())
	}
	ctx := context.Background()
	afterFirst, err := store.GetStack(ctx, "tenant-1", "stack-join-resume")
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	firstSpec, _ := afterFirst.Config[stackConfigKeySpecV2].(map[string]any)
	firstNodes, _ := firstSpec["nodes"].([]any)
	if len(firstNodes) != 2 {
		t.Fatalf("first attempt must persist the joined node, got %d nodes", len(firstNodes))
	}

	h.cfg.Trust.Workers = store
	retry, retryRec := wizardRunTestEvent(t, wizardRunRequest{Intent: wizardRunJoinIntent("stack-join-resume")}, "join-key-1")
	if retryErr := h.createWizardRun(retry); retryErr != nil {
		t.Fatalf("join retry: %v", retryErr)
	}
	if retryRec.Code != http.StatusAccepted {
		t.Fatalf("join retry status = %d, want 202; body=%s", retryRec.Code, retryRec.Body.String())
	}
	data := decodeWizardRunSuccess(t, retryRec)
	if data["node_id"] != "worker-1" || data["idempotent_replay"] != true {
		t.Fatalf("join retry must reuse the persisted node: %#v", data)
	}
	afterRetry, err := store.GetStack(ctx, "tenant-1", "stack-join-resume")
	if err != nil {
		t.Fatalf("GetStack after retry: %v", err)
	}
	retrySpec, _ := afterRetry.Config[stackConfigKeySpecV2].(map[string]any)
	retryNodes, _ := retrySpec["nodes"].([]any)
	if len(retryNodes) != 2 {
		t.Fatalf("join retry appended a phantom node: %d nodes", len(retryNodes))
	}
}

func TestWizardRunFoundPersistsTopLevelStackKitForRollout(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})

	e, rec := wizardRunTestEvent(t, wizardRunRequest{Intent: wizardRunTestIntent(specv2.RunKindFirstRun)}, "")
	if err := h.createWizardRun(e); err != nil {
		t.Fatalf("createWizardRun: %v", err)
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	data := decodeWizardRunSuccess(t, rec)
	stack, err := store.GetStack(context.Background(), "tenant-1", data["stack_id"].(string))
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	userConfig, _ := stack.Config["user_config"].(map[string]any)
	if userConfig["stackkit"] != specv2.KitSlugBasement {
		t.Fatalf("user_config must carry the top-level stackkit for the rollout handoff and intent kit: %#v", userConfig)
	}
	if stack.Config["stackkit_catalog_ref"] != specv2.KitSlugBasement {
		t.Fatalf("runtime fields must record the kit: %v", stack.Config["stackkit_catalog_ref"])
	}
}

func TestWizardRunDuplicateNameResolvesBeforeProjection(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})

	for range 2 {
		e, rec := wizardRunTestEvent(t, wizardRunRequest{Intent: wizardRunTestIntent(specv2.RunKindFirstRun)}, "")
		if err := h.createWizardRun(e); err != nil {
			t.Fatalf("createWizardRun: %v", err)
		}
		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
		}
	}
	stacks, err := store.ListStacksByTenant(context.Background(), "tenant-1")
	if err != nil {
		t.Fatalf("ListStacksByTenant: %v", err)
	}
	if len(stacks) != 2 {
		t.Fatalf("expected 2 deployments, got %d", len(stacks))
	}
	stackNames := map[string]bool{}
	specNames := map[string]bool{}
	for _, stack := range stacks {
		spec, _ := stack.Config[stackConfigKeySpecV2].(map[string]any)
		metadata, _ := spec["metadata"].(map[string]any)
		specName, _ := metadata["name"].(string)
		if strings.TrimSpace(stack.Name) == "" || strings.TrimSpace(specName) == "" {
			t.Fatalf("duplicate-name resolution produced an empty identity: stack=%#v spec=%#v", stack.Name, specName)
		}
		if stackNames[stack.Name] || specNames[specName] {
			t.Fatalf("duplicate-name resolution reused an identity: stack=%#v spec=%#v", stack.Name, specName)
		}
		stackNames[stack.Name] = true
		specNames[specName] = true
	}
}

func TestWizardRunOversizedIdempotencyKeyIsRejected(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})

	e, rec := wizardRunTestEvent(t, wizardRunRequest{Intent: wizardRunTestIntent(specv2.RunKindFirstRun)}, strings.Repeat("k", 257))
	if err := h.createWizardRun(e); err != nil {
		t.Fatalf("createWizardRun: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestStackSpecFromRequestStripsProjectedSpec(t *testing.T) {
	body := strings.NewReader(`{"name":"x","stack_spec_v2":{"apiVersion":"stackkit/v2alpha1"}}`)
	spec, msg := stackSpecFromRequest(body)
	if msg != "" {
		t.Fatalf("unexpected error: %s", msg)
	}
	if _, exists := spec[stackConfigKeySpecV2]; exists {
		t.Fatal("request bodies must not smuggle a v2 projection past the wizard admission")
	}
}

func wizardRunActiveEvent(t *testing.T) (*httpx.Event, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/wizard/runs/active", nil)
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{
		UserID: "auth0|user-1",
		OrgID:  "tenant-1",
	}))
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

func TestGetActiveWizardRunReturnsNullWithoutRuns(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})

	e, rec := wizardRunActiveEvent(t)
	if err := h.getActiveWizardRun(e); err != nil {
		t.Fatalf("getActiveWizardRun: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	data := decodeWizardRunSuccess(t, rec)
	if run, exists := data["run"]; !exists || run != nil {
		t.Fatalf("expected run: null, got %#v", data)
	}
}

func TestGetActiveWizardRunReturnsLatestRunWithJobSnapshot(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})

	// Execute a real first-run so the ledger and the provision job exist.
	e, rec := wizardRunTestEvent(t, wizardRunRequest{Intent: wizardRunTestIntent(specv2.RunKindFirstRun)}, "active-key-1")
	if err := h.createWizardRun(e); err != nil {
		t.Fatalf("createWizardRun: %v", err)
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("run status = %d; body=%s", rec.Code, rec.Body.String())
	}
	created := decodeWizardRunSuccess(t, rec)

	activeEvent, activeRec := wizardRunActiveEvent(t)
	if err := h.getActiveWizardRun(activeEvent); err != nil {
		t.Fatalf("getActiveWizardRun: %v", err)
	}
	if activeRec.Code != http.StatusOK {
		t.Fatalf("active status = %d; body=%s", activeRec.Code, activeRec.Body.String())
	}
	data := decodeWizardRunSuccess(t, activeRec)
	run, _ := data["run"].(map[string]any)
	if run == nil {
		t.Fatalf("expected a run payload: %#v", data)
	}
	if run["status"] != "completed" || run["stack_id"] != created["stack_id"] {
		t.Fatalf("unexpected active run: %#v", run)
	}
	job, _ := run["job"].(map[string]any)
	if job == nil || job["id"] != created["job_id"] || job["state"] == "" {
		t.Fatalf("expected a live job snapshot: %#v", run)
	}
	if run["result"].(map[string]any)["pairing_job_id"] != created["pairing_job_id"] {
		t.Fatalf("result must carry the pairing job for resume: %#v", run["result"])
	}
}

func TestGetActiveWizardRunHidesRunsWhenFlagDisabled(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})

	seedEvent, seedRec := wizardRunTestEvent(t, wizardRunRequest{Intent: wizardRunTestIntent(specv2.RunKindFirstRun)}, "")
	if err := h.createWizardRun(seedEvent); err != nil {
		t.Fatalf("createWizardRun: %v", err)
	}
	if seedRec.Code != http.StatusAccepted {
		t.Fatalf("seed status = %d", seedRec.Code)
	}

	h.crud.runtimeFeatures = wizardRunFakeFeatures{enabled: false}
	e, rec := wizardRunActiveEvent(t)
	if err := h.getActiveWizardRun(e); err != nil {
		t.Fatalf("getActiveWizardRun: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	data := decodeWizardRunSuccess(t, rec)
	if run, exists := data["run"]; !exists || run != nil {
		t.Fatalf("flag-off must read as no active run, got %#v", data)
	}
}

func TestGetActiveWizardRunScopesToOwner(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})

	if _, err := store.UpsertWizardRun(context.Background(), controlplane.WizardRun{
		ID: "run-other", TenantID: "tenant-1", OwnerSubjectID: "auth0|someone-else",
		RequestSHA256: "h", RunKind: "first-run", RequestedRunKind: "first-run", Status: "completed",
	}); err != nil {
		t.Fatalf("seed foreign run: %v", err)
	}

	e, rec := wizardRunActiveEvent(t)
	if err := h.getActiveWizardRun(e); err != nil {
		t.Fatalf("getActiveWizardRun: %v", err)
	}
	data := decodeWizardRunSuccess(t, rec)
	if run, exists := data["run"]; !exists || run != nil {
		t.Fatalf("another owner's run must not leak, got %#v", data)
	}
}
