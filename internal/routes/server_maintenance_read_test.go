package routes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/middleware"
	"github.com/kombifyio/techstack/pkg/servermaintenance"
)

type scriptedMaintenanceDispatcher struct {
	inertMaintenanceDispatcher
	plan   servermaintenance.PlanResult
	reboot servermaintenance.RebootResult
}

func (d scriptedMaintenanceDispatcher) Plan(context.Context, servermaintenance.Target) (servermaintenance.PlanResult, error) {
	return d.plan, nil
}

func (d scriptedMaintenanceDispatcher) Reboot(context.Context, servermaintenance.Target, time.Duration) (servermaintenance.RebootResult, error) {
	return d.reboot, nil
}

// runOnce advances the job behind key by one runner step.
func (f *maintenanceFixture) runOnce(dispatcher servermaintenance.Dispatcher, key string) {
	f.t.Helper()
	job, err := f.store.GetServerMaintenanceJob(f.t.Context(), "tenant-1", serverMaintenanceJobID("tenant-1", "owner-1", key))
	if err != nil {
		f.t.Fatal(err)
	}
	runner := servermaintenance.Runner{Jobs: f.store, Servers: f.store, Dispatcher: dispatcher, Now: func() time.Time { return f.now }}
	if _, err := runner.Advance(f.t.Context(), *job); err != nil {
		f.t.Fatalf("advance: %v", err)
	}
}

func (f *maintenanceFixture) get(ownerID, jobID string) (int, serverMaintenanceJobView) {
	f.t.Helper()
	ctx := identity.NewContext(context.Background(), &identity.Identity{UserID: ownerID, OrgID: "tenant-1"})
	ctx = middleware.WithSignedEntitlements(ctx, ServerMaintenanceEntitlement)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/servers/server-1/actions/"+jobID, nil).WithContext(ctx)
	request.SetPathValue("serverId", "server-1")
	request.SetPathValue("jobId", jobID)
	recorder := httptest.NewRecorder()
	_ = f.h.maintenanceJob(&httpx.Event{Request: request, Response: recorder})
	var payload struct {
		Data serverMaintenanceJobView `json:"data"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
	return recorder.Code, payload.Data
}

// The owner follows a reboot: the job is awaiting its node, and the read
// reports the scheduled time and, once the node reports a new boot id, that
// the boot id changed.
func TestServerMaintenanceJobReadShowsTheOwnersRebootProgress(t *testing.T) {
	f := newMaintenanceFixture(t, inertMaintenanceDispatcher{})
	status, receipt, reason := f.post(fullyAuthorizedCaller, "reboot-1", reboot(7))
	if status != http.StatusAccepted {
		t.Fatalf("reboot status=%d reason=%q", status, reason)
	}
	scheduled := f.now.Add(servermaintenance.RebootDelay)
	f.runOnce(scriptedMaintenanceDispatcher{reboot: servermaintenance.RebootResult{BootIDBefore: "boot-a", ScheduledAt: scheduled}}, "reboot-1")
	f.setServer("owner-1", 7, map[string]any{servermaintenance.MetadataBootID: "boot-b"})

	status, job := f.get("owner-1", receipt.JobID)
	if status != http.StatusOK || job.State != controlplane.ServerMaintenanceStateAwaitingNodeReturn || job.Reboot == nil ||
		job.Reboot.ScheduledAt == nil || !job.Reboot.ScheduledAt.Equal(scheduled) || !job.Reboot.BootIDChanged || job.Reboot.ReturnDeadline == nil {
		t.Fatalf("status=%d job=%+v reboot=%+v", status, job, job.Reboot)
	}
	if _, leaked := job.Result["boot_id_before"]; leaked {
		t.Fatalf("result exposes the internal boot id: %v", job.Result)
	}
}

func TestServerMaintenanceJobReadHidesAnotherOwnersJob(t *testing.T) {
	f := newMaintenanceFixture(t, inertMaintenanceDispatcher{})
	_, receipt, _ := f.post(fullyAuthorizedCaller, "reboot-1", reboot(7))
	if receipt.JobID == "" {
		t.Fatal("reboot was not accepted")
	}
	if status, _ := f.get("owner-2", receipt.JobID); status != http.StatusNotFound {
		t.Fatalf("another owner status=%d, want 404", status)
	}
}

// A completed plan tells the client exactly what an update installs: exact
// counts, the hold scope, dpkg problems and a bounded package list that says
// when it was cut.
func TestServerMaintenancePlanResultListsBoundedPackages(t *testing.T) {
	f := newMaintenanceFixture(t, inertMaintenanceDispatcher{})
	plan := map[string]any{"action": "os_update_plan", "expected_inventory_revision": 7}
	status, receipt, reason := f.post(fullyAuthorizedCaller, "plan-1", plan)
	if status != http.StatusAccepted {
		t.Fatalf("plan status=%d reason=%q", status, reason)
	}
	packages := make([]servermaintenance.PlanPackage, 0, servermaintenance.MaxStoredPlanPackages+5)
	for i := 0; i < cap(packages); i++ {
		packages = append(packages, servermaintenance.PlanPackage{Name: fmt.Sprintf("pkg-%03d", i), From: "1.0", To: "1.1", Security: i == 0})
	}
	f.runOnce(scriptedMaintenanceDispatcher{plan: servermaintenance.PlanResult{
		PlanDigest: maintenanceTestPlanDigest, PendingCount: len(packages), SecurityCount: 1, RebootLikely: true,
		Held: []string{"containerd.io"}, HoldScope: "container runtime held", Packages: packages,
		DpkgProblems: []string{"libfoo is half-configured"},
	}}, "plan-1")

	status, job := f.get("owner-1", receipt.JobID)
	listed, _ := job.Result["packages"].([]any)
	first, _ := listed[0].(map[string]any)
	if status != http.StatusOK || job.State != controlplane.ServerMaintenanceStateCompleted ||
		job.Result["plan_digest"] != maintenanceTestPlanDigest || job.Result["pending_count"] != float64(len(packages)) ||
		job.Result["hold_scope"] != "container runtime held" || job.Result["packages_truncated"] != true ||
		len(listed) != servermaintenance.MaxStoredPlanPackages ||
		first["name"] != "pkg-000" || first["from"] != "1.0" || first["to"] != "1.1" || first["security"] != true {
		t.Fatalf("status=%d state=%s result=%v", status, job.State, job.Result)
	}
	if held, _ := job.Result["held"].([]any); len(held) != 1 {
		t.Fatalf("held=%v", job.Result["held"])
	}
	if problems, _ := job.Result["dpkg_problems"].([]any); len(problems) != 1 {
		t.Fatalf("dpkg_problems=%v", job.Result["dpkg_problems"])
	}
}
