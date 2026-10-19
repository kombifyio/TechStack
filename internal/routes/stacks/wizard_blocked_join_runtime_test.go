package stacks

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/specv2"
)

type wizardJoinIntentFault struct {
	*controlplane.PostgresStore
	once atomic.Bool
}

func (s *wizardJoinIntentFault) MutateHomelabIntent(ctx context.Context, tenant, home string, mutate func(map[string]any) error) (*controlplane.Homelab, error) {
	return s.PostgresStore.MutateHomelabIntent(ctx, tenant, home, func(doc map[string]any) error {
		if err := mutate(doc); err != nil {
			return err
		}
		if s.once.CompareAndSwap(true, false) {
			doc["e85_join_controlled_rollback"] = true
			return errors.New("E85 controlled blocked-join canonical callback failure")
		}
		return nil
	})
}

func TestWizardBlockedJoinSameKeyRecovery(t *testing.T) {
	ctx, db, store, h := wizardRevisionRuntime(t)
	tenant, owner := os.Getenv("E85_TENANT"), os.Getenv("E85_OWNER")
	home, err := store.GetHomelabByOwner(ctx, tenant, owner)
	if err != nil {
		t.Fatal("canonical Homelab unavailable")
	}
	baselineWizard := home.Intent["wizard"].(map[string]any)
	baselineActive, err := store.ListStacksByTenant(ctx, tenant)
	if err != nil {
		t.Fatal("active stack baseline unavailable")
	}
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	keys := []string{"e85-join-repeat-" + stamp, "e85-join-fault-" + stamp}
	// Even a matching deterministic ID is a pre-existing target, not a Stack
	// created by this join. The fault key below has a different deterministic ID.
	targetID := deterministicStackID(tenant, owner, keys[0])
	target, err := store.CreateStack(ctx, controlplane.CreateStackRequest{ID: targetID, TenantID: tenant,
		OwnerSubjectID: owner, HomelabID: home.ID, Name: "e85-blocked-join-" + stamp, Status: "draft",
		Description: "E85-R1-NEW-01 target-only fixture; no runtime", Config: map[string]any{"qualification": "E85-R1-NEW-01"}})
	if err != nil {
		t.Fatal("labeled owned draft target could not be created")
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		ownRunIDs := map[string]bool{}
		for _, key := range keys {
			run, readErr := store.GetWizardRunByKey(cleanupCtx, tenant, owner, key)
			if errors.Is(readErr, controlplane.ErrNotFound) {
				continue
			}
			if readErr != nil {
				t.Error("qualification ledger cleanup read failed")
				continue
			}
			ownRunIDs[run.ID] = true
			if err := store.DismissWizardRun(cleanupCtx, tenant, owner, run.ID, time.Now()); err != nil {
				t.Error("qualification notice dismissal failed")
			}
		}
		if err := store.SoftDeleteStack(cleanupCtx, tenant, targetID); err != nil {
			t.Error("owned draft target cleanup failed")
		}
		_, err := store.MutateHomelabIntent(cleanupCtx, tenant, home.ID, func(doc map[string]any) error {
			wizard := doc["wizard"].(map[string]any)
			last, _ := wizard["last_run_id"].(string)
			if ownRunIDs[last] {
				for _, field := range []string{"last_run_id", "last_run_kind", "last_goal_projection_release"} {
					if value, exists := baselineWizard[field]; exists {
						wizard[field] = value
					} else {
						delete(wizard, field)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Error("owned qualification metadata cleanup failed")
		}
		active, err := store.ListStacksByTenant(cleanupCtx, tenant)
		archived, archiveErr := store.GetStackIncludingDeleted(cleanupCtx, tenant, targetID)
		if err != nil || !reflect.DeepEqual(active, baselineActive) || archiveErr != nil || archived.DeletedAt == nil {
			t.Error("draft target cleanup did not restore active baseline")
		}
		t.Logf("E85_JOIN_CLEANUP target=%s soft_deleted=true own_notices_dismissed=true canonical_metadata_restored_if_still_owned=true active_baseline_restored=true", targetID)
	})
	before := wizardRevisionResourceCounts(t, ctx, db)
	request := wizardRevisionRequest()
	request.Intent.RunKind = specv2.RunKindExpansion
	request.Intent.KitAssignment = specv2.KitAssignment{Mode: specv2.KitAssignmentJoin, KitDeploymentID: targetID}
	saved := func(key string, response *httptest.ResponseRecorder) *controlplane.WizardRun {
		t.Helper()
		if response.Code != 409 || !strings.Contains(response.Body.String(), "wizard_desired_projection_blocked") {
			t.Fatalf("join did not return structured saved-blocked409: status=%d", response.Code)
		}
		run, err := store.GetWizardRunByKey(ctx, tenant, owner, key)
		if err != nil || run.ErrorReason != controlplane.WizardDesiredProjectionBlocked || run.StackID != targetID || run.NodeID != "" || run.JobID != "" || run.PairingJobID != "" {
			t.Fatal("join target, reserved identity or empty runtime state changed")
		}
		return run
	}
	first := saved(keys[0], wizardRevisionCall(ctx, h, keys[0], request))
	if retry := saved(keys[0], wizardRevisionCall(ctx, h, keys[0], request)); retry.ID != first.ID {
		t.Fatal("blocked join repeat changed identity")
	}
	changed := request
	changed.Intent.KitAssignment.KitDeploymentID = "different-target"
	if response := wizardRevisionCall(ctx, h, keys[0], changed); response.Code != 409 {
		t.Fatal("reserved join accepted a different target")
	}
	beforeFault, err := store.GetHomelabByOwner(ctx, tenant, owner)
	if err != nil {
		t.Fatal("pre-fault canonical intent unavailable")
	}
	fault := &wizardJoinIntentFault{PostgresStore: store}
	fault.once.Store(true)
	h.crud.homelabStore = fault
	response := wizardRevisionCall(ctx, h, keys[1], request)
	if response.Code != 503 || !strings.Contains(response.Body.String(), "wizard_intent_projection_failed") {
		t.Fatal("controlled canonical callback failure was not reported")
	}
	failed, err := store.GetWizardRunByKey(ctx, tenant, owner, keys[1])
	if err != nil || failed.ErrorReason != "intent_projection_failed" || failed.StackID != targetID || failed.NodeID != "" || failed.JobID != "" || failed.PairingJobID != "" {
		t.Fatal("failed join ledger lost target or acquired runtime state")
	}
	afterFault, err := store.GetHomelabByOwner(ctx, tenant, owner)
	if err != nil || !reflect.DeepEqual(beforeFault.Intent, afterFault.Intent) {
		t.Fatal("controlled callback did not roll back")
	}
	recovered := saved(keys[1], wizardRevisionCall(ctx, h, keys[1], request))
	if recovered.ID != failed.ID || targetID == deterministicStackID(tenant, owner, keys[1]) {
		t.Fatal("join recovery identity or fault scheduling fixture differs")
	}
	if retry := saved(keys[1], wizardRevisionCall(ctx, h, keys[1], request)); retry.ID != recovered.ID {
		t.Fatal("recovered join repeat changed identity")
	}
	unchangedTarget, err := store.GetStack(ctx, tenant, targetID)
	if err != nil || !reflect.DeepEqual(target, unchangedTarget) || !reflect.DeepEqual(before, wizardRevisionResourceCounts(t, ctx, db)) {
		t.Fatal("blocked joins changed target or created runtime resources")
	}
	t.Logf("E85_JOIN target=%s owner_tenant_target_validated=true matching_and_different_deterministic_ids=true blocked_repeat_structured409=true callback_rollback_503_then_same_key_recovery409=true failed_run=%s recovered_run=%s target_unchanged=true no_additional_stacks_servers_jobs_workers_pairing=true", targetID, failed.ID, recovered.ID)
}

func TestWizardBlockedJoinRuntimeReferencesStayGuarded(t *testing.T) {
	for _, state := range []wizardRunState{
		{resumeStack: &controlplane.Stack{ID: "attempt-created-stack"}},
		{priorFailed: &controlplane.WizardRun{NodeID: "existing-node"}},
		{priorFailed: &controlplane.WizardRun{JobID: "existing-job"}},
		{priorFailed: &controlplane.WizardRun{PairingJobID: "existing-pairing"}},
	} {
		state.request.Intent.KitAssignment.Mode = specv2.KitAssignmentJoin
		out := httptest.NewRecorder()
		event := &httpx.Event{Request: httptest.NewRequest(http.MethodPost, "/api/v1/wizard/runs", nil), Response: out}
		if err := (wizardRunHandlers{}).persistBlockedWizardRun(event, &state); err != nil || out.Code != 409 || strings.Contains(out.Body.String(), "wizard_desired_projection_blocked") {
			t.Fatal("real runtime recovery guard was weakened")
		}
	}
}
