package routes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

func TestApplyDriftResultPayload_ReadsJSONFields(t *testing.T) {
	collection := core.NewBaseCollection("drift_results")
	collection.Fields.Add(
		&core.JSONField{Name: "affected_resources"},
		&core.JSONField{Name: "plan_summary"},
	)

	record := core.NewRecord(collection)
	affectedResources := []ResourceChangeResponse{
		{
			Address:      "module.app.aws_instance.web",
			ResourceType: "aws_instance",
			Action:       "update",
			Changes: map[string]interface{}{
				"instance_type": map[string]interface{}{
					"before": "t3.small",
					"after":  "t3.medium",
				},
			},
		},
	}
	summary := DriftSummaryResponse{
		ToCreate: 1,
		ToUpdate: 2,
		ToDelete: 3,
	}

	record.Set("affected_resources", affectedResources)
	record.Set("plan_summary", summary)

	response := &DriftDetailsResponse{}
	applyDriftResultPayload(record, response)

	if len(response.AffectedResources) != 1 {
		t.Fatalf("expected 1 affected resource, got %d", len(response.AffectedResources))
	}
	if response.AffectedResources[0].Address != affectedResources[0].Address {
		t.Fatalf("expected address %q, got %q", affectedResources[0].Address, response.AffectedResources[0].Address)
	}
	if response.Summary != summary {
		t.Fatalf("expected summary %+v, got %+v", summary, response.Summary)
	}
}

func TestApplyDriftResultPayload_IgnoresInvalidJSONFields(t *testing.T) {
	collection := core.NewBaseCollection("drift_results")
	collection.Fields.Add(
		&core.JSONField{Name: "affected_resources"},
		&core.JSONField{Name: "plan_summary"},
	)

	record := core.NewRecord(collection)
	record.Set("affected_resources", "not-json-array")
	record.Set("plan_summary", "not-json-object")

	response := &DriftDetailsResponse{
		AffectedResources: []ResourceChangeResponse{{Address: "existing"}},
		Summary:           DriftSummaryResponse{ToCreate: 1},
	}
	applyDriftResultPayload(record, response)

	if len(response.AffectedResources) != 1 || response.AffectedResources[0].Address != "existing" {
		t.Fatalf("invalid affected_resources should leave response unchanged: %+v", response.AffectedResources)
	}
	if response.Summary.ToCreate != 1 {
		t.Fatalf("invalid plan_summary should leave response unchanged: %+v", response.Summary)
	}
}

func TestDriftDecodeTriggerRequestIgnoresInvalidJSON(t *testing.T) {
	handler := driftRouteHandlers{}
	event, _ := driftRouteTestEvent(http.MethodPost, "/api/v1/stacks/stack-1/drift/check", "stack-1", "owner-1", `{`)

	req := handler.decodeTriggerRequest(event)

	if req.Force {
		t.Fatal("expected invalid JSON body to fall back to Force=false")
	}
}

func TestDriftRecentCheckRateLimitOnlyBlocksNonForced(t *testing.T) {
	app := newDriftRouteTestApp(t, true)
	stack := createDriftRouteTestStack(t, app, "owner-1", "running")
	stack.Set("drift_checked_at", time.Now().Add(-2*time.Minute))
	if err := app.Save(stack); err != nil {
		t.Fatalf("save stack: %v", err)
	}

	handler := driftRouteHandlers{app: app}
	forcedEvent, _ := driftRouteTestEvent(http.MethodPost, "/api/v1/stacks/"+stack.Id+"/drift/check", stack.Id, "owner-1", `{"force":true}`)
	limited, err := handler.recentCheckRateLimit(forcedEvent, stack, true)
	if err != nil {
		t.Fatalf("forced check should bypass rate limit: %v", err)
	}
	if limited {
		t.Fatal("forced check should not be reported as rate limited")
	}

	limitedEvent, limitedRecorder := driftRouteTestEvent(http.MethodPost, "/api/v1/stacks/"+stack.Id+"/drift/check", stack.Id, "owner-1", ``)
	limited, err = handler.recentCheckRateLimit(limitedEvent, stack, false)
	if err != nil {
		t.Fatalf("rate limit should write response without returning transport error: %v", err)
	}
	if !limited {
		t.Fatal("expected non-forced recent check to report rate limited")
	}
	if limitedRecorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d body=%s, want 429", limitedRecorder.Code, limitedRecorder.Body.String())
	}
}

