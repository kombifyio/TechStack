package tenant

import (
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/pocketbase/pocketbase/core"
)

const tenantlessRecordsSampleLimit = 10
const backfillRecordsBatchLimit = 200

// BackfillIssueKind identifies the type of SaaS tenant backfill problem.
type BackfillIssueKind string

const (
	BackfillIssueMissingField      BackfillIssueKind = "missing_tenant_field"
	BackfillIssueTenantlessRecords BackfillIssueKind = "tenantless_records"
)

// TenantScopedCollections lists PocketBase collections that must carry
// tenant_id before the SaaS tenant enforcer can be considered safe.
// NOTE: "nodes", "services", and "activity_log" are intentionally omitted —
// migration 006 (PB-retirement) took ownership of those Postgres tables; they
// are no longer PocketBase-managed collections and their _collections metadata
// is purged by Migrate()'s self-heal block.
var TenantScopedCollections = []string{
	"stacks",
	"jobs",
	"workers",
	"homelab_members",
	"vm_leases",
	"vm_lease_enrollment_outbox",
	"vm_lease_operation_journal",
}

// BackfillIssue describes a collection that is not safe for SaaS tenant isolation.
type BackfillIssue struct {
	Collection string
	Kind       BackfillIssueKind
	Count      int
}

func (i BackfillIssue) String() string {
	if i.Count > 0 {
		return fmt.Sprintf("%s:%s(count>=%d)", i.Collection, i.Kind, i.Count)
	}
	return fmt.Sprintf("%s:%s", i.Collection, i.Kind)
}

// BackfillReport captures the tenant backfill status for startup logging/tests.
type BackfillReport struct {
	Mode    config.DeploymentMode
	Checked []string
	Issues  []BackfillIssue
}

// BackfillRepairReport captures tenant_id repairs performed before the strict
// SaaS startup check.
type BackfillRepairReport struct {
	Mode    config.DeploymentMode
	Updated map[string]int
	Skipped map[string]int
}

// TotalUpdated returns the number of records repaired across collections.
func (r BackfillRepairReport) TotalUpdated() int {
	total := 0
	for _, count := range r.Updated {
		total += count
	}
	return total
}

// TotalSkipped returns the number of records that could not be repaired.
func (r BackfillRepairReport) TotalSkipped() int {
	total := 0
	for _, count := range r.Skipped {
		total += count
	}
	return total
}

// IssueSummaries returns compact strings safe to emit in startup logs.
func (r BackfillReport) IssueSummaries() []string {
	if len(r.Issues) == 0 {
		return nil
	}
	out := make([]string, 0, len(r.Issues))
	for _, issue := range r.Issues {
		out = append(out, issue.String())
	}
	return out
}

// FatalIssues returns the backfill issues that must fail SaaS startup.
func (r BackfillReport) FatalIssues() []BackfillIssue {
	if len(r.Issues) == 0 {
		return nil
	}
	out := make([]BackfillIssue, 0, len(r.Issues))
	for _, issue := range r.Issues {
		if issue.Kind == BackfillIssueTenantlessRecords && allowsLegacyTenantlessRecords(issue.Collection) {
			continue
		}
		out = append(out, issue)
	}
	return out
}

// FatalIssueSummaries returns compact startup-log strings for fatal issues.
func (r BackfillReport) FatalIssueSummaries() []string {
	issues := r.FatalIssues()
	if len(issues) == 0 {
		return nil
	}
	out := make([]string, 0, len(issues))
	for _, issue := range issues {
		out = append(out, issue.String())
	}
	return out
}

// BackfillStackScopedTenantIDs repairs derived records whose tenant_id can be
// safely copied from their owning stack or owner cloud link. It intentionally
// uses the app without hooks so old records don't re-trigger lifecycle side
// effects.
func BackfillStackScopedTenantIDs(app core.App, mode config.DeploymentMode) (*BackfillRepairReport, error) {
	report := &BackfillRepairReport{
		Mode:    mode,
		Updated: map[string]int{},
		Skipped: map[string]int{},
	}
	if !mode.IsSaaS() {
		return report, nil
	}

	for _, name := range []string{"jobs", "activity_log"} {
		updated, skipped, err := backfillTenantIDFromStack(app, name)
		if err != nil {
			return report, err
		}
		if updated > 0 {
			report.Updated[name] = updated
		}
		if skipped > 0 {
			report.Skipped[name] = skipped
		}
	}

	updated, skipped, err := backfillServiceTenantIDs(app)
	if err != nil {
		return report, err
	}
	if updated > 0 {
		report.Updated["services"] = updated
	}
	if skipped > 0 {
		report.Skipped["services"] = skipped
	}

	updated, skipped, err = backfillWorkerTenantIDs(app)
	if err != nil {
		return report, err
	}
	if updated > 0 {
		report.Updated["workers"] = updated
	}
	if skipped > 0 {
		report.Skipped["workers"] = skipped
	}

	updated, skipped, err = backfillStackTenantIDs(app)
	if err != nil {
		return report, err
	}
	if updated > 0 {
		report.Updated["stacks"] = updated
	}
	if skipped > 0 {
		report.Skipped["stacks"] = skipped
	}

	return report, nil
}

