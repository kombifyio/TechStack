package stacks

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/features"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/specv2"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// These two named handler regressions require the existing managed application
// runtime, never an embedded database, invented identity or provider executor.
func wizardRevisionRuntime(t *testing.T) (context.Context, *sql.DB, *controlplane.PostgresStore, wizardRunHandlers) {
	t.Helper()
	if os.Getenv("E85_WIZARD_QUALIFICATION") != "1" {
		t.Skip("explicit managed-PG application-role qualification required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	owner, tenant := os.Getenv("E85_OWNER"), os.Getenv("E85_TENANT")
	if owner != "auth0|test-fresh-2-ced788f5-2229-4413-93e3-a2deb8bd3ec2" || tenant != "usr:"+owner {
		t.Fatal("canonical normal-login fixture differs")
	}
	ctx = identity.NewContext(ctx, &identity.Identity{UserID: owner, OrgID: tenant})
	db, err := sql.Open("pgx", os.Getenv("E85_APPLICATION_DSN"))
	if err != nil {
		t.Fatal("application database open failed")
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(3)
	var login, session, database, system string
	var admin, keys bool
	err = db.QueryRowContext(ctx, `SELECT current_user,session_user,r.rolsuper OR r.rolcreaterole OR r.rolcreatedb OR r.rolbypassrls,
		has_table_privilege(current_user,'public.advanced_issuer_keys','SELECT') OR has_any_column_privilege(current_user,'public.advanced_issuer_keys','SELECT')
		FROM pg_roles r WHERE r.rolname=current_user`).Scan(&login, &session, &admin, &keys)
	if err != nil || login != "techstack_application" || session != login || admin || keys {
		t.Fatal("application role purpose refused")
	}
	err = db.QueryRowContext(ctx, `SELECT database_name,system_identifier FROM provider_control_runtime_authority()`).Scan(&database, &system)
	if err != nil || database+"@"+system != strings.TrimSpace(os.Getenv("E85_EXPECTED_DATABASE_IDENTITY")) {
		t.Fatal("database/system identity differs")
	}
	store := controlplane.NewPostgresStore(db)
	home, err := store.GetHomelabByOwner(ctx, tenant, owner)
	if err != nil || home.ID != "hl-4a8d01f5bd0318862362d8ac8d115345" {
		t.Fatal("canonical Homelab fixture differs")
	}
	flags, err := features.NewService(features.NewPostgresStore(db), features.ServiceConfig{})
	if err != nil {
		t.Fatal("actual feature service unavailable")
	}
	catalog, err := stackkitrelease.ResolveUseCaseCatalog()
	if err != nil {
		t.Fatal("pinned catalog unavailable")
	}
	h := wizardRunHandlers{crud: crudRouteHandlers{deploymentMode: config.ModeSaaS, stackStore: store, homelabStore: store,
		jobStore: store, serverStore: store, runtimeFeatures: flags}, wizardRuns: store,
		cfg: WizardRunRouteConfig{DeploymentMode: config.ModeSaaS, Features: flags, ReleaseVersion: catalog.Release.Version}}
	return ctx, db, store, h
}

func wizardRevisionRequest() wizardRunRequest {
	return wizardRunRequest{Intent: specv2.WizardIntent{Schema: specv2.WizardIntentSchema, RunKind: specv2.RunKindFirstRun,
		Name: "D36 acceptance", Goals: []string{"ai"},
		Server:              specv2.ServerIntent{Roles: []string{"controller", "worker"}, Transport: specv2.TransportInstallCommand},
		KitAssignment:       specv2.KitAssignment{Mode: specv2.KitAssignmentFound, KitSlug: specv2.KitSlugBasement},
		UseCaseSettings:     map[string]map[string]any{"ai": {"model-pack-general": false}},
		UseCaseCapabilities: map[string]map[string]string{"ai": {"inference": "off", "observability": "off"}},
		UseCaseSetupAnswers: map[string]map[string][]string{"ai": {"tool-inference": {"llama-cpp"}, "extras": {}}}}}
}

func wizardRevisionCall(ctx context.Context, h wizardRunHandlers, key string, request wizardRunRequest) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(request)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/wizard/runs", bytes.NewReader(raw)).WithContext(ctx)
	r.Header.Set("X-Idempotency-Key", key)
	response := httptest.NewRecorder()
	if err := h.createWizardRun(&httpx.Event{Request: r, Response: response}); err != nil {
		response.Code = 500
	}
	return response
}

func wizardRevisionResourceCounts(t *testing.T, ctx context.Context, db *sql.DB) []int {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal("resource read transaction unavailable")
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT set_config('app.tenant_id',$1,true)`, os.Getenv("E85_TENANT")); err != nil {
		t.Fatal("tenant scope unavailable")
	}
	counts := make([]int, 5)
	for i, table := range []string{"stacks", "servers", "jobs", "workers", "pairing_tokens"} {
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE tenant_id=$1", os.Getenv("E85_TENANT")).Scan(&counts[i]); err != nil {
			t.Fatal("resource count unavailable: " + table)
		}
	}
	return counts
}

func TestWizardSavedBlockedDisjointExpansion(t *testing.T) {
	ctx, db, store, h := wizardRevisionRuntime(t)
	tenant, owner := os.Getenv("E85_TENANT"), os.Getenv("E85_OWNER")
	before := wizardRevisionResourceCounts(t, ctx, db)
	saved := wizardRevisionRequest()
	if response := wizardRevisionCall(ctx, h, "e85-r1-saved-recorded", saved); response.Code != 409 || !strings.Contains(response.Body.String(), "wizard_desired_projection_blocked") {
		t.Fatal("recorded fixture was not saved blocked")
	}
	home, err := store.GetHomelabByOwner(ctx, tenant, owner)
	if err != nil {
		t.Fatal("canonical intent unavailable")
	}
	wizard := home.Intent["wizard"].(map[string]any)
	baseline, err := wizardIntentFromDocument(wizard)
	if err != nil {
		t.Fatal("canonical decisions invalid")
	}
	// Cleanup runs on failure as well and precedes closing the admitted Store.
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := store.MutateHomelabIntent(cleanupCtx, tenant, home.ID, func(doc map[string]any) error {
			w := doc["wizard"].(map[string]any)
			if !slices.Contains(baseline.Goals, "files") {
				for _, field := range []string{"goals", "unmapped_goals"} {
					w[field] = slices.DeleteFunc(wizardStringValues(w[field]), func(goal string) bool { return goal == "files" })
				}
			}
			settings := w["use_case_settings"].(map[string]any)["ai"].(map[string]any)
			settings["model-pack-general"] = baseline.UseCaseSettings["ai"]["model-pack-general"]
			return nil
		})
		if err != nil {
			t.Error("own qualification goal cleanup failed")
		}
	})
	// A new Node asks only for files; it never resends the stored AI maps.
	expansion := saved
	expansion.Intent.RunKind, expansion.Intent.Goals = specv2.RunKindExpansion, []string{"files"}
	expansion.Intent.UseCaseSettings, expansion.Intent.UseCaseCapabilities, expansion.Intent.UseCaseSetupAnswers = nil, nil, nil
	// Pause after inheritance/admission, then make an actual newer AI decision.
	// Persistence must write the incoming delta, not this inherited snapshot.
	entered, release := make(chan struct{}), make(chan struct{})
	h.wizardRuns = &wizardReservationPause{PostgresStore: store, hash: wizardRunRequestHash(expansion), entered: entered, release: release}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- wizardRevisionCall(ctx, h, "e85-r1-disjoint-expansion", expansion) }()
	select {
	case <-entered:
	case <-ctx.Done():
		close(release)
		<-done
		t.Fatal("disjoint expansion did not reach its pre-effect boundary")
	case response := <-done:
		close(release)
		t.Fatalf("expansion returned %d before interleaving", response.Code)
	}
	newer := wizardRevisionRequest()
	newer.Intent.UseCaseSettings["ai"]["model-pack-general"] = true
	newResponse := wizardRevisionCall(ctx, h, "e85-r1-concurrent-ai-edit", newer)
	close(release)
	response := <-done
	if newResponse.Code != 409 || !strings.Contains(newResponse.Body.String(), "wizard_desired_projection_blocked") {
		t.Fatal("newer explicit AI decision was not saved blocked")
	}
	if response.Code != 409 || !strings.Contains(response.Body.String(), "wizard_desired_projection_blocked") {
		t.Fatal("disjoint expansion bypassed desired admission")
	}
	home, err = store.GetHomelabByOwner(ctx, tenant, owner)
	if err != nil {
		t.Fatal("saved expansion unavailable")
	}
	wizard = home.Intent["wizard"].(map[string]any)
	after, err := wizardIntentFromDocument(wizard)
	if err != nil || after.UseCaseSettings["ai"]["model-pack-general"] != true || !reflect.DeepEqual(after.UseCaseCapabilities, baseline.UseCaseCapabilities) || !reflect.DeepEqual(after.UseCaseSetupAnswers, baseline.UseCaseSetupAnswers) || wizard["projection_status"] != "blocked" || len(wizard["blocked_capabilities"].([]any)) != 1 {
		t.Fatal("disjoint expansion erased recorded/off/empty choices or unresolved marker")
	}
	if !reflect.DeepEqual(before, wizardRevisionResourceCounts(t, ctx, db)) {
		t.Fatal("disjoint blocked request created resources")
	}
	t.Log("E85_R1 actual_handler_saved_then_disjoint_expansion=blocked409 canonical_choices_and_marker_preserved=true newer_explicit_decision_survives_stale_inheritance=true runtime_resources_unchanged=true own_extra_goal_and_setting_cleanup_registered=true")
}

type wizardReservationPause struct {
	*controlplane.PostgresStore
	hash    string
	entered chan struct{}
	release <-chan struct{}
}

func (s *wizardReservationPause) ReserveWizardRun(ctx context.Context, run controlplane.WizardRun) (*controlplane.WizardRun, error) {
	if run.RequestSHA256 == s.hash {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.PostgresStore.ReserveWizardRun(ctx, run)
}

func TestWizardReservedKeyExcludesExecutableHandler(t *testing.T) {
	ctx, db, store, h := wizardRevisionRuntime(t)
	before := wizardRevisionResourceCounts(t, ctx, db)
	blocked := wizardRevisionRequest()
	executable := wizardRevisionRequest()
	executable.Intent.UseCaseCapabilities = map[string]map[string]string{"ai": {"inference": "ollama", "observability": "off"}}
	executable.Intent.UseCaseSetupAnswers = map[string]map[string][]string{"ai": {"tool-inference": {"ollama"}, "extras": {}}}
	entered, release := make(chan struct{}), make(chan struct{})
	h.wizardRuns = &wizardReservationPause{PostgresStore: store, hash: wizardRunRequestHash(executable), entered: entered, release: release}
	done := make(chan *httptest.ResponseRecorder, 1)
	key := "e85-r2-interleaving-" + time.Now().UTC().Format("20060102T150405.000000000")
	go func() { done <- wizardRevisionCall(ctx, h, key, executable) }()
	select {
	case <-entered:
	case <-ctx.Done():
		close(release)
		<-done
		t.Fatal("executable handler did not reach pre-effect reservation")
	case response := <-done:
		close(release)
		t.Fatalf("executable handler returned %d before interleaving", response.Code)
	}
	first := wizardRevisionCall(ctx, h, key, blocked)
	close(release)
	second := <-done
	if first.Code != 409 || !strings.Contains(first.Body.String(), "wizard_desired_projection_blocked") || second.Code != 409 || !strings.Contains(second.Body.String(), "wizard_idempotency_conflict") {
		t.Fatal("changed executable handler bypassed the blocked key reservation")
	}
	run, err := store.GetWizardRunByKey(ctx, os.Getenv("E85_TENANT"), os.Getenv("E85_OWNER"), key)
	if err != nil || run.RequestSHA256 != wizardRunRequestHash(blocked) || run.StackID != "" || run.NodeID != "" || run.JobID != "" || run.PairingJobID != "" {
		t.Fatal("reserved identity or empty runtime references changed")
	}
	h.wizardRuns = store
	if retry := wizardRevisionCall(ctx, h, key, blocked); retry.Code != 409 || !strings.Contains(retry.Body.String(), "wizard_desired_projection_blocked") {
		t.Fatal("same-request blocked retry failed")
	}
	replayed, err := store.GetWizardRunByKey(ctx, os.Getenv("E85_TENANT"), os.Getenv("E85_OWNER"), key)
	if err != nil || replayed.ID != run.ID || !reflect.DeepEqual(before, wizardRevisionResourceCounts(t, ctx, db)) {
		t.Fatal("retry changed identity or runtime resources")
	}
	if _, err = store.ReserveWizardRun(ctx, controlplane.WizardRun{ID: run.ID, TenantID: run.TenantID, OwnerSubjectID: run.OwnerSubjectID, IdempotencyKey: key, RequestSHA256: "different", RunKind: run.RunKind, RequestedRunKind: run.RequestedRunKind, Status: "failed"}); !errors.Is(err, controlplane.ErrConflict) {
		t.Fatal("existing key was rebound")
	}
	t.Log("E85_R2 actual_executable_handler_paused_after_prepare=true blocked_handler_reserves_first=true changed_executable_handler_conflict409_before_effects=true same_key_retry_identity=true runtime_resources_unchanged=true")
	t.Logf("E85_R2 run_id=%s key=%s", run.ID, key)
}