func TestDriftDetailsNeverCheckedReturnsStatus(t *testing.T) {
	app := newDriftRouteTestApp(t, true)
	stack := createDriftRouteTestStack(t, app, "owner-1", "running")
	handler := driftRouteHandlers{app: app}

	response, ok, err := handler.stackDriftDetailsResponse(stack)

	if err != nil {
		t.Fatalf("stackDriftDetailsResponse returned error: %v", err)
	}
	if !ok {
		t.Fatal("expected never-checked stack to produce a response")
	}
	if response.Status != "never_checked" {
		t.Fatalf("status = %q, want never_checked", response.Status)
	}
	if response.Summary != (DriftSummaryResponse{}) {
		t.Fatalf("summary = %+v, want empty summary", response.Summary)
	}
}

func TestDriftStackScopedLastResultRequiresOwnerAndStack(t *testing.T) {
	app := newDriftRouteTestApp(t, true)
	stack := createDriftRouteTestStack(t, app, "owner-1", "running")
	handler := driftRouteHandlers{app: app}

	foreignOwnerResult := createDriftRouteTestResult(t, app, stack.Id, "owner-2")
	assertDriftLastResultIgnored(t, handler, app, stack, foreignOwnerResult)

	foreignStackResult := createDriftRouteTestResult(t, app, "other-stack", "owner-1")
	assertDriftLastResultIgnored(t, handler, app, stack, foreignStackResult)
}

func assertDriftLastResultIgnored(t *testing.T, handler driftRouteHandlers, app core.App, stack, result *core.Record) {
	t.Helper()

	result.Set("job_id", "foreign-job")
	result.Set("affected_count", 7)
	if err := app.Save(result); err != nil {
		t.Fatalf("save drift result: %v", err)
	}
	stack.Set("last_drift_result_id", result.Id)
	if err := app.Save(stack); err != nil {
		t.Fatalf("save stack: %v", err)
	}

	status := handler.stackDriftStatusResponse(stack, stack.Id)
	if status.LastResultID != "" || status.LastJobID != "" || status.AffectedCount != 0 {
		t.Fatalf("status leaked foreign result fields: %+v", status)
	}
	_, ok, err := handler.stackDriftDetailsResponse(stack)
	if err != nil {
		t.Fatalf("details returned error: %v", err)
	}
	if ok {
		t.Fatal("expected foreign last_drift_result_id to be treated as not found")
	}
}

func TestDriftOwnedStackRejectsSignedEdgeAdminWhenNotOwner(t *testing.T) {
	app := newDriftRouteTestApp(t, true)
	stack := createDriftRouteTestStack(t, app, "owner-1", "running")
	handler := driftRouteHandlers{app: app}
	event, recorder := driftRouteTestEvent(http.MethodGet, "/api/v1/stacks/"+stack.Id+"/drift/status", stack.Id, "api-key:techstack-admin", ``)
	event.Request = event.Request.WithContext(identity.NewContext(context.Background(), &identity.Identity{
		UserID: "api-key:techstack-admin",
		Roles:  []string{"admin"},
	}))

	_, _, ok, err := handler.ownedStack(event, "api-key:techstack-admin")
	if err != nil {
		t.Fatalf("ownedStack should write response without returning transport error: %v", err)
	}
	if ok {
		t.Fatal("signed edge admin should not own another owner's stack")
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want 403", recorder.Code, recorder.Body.String())
	}
}