// backfillStackTenantIDs derives tenant_id for legacy stack records from the
// owner's user_links org membership. Stacks created before SaaS tenancy was
// enforced live on the live Render instance without a tenant_id and prevent
// startup; tolerating them outright would break tenant isolation, so repair
// before the strict check instead.
func backfillStackTenantIDs(app core.App) (int, int, error) {
	collection, err := app.FindCollectionByNameOrId("stacks")
	if err != nil {
		return 0, 0, nil
	}
	if collection.Fields.GetByName("tenant_id") == nil {
		return 0, 0, nil
	}

	updated := 0
	skipped := 0
	for {
		records, err := app.FindRecordsByFilter(collection, "tenant_id = ''", "", backfillRecordsBatchLimit, 0)
		if err != nil {
			return updated, skipped, fmt.Errorf("query tenantless stacks records: %w", err)
		}
		if len(records) == 0 {
			break
		}
		batchUpdated := 0
		for _, record := range records {
			tenantID, ok := tenantIDFromOwnerLink(app, record.GetString("owner_id"))
			if !ok || tenantID == "" {
				skipped++
				continue
			}
			record.Set("tenant_id", tenantID)
			if err := app.UnsafeWithoutHooks().SaveNoValidate(record); err != nil {
				return updated, skipped, fmt.Errorf("save tenant_id for stacks/%s: %w", record.Id, err)
			}
			updated++
			batchUpdated++
		}
		if batchUpdated == 0 || len(records) < backfillRecordsBatchLimit {
			break
		}
	}
	return updated, skipped, nil
}

// CheckBackfill verifies SaaS tenant-scoped collections have tenant_id and no
// tenantless records. Self-hosted mode is explicitly compatible and returns no
// issues so old single-tenant data remains valid.
func CheckBackfill(app core.App, mode config.DeploymentMode) (*BackfillReport, error) {
	report := &BackfillReport{Mode: mode}
	if !mode.IsSaaS() {
		return report, nil
	}

	for _, name := range TenantScopedCollections {
		collection, err := app.FindCollectionByNameOrId(name)
		if err != nil {
			continue
		}
		report.Checked = append(report.Checked, collection.Name)

		if collection.Fields.GetByName("tenant_id") == nil {
			report.Issues = append(report.Issues, BackfillIssue{
				Collection: collection.Name,
				Kind:       BackfillIssueMissingField,
			})
			continue
		}

		records, err := app.FindRecordsByFilter(collection, "tenant_id = ''", "", tenantlessRecordsSampleLimit, 0)
		if err != nil {
			return report, fmt.Errorf("query tenantless records for %s: %w", collection.Name, err)
		}
		if len(records) > 0 {
			report.Issues = append(report.Issues, BackfillIssue{
				Collection: collection.Name,
				Kind:       BackfillIssueTenantlessRecords,
				Count:      len(records),
			})
		}
	}

	if fatal := report.FatalIssueSummaries(); len(fatal) > 0 {
		return report, fmt.Errorf("tenant backfill check failed: %s", strings.Join(fatal, ", "))
	}
	return report, nil
}

func allowsLegacyTenantlessRecords(collection string) bool {
	switch collection {
	// Residual tenantless rows in these collections do not break tenant
	// isolation - all tenant-scoped queries filter `tenant_id = {orgID}`
	// and a row with an empty tenant_id matches no live tenant, so it is
	// invisible rather than leaked. The startup-strict check still flags
	// them as a warning so operators see they exist, but deploy may
	// proceed. stacks was added 2026-05-20 after a Render-hosted instance
	// carried over a pre-SaaS stack with no user_links org membership;
	// the backfill repair tries to derive tenant_id from owner_id first
	// (see backfillStackTenantIDs) and only falls through to this
	// tolerance when no derivation is possible.
	case "jobs", "activity_log", "workers", "stacks":
		return true
	default:
		return false
	}
}

func backfillWorkerTenantIDs(app core.App) (int, int, error) {
	collection, err := app.FindCollectionByNameOrId("workers")
	if err != nil {
		return 0, 0, nil
	}
	if collection.Fields.GetByName("tenant_id") == nil {
		return 0, 0, nil
	}

	updated := 0
	skipped := 0
	for {
		records, err := app.FindRecordsByFilter(collection, "tenant_id = ''", "", backfillRecordsBatchLimit, 0)
		if err != nil {
			return updated, skipped, fmt.Errorf("query tenantless workers records: %w", err)
		}
		if len(records) == 0 {
			break
		}
		batchUpdated := 0
		for _, record := range records {
			tenantID, ok := workerTenantID(app, record)
			if !ok || tenantID == "" {
				skipped++
				continue
			}
			record.Set("tenant_id", tenantID)
			if err := app.UnsafeWithoutHooks().SaveNoValidate(record); err != nil {
				return updated, skipped, fmt.Errorf("save tenant_id for workers/%s: %w", record.Id, err)
			}
			updated++
			batchUpdated++
		}
		if batchUpdated == 0 || len(records) < backfillRecordsBatchLimit {
			break
		}
	}
	return updated, skipped, nil
}

