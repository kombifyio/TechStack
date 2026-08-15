package tenant

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

func TestCheckBackfillSelfHostedSkipsTenantlessRecords(t *testing.T) {
	app := newBackfillTestApp(t)
	collection := ensureBackfillTestCollection(t, app, "stacks", &core.TextField{Name: "tenant_id", Max: 200})
	createBackfillTestRecord(t, app, collection, "")

	report, err := CheckBackfill(app, config.ModeSelfHosted)
	if err != nil {
		t.Fatalf("CheckBackfill self-hosted: %v", err)
	}
	if len(report.Issues) != 0 {
		t.Fatalf("self-hosted issues = %v, want none", report.Issues)
	}
}

func TestCheckBackfillSaaSWarnsButTolerantOfLegacyTenantlessStacks(t *testing.T) {
	// Pre-SaaS stacks that have no derivable tenant_id (no user_links
	// org membership) are recorded as a warning issue, but they do not
	// crash the SaaS startup check - tenant-scoped queries filter
	// `tenant_id = {orgID}` so a tenantless row matches no live tenant
	// and is therefore invisible. See backfill.go allowsLegacyTenantlessRecords.
	app := newBackfillTestApp(t)
	collection := ensureBackfillTestCollection(t, app, "stacks", &core.TextField{Name: "tenant_id", Max: 200})
	createBackfillTestRecord(t, app, collection, "")

	report, err := CheckBackfill(app, config.ModeSaaS)
	if err != nil {
		t.Fatalf("CheckBackfill SaaS legacy stacks: %v", err)
	}
	if len(report.Issues) != 1 {
		t.Fatalf("issues = %v, want one warning issue", report.Issues)
	}
	if len(report.FatalIssues()) != 0 {
		t.Fatalf("fatal issues = %v, want none", report.FatalIssues())
	}
	issue := report.Issues[0]
	if issue.Collection != "stacks" || issue.Kind != BackfillIssueTenantlessRecords {
		t.Fatalf("issue = %#v, want stacks tenantless_records", issue)
	}
}

func TestCheckBackfillSaaSAllowsLegacyOperationalTenantlessRecords(t *testing.T) {
	app := newBackfillTestApp(t)
	collection := ensureBackfillTestCollection(t, app, "jobs", &core.TextField{Name: "tenant_id", Max: 200})
	createBackfillTestRecord(t, app, collection, "")

	report, err := CheckBackfill(app, config.ModeSaaS)
	if err != nil {
		t.Fatalf("CheckBackfill SaaS operational records: %v", err)
	}
	if len(report.Issues) != 1 {
		t.Fatalf("issues = %v, want one warning issue", report.Issues)
	}
	if len(report.FatalIssues()) != 0 {
		t.Fatalf("fatal issues = %v, want none", report.FatalIssues())
	}
}

func TestCheckBackfillSaaSAllowsLegacyWorkerTenantlessRecords(t *testing.T) {
	app := newBackfillTestApp(t)
	collection := ensureBackfillTestCollection(t, app, "workers",
		&core.TextField{Name: "tenant_id", Max: 200},
		&core.TextField{Name: "owner_id", Max: 200},
	)
	createBackfillTestRecordWithFields(t, app, collection, "", map[string]any{"owner_id": "owner-1"})

	report, err := CheckBackfill(app, config.ModeSaaS)
	if err != nil {
		t.Fatalf("CheckBackfill SaaS legacy workers: %v", err)
	}
	if len(report.Issues) != 1 {
		t.Fatalf("issues = %v, want one warning issue", report.Issues)
	}
	if len(report.FatalIssues()) != 0 {
		t.Fatalf("fatal issues = %v, want none", report.FatalIssues())
	}
}

func TestCheckBackfillSaaSRejectsMissingTenantField(t *testing.T) {
	app := newBackfillTestApp(t)
	ensureBackfillTestCollection(t, app, "workers", &core.TextField{Name: "hostname", Max: 255})

	report, err := CheckBackfill(app, config.ModeSaaS)
	if err == nil {
		t.Fatal("CheckBackfill SaaS with missing tenant_id field: expected error")
	}
	if len(report.Issues) != 1 {
		t.Fatalf("issues = %v, want one", report.Issues)
	}
	issue := report.Issues[0]
	if issue.Collection != "workers" || issue.Kind != BackfillIssueMissingField {
		t.Fatalf("issue = %#v, want workers missing_tenant_field", issue)
	}
}

func TestCheckBackfillSaaSPassesScopedRecords(t *testing.T) {
	app := newBackfillTestApp(t)
	collection := ensureBackfillTestCollection(t, app, "stacks", &core.TextField{Name: "tenant_id", Max: 200})
	createBackfillTestRecord(t, app, collection, "org-42")

	report, err := CheckBackfill(app, config.ModeSaaS)
	if err != nil {
		t.Fatalf("CheckBackfill SaaS scoped records: %v", err)
	}
	if len(report.Issues) != 0 {
		t.Fatalf("issues = %v, want none", report.Issues)
	}
}