func TestDriftStatusUnauthenticatedStopsBeforeStackLookup(t *testing.T) {
	app := newDriftRouteTestApp(t, true)
	handler := driftRouteHandlers{app: app}
	event, recorder := driftRouteTestEvent(http.MethodGet, "/api/v1/stacks/missing-stack/drift/status", "missing-stack", "", ``)

	if err := handler.status(event); err != nil && !errors.Is(err, httpx.ErrResponseWritten) {
		t.Fatalf("status returned unexpected error: %v", err)
	}
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s, want 401", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "Stack not found") || strings.Contains(recorder.Body.String(), "Not allowed") {
		t.Fatalf("unauthenticated request proceeded past auth: %s", recorder.Body.String())
	}
}

func TestDriftCheckRejectsStoppedStack(t *testing.T) {
	app := newDriftRouteTestApp(t, true)
	stack := createDriftRouteTestStack(t, app, "owner-1", "stopped")
	handler := driftRouteHandlers{app: app}
	event, recorder := driftRouteTestEvent(http.MethodPost, "/api/v1/stacks/"+stack.Id+"/drift/check", stack.Id, "owner-1", `{}`)

	if err := handler.check(event); err != nil {
		t.Fatalf("check returned unexpected error: %v", err)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s, want 400", recorder.Code, recorder.Body.String())
	}
}

func TestDriftCheckRevertsStatusWhenTriggerFails(t *testing.T) {
	app := newDriftRouteTestApp(t, false)
	stack := createDriftRouteTestStack(t, app, "owner-1", "running")
	orch := orchestrator.NewWithApp(app, orchestrator.DefaultConfig(), logger.Default())
	defer orch.Stop()

	handler := driftRouteHandlers{app: app, orch: orch}
	event, recorder := driftRouteTestEvent(http.MethodPost, "/api/v1/stacks/"+stack.Id+"/drift/check", stack.Id, "owner-1", `{}`)

	if err := handler.check(event); err != nil {
		t.Fatalf("check returned unexpected error: %v", err)
	}
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s, want 500", recorder.Code, recorder.Body.String())
	}
	reloaded, err := app.FindRecordById("stacks", stack.Id)
	if err != nil {
		t.Fatalf("reload stack: %v", err)
	}
	if got := reloaded.GetString("drift_status"); got != "unknown" {
		t.Fatalf("drift_status = %q, want unknown", got)
	}
}

func TestDriftListResponsePaginatesAndEnrichesStackNames(t *testing.T) {
	app := newDriftRouteTestApp(t, true)
	stack := createDriftRouteTestStack(t, app, "owner-1", "running")
	for i := 0; i < 3; i++ {
		createDriftRouteTestResult(t, app, stack.Id, "owner-1")
	}
	handler := driftRouteHandlers{app: app}

	response, err := handler.driftResultListResponse("owner-1", 1, 2, 0)

	if err != nil {
		t.Fatalf("driftResultListResponse returned error: %v", err)
	}
	if response.TotalItems != 3 || response.TotalPages != 2 {
		body, _ := json.Marshal(response)
		t.Fatalf("unexpected pagination response: %s", body)
	}
	if len(response.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(response.Items))
	}
	if response.Items[0].StackName != "Homelab" {
		t.Fatalf("expected best-effort stack name enrichment, got %q", response.Items[0].StackName)
	}
}