func backfillTenantIDFromStack(app core.App, collectionName string) (int, int, error) {
	collection, err := app.FindCollectionByNameOrId(collectionName)
	if err != nil {
		return 0, 0, nil
	}
	if collection.Fields.GetByName("tenant_id") == nil || collection.Fields.GetByName("stack_id") == nil {
		return 0, 0, nil
	}

	updated := 0
	skipped := 0
	for {
		records, err := app.FindRecordsByFilter(collection, "tenant_id = '' && stack_id != ''", "", backfillRecordsBatchLimit, 0)
		if err != nil {
			return updated, skipped, fmt.Errorf("query tenantless %s records: %w", collection.Name, err)
		}
		if len(records) == 0 {
			break
		}
		batchUpdated := 0
		for _, record := range records {
			tenantID, ok, err := stackTenantID(app, record.GetString("stack_id"))
			if err != nil {
				return updated, skipped, err
			}
			if !ok || tenantID == "" {
				skipped++
				continue
			}
			record.Set("tenant_id", tenantID)
			if err := app.UnsafeWithoutHooks().SaveNoValidate(record); err != nil {
				return updated, skipped, fmt.Errorf("save tenant_id for %s/%s: %w", collection.Name, record.Id, err)
			}
			updated++
			batchUpdated++
		}
		if batchUpdated == 0 || len(records) < backfillRecordsBatchLimit {
			break
		}
	}
	return updated, skipped, nil
}

func backfillServiceTenantIDs(app core.App) (int, int, error) {
	collection, err := app.FindCollectionByNameOrId("services")
	if err != nil {
		return 0, 0, nil
	}
	if collection.Fields.GetByName("tenant_id") == nil || collection.Fields.GetByName("node_id") == nil {
		return 0, 0, nil
	}

	updated := 0
	skipped := 0
	for {
		records, err := app.FindRecordsByFilter(collection, "tenant_id = '' && node_id != ''", "", backfillRecordsBatchLimit, 0)
		if err != nil {
			return updated, skipped, fmt.Errorf("query tenantless services records: %w", err)
		}
		if len(records) == 0 {
			break
		}
		batchUpdated := 0
		for _, record := range records {
			tenantID, ok, err := nodeTenantID(app, record.GetString("node_id"))
			if err != nil {
				return updated, skipped, err
			}
			if !ok || tenantID == "" {
				skipped++
				continue
			}
			record.Set("tenant_id", tenantID)
			if err := app.UnsafeWithoutHooks().SaveNoValidate(record); err != nil {
				return updated, skipped, fmt.Errorf("save tenant_id for services/%s: %w", record.Id, err)
			}
			updated++
			batchUpdated++
		}
		if batchUpdated == 0 || len(records) < backfillRecordsBatchLimit {
			break
		}
	}
	return updated, skipped, nil
}

func stackTenantID(app core.App, stackID string) (string, bool, error) {
	stackID = strings.TrimSpace(stackID)
	if stackID == "" {
		return "", false, nil
	}
	stack, err := app.FindRecordById("stacks", stackID)
	if err != nil {
		return "", false, nil
	}
	return strings.TrimSpace(stack.GetString("tenant_id")), true, nil
}

func nodeTenantID(app core.App, nodeID string) (string, bool, error) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return "", false, nil
	}
	node, err := app.FindRecordById("nodes", nodeID)
	if err != nil {
		return "", false, nil
	}
	tenantID := strings.TrimSpace(node.GetString("tenant_id"))
	if tenantID != "" {
		return tenantID, true, nil
	}
	return stackTenantID(app, node.GetString("stack_id"))
}

func workerTenantID(app core.App, worker *core.Record) (string, bool) {
	if worker == nil {
		return "", false
	}
	if tenantID, ok := tenantIDFromStackBestEffort(app, worker.GetString("stack_id")); ok {
		return tenantID, true
	}
	return tenantIDFromOwnerLink(app, worker.GetString("owner_id"))
}

func tenantIDFromStackBestEffort(app core.App, stackID string) (string, bool) {
	tenantID, ok, err := stackTenantID(app, stackID)
	if err != nil || !ok || tenantID == "" {
		return "", false
	}
	return tenantID, true
}

func tenantIDFromOwnerLink(app core.App, ownerID string) (string, bool) {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return "", false
	}
	collection, err := app.FindCollectionByNameOrId("user_links")
	if err != nil || collection.Fields.GetByName("org_id") == nil {
		return "", false
	}
	records, err := app.FindRecordsByFilter(
		collection,
		"user = {:ownerID} && org_id != ''",
		"",
		tenantlessRecordsSampleLimit,
		0,
		map[string]any{"ownerID": ownerID},
	)
	if err != nil || len(records) == 0 {
		return "", false
	}
	tenantID := ""
	for _, record := range records {
		orgID := strings.TrimSpace(record.GetString("org_id"))
		if orgID == "" {
			continue
		}
		if tenantID == "" {
			tenantID = orgID
			continue
		}
		if tenantID != orgID {
			return "", false
		}
	}
	if tenantID == "" {
		return "", false
	}
	return tenantID, true
}