func TestBackfillStackScopedTenantIDsRepairsWorkersFromStackOrOwnerLink(t *testing.T) {
	app := newBackfillTestApp(t)
	stacks := ensureBackfillTestCollection(t, app, "stacks", &core.TextField{Name: "tenant_id", Max: 200})
	stack := createBackfillTestRecord(t, app, stacks, "org-stack")
	userLinks := ensureBackfillTestCollection(t, app, "user_links",
		&core.TextField{Name: "user", Max: 200},
		&core.TextField{Name: "org_id", Max: 200},
	)
	createBackfillPlainTestRecord(t, app, userLinks, map[string]any{"user": "owner-1", "org_id": "org-owner"})
	workers := ensureBackfillTestCollection(t, app, "workers",
		&core.TextField{Name: "tenant_id", Max: 200},
		&core.TextField{Name: "stack_id", Max: 200},
		&core.TextField{Name: "owner_id", Max: 200},
	)
	stackWorker := createBackfillTestRecordWithFields(t, app, workers, "", map[string]any{"stack_id": stack.Id})
	ownerWorker := createBackfillTestRecordWithFields(t, app, workers, "", map[string]any{"owner_id": "owner-1"})

	repair, err := BackfillStackScopedTenantIDs(app, config.ModeSaaS)
	if err != nil {
		t.Fatalf("BackfillStackScopedTenantIDs: %v", err)
	}
	if got := repair.Updated["workers"]; got != 2 {
		t.Fatalf("workers updated = %d (%v), want 2", got, repair.Updated)
	}
	assertBackfillRecordTenantID(t, app, "workers", stackWorker.Id, "org-stack")
	assertBackfillRecordTenantID(t, app, "workers", ownerWorker.Id, "org-owner")
}

func TestBackfillStackScopedTenantIDsRepairsDerivedRecords(t *testing.T) {
	app := newBackfillTestApp(t)
	stacks := ensureBackfillTestCollection(t, app, "stacks", &core.TextField{Name: "tenant_id", Max: 200})
	stack := createBackfillTestRecord(t, app, stacks, "org-42")
	jobs := ensureBackfillTestCollection(t, app, "jobs",
		&core.TextField{Name: "tenant_id", Max: 200},
		&core.TextField{Name: "stack_id", Max: 200},
	)
	activity := ensureBackfillTestCollection(t, app, "activity_log",
		&core.TextField{Name: "tenant_id", Max: 200},
		&core.TextField{Name: "stack_id", Max: 200},
	)
	job := createBackfillTestRecordWithFields(t, app, jobs, "", map[string]any{"stack_id": stack.Id})
	log := createBackfillTestRecordWithFields(t, app, activity, "", map[string]any{"stack_id": stack.Id})

	repair, err := BackfillStackScopedTenantIDs(app, config.ModeSaaS)
	if err != nil {
		t.Fatalf("BackfillStackScopedTenantIDs: %v", err)
	}
	if repair.TotalUpdated() != 2 {
		t.Fatalf("updated = %d (%v), want 2", repair.TotalUpdated(), repair.Updated)
	}
	assertBackfillRecordTenantID(t, app, "jobs", job.Id, "org-42")
	assertBackfillRecordTenantID(t, app, "activity_log", log.Id, "org-42")

	report, err := CheckBackfill(app, config.ModeSaaS)
	if err != nil {
		t.Fatalf("CheckBackfill after repair: %v", err)
	}
	if len(report.Issues) != 0 {
		t.Fatalf("issues after repair = %v, want none", report.Issues)
	}
}

func newBackfillTestApp(t *testing.T) *tests.TestApp {
	t.Helper()

	app, err := tests.NewTestApp(t.TempDir())
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	t.Cleanup(app.Cleanup)
	return app
}

func ensureBackfillTestCollection(t *testing.T, app core.App, name string, fields ...core.Field) *core.Collection {
	t.Helper()

	collection := core.NewBaseCollection(name)
	collection.Fields.Add(fields...)
	if err := app.Save(collection); err != nil {
		t.Fatalf("save %s collection: %v", name, err)
	}
	return collection
}

func createBackfillTestRecord(t *testing.T, app core.App, collection *core.Collection, tenantID string) *core.Record {
	t.Helper()

	return createBackfillTestRecordWithFields(t, app, collection, tenantID, nil)
}

func createBackfillTestRecordWithFields(t *testing.T, app core.App, collection *core.Collection, tenantID string, fields map[string]any) *core.Record {
	t.Helper()

	record := core.NewRecord(collection)
	record.Set("tenant_id", tenantID)
	for key, value := range fields {
		record.Set(key, value)
	}
	if err := app.Save(record); err != nil {
		t.Fatalf("save %s record: %v", collection.Name, err)
	}
	return record
}

func createBackfillPlainTestRecord(t *testing.T, app core.App, collection *core.Collection, fields map[string]any) *core.Record {
	t.Helper()

	record := core.NewRecord(collection)
	for key, value := range fields {
		record.Set(key, value)
	}
	if err := app.Save(record); err != nil {
		t.Fatalf("save %s record: %v", collection.Name, err)
	}
	return record
}

func assertBackfillRecordTenantID(t *testing.T, app core.App, collectionName, recordID, want string) {
	t.Helper()

	record, err := app.FindRecordById(collectionName, recordID)
	if err != nil {
		t.Fatalf("find %s/%s: %v", collectionName, recordID, err)
	}
	if got := record.GetString("tenant_id"); got != want {
		t.Fatalf("%s/%s tenant_id = %q, want %q", collectionName, recordID, got, want)
	}
}