func newDriftRouteTestApp(t *testing.T, includeJobs bool) *tests.TestApp {
	t.Helper()

	app, err := tests.NewTestApp(driftRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	ensureDriftRouteTestCollections(t, app, includeJobs)
	return app
}

func ensureDriftRouteTestCollections(t *testing.T, app core.App, includeJobs bool) {
	t.Helper()

	ensureDriftRouteTestCollection(t, app, "stacks",
		&core.TextField{Name: "owner_id"},
		&core.TextField{Name: "name"},
		&core.TextField{Name: "status"},
		&core.TextField{Name: "drift_status"},
		&core.DateField{Name: "drift_checked_at"},
		&core.TextField{Name: "last_drift_result_id"},
	)
	ensureDriftRouteTestCollection(t, app, "drift_results",
		&core.TextField{Name: "owner_id"},
		&core.TextField{Name: "stack_id"},
		&core.TextField{Name: "job_id"},
		&core.TextField{Name: "status"},
		&core.NumberField{Name: "affected_count"},
		&core.TextField{Name: "trigger_type"},
		&core.DateField{Name: "checked_at"},
		&core.NumberField{Name: "duration_ms"},
		&core.TextField{Name: "error_message"},
		&core.JSONField{Name: "affected_resources"},
		&core.JSONField{Name: "plan_summary"},
	)
	if includeJobs {
		ensureDriftRouteTestCollection(t, app, "jobs",
			&core.TextField{Name: "type"},
			&core.TextField{Name: "state"},
			&core.NumberField{Name: "progress"},
			&core.TextField{Name: "stack_id"},
			&core.TextField{Name: "current_step"},
		)
	}
}

func ensureDriftRouteTestCollection(t *testing.T, app core.App, name string, fields ...core.Field) *core.Collection {
	t.Helper()

	collection, err := app.FindCollectionByNameOrId(name)
	if err != nil {
		collection = core.NewBaseCollection(name)
	}
	for _, field := range fields {
		if collection.Fields.GetByName(field.GetName()) == nil {
			collection.Fields.Add(field)
		}
	}
	if err := app.Save(collection); err != nil {
		t.Fatalf("save %s collection: %v", name, err)
	}
	return collection
}

func createDriftRouteTestStack(t *testing.T, app core.App, ownerID, status string) *core.Record {
	t.Helper()

	collection, err := app.FindCollectionByNameOrId("stacks")
	if err != nil {
		t.Fatalf("find stacks collection: %v", err)
	}
	record := core.NewRecord(collection)
	record.Set("owner_id", ownerID)
	record.Set("name", "Homelab")
	record.Set("status", status)
	record.Set("drift_status", "unknown")
	if err := app.Save(record); err != nil {
		t.Fatalf("save stack: %v", err)
	}
	return record
}

func createDriftRouteTestResult(t *testing.T, app core.App, stackID, ownerID string) *core.Record {
	t.Helper()

	collection, err := app.FindCollectionByNameOrId("drift_results")
	if err != nil {
		t.Fatalf("find drift_results collection: %v", err)
	}
	record := core.NewRecord(collection)
	record.Set("owner_id", ownerID)
	record.Set("stack_id", stackID)
	record.Set("job_id", "job-"+time.Now().Format("150405.000000000"))
	record.Set("status", "in_sync")
	record.Set("affected_count", 0)
	record.Set("trigger_type", "manual")
	record.Set("checked_at", time.Now())
	record.Set("duration_ms", 42)
	if err := app.Save(record); err != nil {
		t.Fatalf("save drift result: %v", err)
	}
	return record
}

func driftRouteTestEvent(method, target, stackID, ownerID, body string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", stackID)
	if ownerID != "" {
		req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{UserID: ownerID}))
	}
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

func driftRoutePocketBaseTestDataDir(t *testing.T) string {
	t.Helper()

	cmd := exec.Command("go", "env", "GOMODCACHE")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("resolve go module cache: %v", err)
	}

	modCache := strings.TrimSpace(string(output))
	if modCache == "" {
		t.Fatal("resolve go module cache: empty result")
	}

	matches, err := filepath.Glob(filepath.Join(modCache, "github.com", "pocketbase", "pocketbase@*", "tests", "data"))
	if err != nil {
		t.Fatalf("resolve pocketbase test data: %v", err)
	}
	if len(matches) == 0 {
		matches, err = filepath.Glob(filepath.Join(modCache, "github.com", "*", "pocketbase@*", "tests", "data"))
		if err != nil {
			t.Fatalf("resolve pocketbase test data fallback: %v", err)
		}
	}
	for _, match := range matches {
		if strings.Contains(filepath.ToSlash(match), path.Join("github.com", "pocketbase", "pocketbase@")) {
			return match
		}
	}
	if len(matches) > 0 {
		return matches[0]
	}
	t.Fatal("resolve pocketbase test data: no matches")
	return ""
}
