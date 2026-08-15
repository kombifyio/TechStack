package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
)

// createTestOrchestrator creates an orchestrator with a mock PocketBaseApp.
func createTestOrchestrator(t *testing.T) (*Orchestrator, *MockPocketBaseApp) {
	t.Helper()
	mock := NewMockPocketBaseApp()
	log := logger.New("debug", "text")
	cfg := &Config{Workers: 2, WorkDir: t.TempDir(), LeaseLister: fakeManagedRuntimeLeaseLister{}}
	o := NewWithApp(mock, cfg, log)
	return o, mock
}

// setupMockCollections sets up standard collections in the mock.
func setupMockCollections(mock *MockPocketBaseApp) {
	mock.AddCollection("stacks", "col_stacks")
	mock.AddCollection("jobs", "col_jobs")
	mock.AddCollection("workers", "col_workers")
	mock.AddCollection("precheck_results", "col_precheck")
	mock.AddCollection("drift_results", "col_drift")
}

func installStackKitInventoryFilter(mock *MockPocketBaseApp) {
	mock.OnFindRecordsByFilter = func(collectionModelOrIdentifier any, _ string, _ string, limit int, _ int, params ...dbx.Params) ([]*core.Record, error) {
		colName := getCollectionName(collectionModelOrIdentifier)
		if colName != "nodes" && colName != "services" {
			return []*core.Record{}, nil
		}
		var param dbx.Params
		if len(params) > 0 {
			param = params[0]
		}
		matches := []*core.Record{}
		for _, record := range mock.GetSavedRecords(colName) {
			switch colName {
			case "nodes":
				if record.GetString("stack_id") != param["stackId"] || record.GetString("name") != param["name"] {
					continue
				}
			case "services":
				if record.GetString("node_id") != param["nodeId"] || record.GetString("name") != param["name"] {
					continue
				}
			}
			matches = append(matches, record)
			if limit > 0 && len(matches) >= limit {
				break
			}
		}
		return matches, nil
	}
}

func TestSyncPocketBaseJobPersistsWaitingAsHonestPendingProjection(t *testing.T) {
	orch, mock := createTestOrchestrator(t)
	defer orch.Stop()
	jobsCollection := mock.AddCollection("jobs", "col_jobs")
	record := core.NewRecord(jobsCollection) // pocketbase-migration-compat: verifies legacy wait projection
	record.Id = "record-waiting"
	record.Set("state", "running")
	mock.AddRecord("jobs", record)

	nextResumeAt := time.Now().UTC().Add(20 * time.Second)
	job := &jobs.Job{
		ID:           "job-waiting",
		State:        jobs.JobStateWaiting,
		WaitReason:   jobs.WaitReasonManagedRuntimeEnrollment,
		NextResumeAt: &nextResumeAt,
		Message:      "Managed VM is still enrolling; the next rollout check is scheduled.",
		Result:       map[string]interface{}{"lease_id": "lease-1"},
	}

	orch.syncPocketBaseJob(job, record.Id)

	stored, err := mock.FindRecordById("jobs", record.Id) // pocketbase-migration-compat: verifies legacy wait projection
	if err != nil {
		t.Fatal(err)
	}
	if stored.GetString("state") != string(jobs.JobStatePending) || stored.GetString("message") != job.Message || stored.GetString("error") != "" {
		t.Fatalf("PocketBase wait projection state=%q message=%q error=%q", stored.GetString("state"), stored.GetString("message"), stored.GetString("error"))
	}
	result, _ := stored.Get("result").(map[string]interface{})
	wait, _ := result["job_wait"].(map[string]interface{})
	if wait["state"] != string(jobs.JobStateWaiting) || wait["reason"] != jobs.WaitReasonManagedRuntimeEnrollment || wait["next_resume_at"] == "" {
		t.Fatalf("PocketBase job_wait result = %#v", wait)
	}
}

func TestUpdateStackStatus_ProjectsStackKitServicesToRuntimeInventory(t *testing.T) {
	o, mock := createTestOrchestrator(t)
	setupMockCollections(mock)
	mock.AddCollection("nodes", "col_nodes")
	mock.AddCollection("services", "col_services")
	installStackKitInventoryFilter(mock)

	stackRecord := core.NewRecord(mock.collections["stacks"])
	stackRecord.Id = "stack-runtime-services"
	stackRecord.Set("name", "Centron Coolify E2E")
	stackRecord.Set("owner_id", "owner-1")
	stackRecord.Set("tenant_id", "tenant-1")
	stackRecord.Set("status", "provisioning")
	mock.AddRecord("stacks", stackRecord)

	job := &jobs.Job{
		ID:       "job-runtime-services",
		Type:     jobs.JobTypeDeploy,
		State:    jobs.JobStateCompleted,
		TargetID: stackRecord.Id,
		Result: map[string]interface{}{
			"runtime_ssh_host":  "centron-vm.example.test",
			"runtime_public_ip": "203.0.113.20",
			"stackkit_outputs": map[string]interface{}{
				"services": []interface{}{
					map[string]interface{}{
						"name": "coolify",
						"url":  "https://coolify.centron.example.test",
						"metadata": map[string]interface{}{
							"type": "custom",
							"port": float64(8000),
						},
					},
					map[string]interface{}{
						"name": "monitoring",
						"url":  "https://status.centron.example.test",
					},
				},
			},
		},
	}

	o.updateStackStatus(job)

	nodes := mock.GetSavedRecords("nodes")
	if len(nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(nodes))
	}
	if nodes[0].GetString("stack_id") != stackRecord.Id || nodes[0].GetString("ip_address") != "203.0.113.20" {
		t.Fatalf("projected node = %+v", nodes[0])
	}

	services := mock.GetSavedRecords("services")
	if len(services) != 2 {
		t.Fatalf("services = %d, want 2", len(services))
	}
	byName := map[string]*core.Record{}
	for _, service := range services {
		byName[service.GetString("name")] = service
		if service.GetString("status") != "running" {
			t.Fatalf("service %s status = %q, want running", service.GetString("name"), service.GetString("status"))
		}
	}
	if byName["coolify"].GetString("url") != "https://coolify.centron.example.test" {
		t.Fatalf("coolify service = %+v", byName["coolify"])
	}
	if byName["coolify"].GetInt("port") != 8000 {
		t.Fatalf("coolify port = %d, want 8000", byName["coolify"].GetInt("port"))
	}
	if byName["monitoring"].GetString("type") != "monitoring" {
		t.Fatalf("monitoring service = %+v", byName["monitoring"])
	}
}

func TestUpdateStackStatus_ProjectsStackKitServiceLinks(t *testing.T) {
	o, mock := createTestOrchestrator(t)
	setupMockCollections(mock)
	mock.AddCollection("nodes", "col_nodes")
	mock.AddCollection("services", "col_services")
	installStackKitInventoryFilter(mock)

	stackRecord := core.NewRecord(mock.collections["stacks"])
	stackRecord.Id = "stack-service-links"
	stackRecord.Set("name", "IONOS Coolify E2E")
	stackRecord.Set("owner_id", "owner-1")
	stackRecord.Set("status", "provisioning")
	mock.AddRecord("stacks", stackRecord)

	job := &jobs.Job{
		ID:       "job-service-links",
		Type:     jobs.JobTypeDeploy,
		State:    jobs.JobStateCompleted,
		TargetID: stackRecord.Id,
		Result: map[string]interface{}{
			"runtime_public_ip": "198.51.100.44",
			"stackkit_outputs": map[string]interface{}{
				"service_links": []interface{}{
					map[string]interface{}{
						"name": "coolify",
						"url":  "https://coolify.kombify.pro",
					},
				},
			},
		},
	}

	o.updateStackStatus(job)

	services := mock.GetSavedRecords("services")
	if len(services) != 1 {
		t.Fatalf("services = %d, want 1", len(services))
	}
	if services[0].GetString("name") != "coolify" || !strings.Contains(services[0].GetString("url"), "kombify.pro") {
		t.Fatalf("projected service link = %+v", services[0])
	}
}

func TestUpdateStackStatus_ProjectsNodeButNotServiceForLoginGatewayOnly(t *testing.T) {
	o, mock := createTestOrchestrator(t)
	setupMockCollections(mock)
	mock.AddCollection("nodes", "col_nodes")
	mock.AddCollection("services", "col_services")
	installStackKitInventoryFilter(mock)

	stackRecord := core.NewRecord(mock.collections["stacks"])
	stackRecord.Id = "stack-login-only"
	stackRecord.Set("name", "Login Only")
	stackRecord.Set("owner_id", "owner-1")
	stackRecord.Set("status", "provisioning")
	mock.AddRecord("stacks", stackRecord)

	job := &jobs.Job{
		ID:       "job-login-only",
		Type:     jobs.JobTypeDeploy,
		State:    jobs.JobStateCompleted,
		TargetID: stackRecord.Id,
		Result: map[string]interface{}{
			"stackkit_outputs": map[string]interface{}{
				"login_gateway": map[string]interface{}{
					"url": "https://login.example.test",
				},
			},
		},
	}

	o.updateStackStatus(job)

	if services := mock.GetSavedRecords("services"); len(services) != 0 {
		t.Fatalf("services = %d, want 0 for login_gateway-only output", len(services))
	}
	if nodes := mock.GetSavedRecords("nodes"); len(nodes) != 1 {
		t.Fatalf("nodes = %d, want 1 for completed managed runtime projection", len(nodes))
	}
}

// TestCheckBlockingPreChecks_AllPassed tests when all pre-checks pass.
func TestCheckBlockingPreChecks_AllPassed(t *testing.T) {
	o, mock := createTestOrchestrator(t)
	defer o.Stop()
	setupMockCollections(mock)

	// No blocking pre-checks (empty result)
	mock.OnFindRecordsByFilter = func(col any, filter, sort string, limit, offset int, params ...dbx.Params) ([]*core.Record, error) {
		return []*core.Record{}, nil
	}

	err := o.CheckBlockingPreChecks("stack-123")
	if err != nil {
		t.Errorf("Expected no error when all checks pass, got: %v", err)
	}
}

// TestCheckBlockingPreChecks_SomeFailed tests when some pre-checks fail.
func TestCheckBlockingPreChecks_SomeFailed(t *testing.T) {
	o, mock := createTestOrchestrator(t)
	defer o.Stop()
	setupMockCollections(mock)

	// Create a mock failed precheck record
	col := mock.collections["precheck_results"]
	failedRecord := core.NewRecord(col)
	failedRecord.Id = "precheck-1"
	failedRecord.Set("worker_id", "worker-1")
	failedRecord.Set("check_type", "blocking")
	failedRecord.Set("status", "failed")
	failedRecord.Set("message", "SSH connection failed")

	mock.OnFindRecordsByFilter = func(colArg any, filter, sort string, limit, offset int, params ...dbx.Params) ([]*core.Record, error) {
		colName := getCollectionName(colArg)
		if colName == "precheck_results" {
			return []*core.Record{failedRecord}, nil
		}
		return nil, nil
	}

	err := o.CheckBlockingPreChecks("stack-123")
	if err == nil {
		t.Error("Expected error when pre-checks fail")
	}
	if !errors.Is(err, ErrBlockingPreChecksFailed) {
		t.Errorf("Expected ErrBlockingPreChecksFailed, got: %v", err)
	}
}

// TestCheckBlockingPreChecks_QueryError tests handling of query errors.
func TestCheckBlockingPreChecks_QueryError(t *testing.T) {
	o, mock := createTestOrchestrator(t)
	defer o.Stop()

	// Simulate query error (collection doesn't exist)
	mock.FindRecordsByFilterError = errors.New("collection not found")

	// Should return nil (treat as no checks configured)
	err := o.CheckBlockingPreChecks("stack-123")
	if err != nil {
		t.Errorf("Expected nil error on query failure (graceful handling), got: %v", err)
	}
}

// TestProvisionStack_StackNotFound tests error when stack doesn't exist.
func TestProvisionStack_StackNotFound(t *testing.T) {
	o, mock := createTestOrchestrator(t)
	defer o.Stop()
	setupMockCollections(mock)

	// No stack in storage
	_, err := o.ProvisionStack("nonexistent-stack", nil)
	if err == nil {
		t.Error("Expected error for nonexistent stack")
	}
}

// TestProvisionStack_AlreadyRunning tests error when stack is already running.
func TestProvisionStack_AlreadyRunning(t *testing.T) {
	o, mock := createTestOrchestrator(t)
	defer o.Stop()
	setupMockCollections(mock)

	// Create a running stack
	stacksCol := mock.collections["stacks"]
	stackRecord := core.NewRecord(stacksCol)
	stackRecord.Id = "stack-running"
	stackRecord.Set("name", "RunningStack")
	stackRecord.Set("status", "running")
	mock.AddRecord("stacks", stackRecord)

	_, err := o.ProvisionStack("stack-running", nil)
	if err == nil {
		t.Error("Expected error for already running stack")
	}
}

// TestProvisionStack_BlockedByPreChecks tests pre-check blocking.
func TestProvisionStack_BlockedByPreChecks(t *testing.T) {
	o, mock := createTestOrchestrator(t)
	defer o.Stop()
	setupMockCollections(mock)

	// Create a pending stack
	stacksCol := mock.collections["stacks"]
	stackRecord := core.NewRecord(stacksCol)
	stackRecord.Id = "stack-blocked"
	stackRecord.Set("name", "BlockedStack")
	stackRecord.Set("status", "pending")
	mock.AddRecord("stacks", stackRecord)

	// Mock failed pre-check
	precheckCol := mock.collections["precheck_results"]
	failedCheck := core.NewRecord(precheckCol)
	failedCheck.Id = "precheck-fail"
	failedCheck.Set("stack_id", "stack-blocked")
	failedCheck.Set("check_type", "blocking")
	failedCheck.Set("status", "failed")
	failedCheck.Set("worker_id", "worker-1")
	failedCheck.Set("message", "Pre-check failed")

	mock.OnFindRecordsByFilter = func(colArg any, filter, sort string, limit, offset int, params ...dbx.Params) ([]*core.Record, error) {
		colName := getCollectionName(colArg)
		if colName == "precheck_results" {
			return []*core.Record{failedCheck}, nil
		}
		return []*core.Record{}, nil
	}

	_, err := o.ProvisionStack("stack-blocked", nil)
	if err == nil {
		t.Error("Expected error when pre-checks fail")
	}
	if !errors.Is(err, ErrBlockingPreChecksFailed) {
		t.Errorf("Expected ErrBlockingPreChecksFailed, got: %v", err)
	}
}

// createTestOrchestratorWithWorkerStore mirrors createTestOrchestrator but
// wires a control-plane memory worker store: worker loading is Postgres-only
// since the PocketBase branch was removed (PB retirement).
func createTestOrchestratorWithWorkerStore(t *testing.T) (*Orchestrator, *MockPocketBaseApp, *controlplane.MemoryStore) {
	t.Helper()
	mock := NewMockPocketBaseApp()
	log := logger.New("debug", "text")
	store := controlplane.NewMemoryStore()
	cfg := &Config{Workers: 2, WorkDir: t.TempDir(), WorkerStore: store, LeaseLister: fakeManagedRuntimeLeaseLister{}}
	o := NewWithApp(mock, cfg, log)
	return o, mock, store
}

// seedApprovedStoreWorker registers an approved, stack-assigned worker in the
// control-plane store.
func seedApprovedStoreWorker(t *testing.T, store *controlplane.MemoryStore, id, tenantID, ownerID, stackID string) {
	t.Helper()
	worker := seedStoreWorkerWithStatus(t, store, id, tenantID, ownerID, stackID, "approved", true)
	seedDeployEligibleServerRuntime(t, store, worker, time.Now().UTC())
}

func seedStoreWorkerWithStatus(t *testing.T, store *controlplane.MemoryStore, id, tenantID, ownerID, stackID, status string, approved bool) controlplane.Worker {
	t.Helper()
	now := time.Now().UTC()
	worker := controlplane.Worker{
		ID:             id,
		TenantID:       tenantID,
		StackID:        stackID,
		Hostname:       id + "-host",
		OS:             "linux",
		Arch:           "amd64",
		Status:         status,
		Approved:       approved,
		LastSeenAt:     &now,
		CPUCores:       8,
		RAMMB:          16384,
		OwnerSubjectID: ownerID,
	}
	if approved {
		worker.ApprovedAt = &now
	}
	if _, err := store.UpsertWorkerHeartbeat(context.Background(), worker); err != nil {
		t.Fatalf("seed worker: %v", err)
	}
	return worker
}

func seedDeployEligibleServerRuntime(t *testing.T, store *controlplane.MemoryStore, worker controlplane.Worker, heartbeatAt time.Time) {
	t.Helper()
	_, err := store.UpsertServerRuntime(context.Background(), controlplane.ServerRuntime{
		ID:              "server-" + worker.ID,
		TenantID:        worker.TenantID,
		StackID:         worker.StackID,
		OwnerSubjectID:  worker.OwnerSubjectID,
		WorkerID:        worker.ID,
		Name:            worker.Hostname,
		LifecycleState:  "active",
		ConnectionState: "connected",
		HealthState:     "healthy",
		LastHeartbeatAt: &heartbeatAt,
	})
	if err != nil {
		t.Fatalf("seed canonical server runtime: %v", err)
	}
}

func seedManagedDeployEligibleServerRuntime(
	t *testing.T,
	store *controlplane.MemoryStore,
	tenantID, ownerID, stackID, leaseID string,
	heartbeatAt time.Time,
) {
	t.Helper()
	_, err := store.UpsertServerRuntime(context.Background(), controlplane.ServerRuntime{
		ID:              runtimeidentity.LeaseServerID(leaseID),
		TenantID:        tenantID,
		StackID:         stackID,
		OwnerSubjectID:  ownerID,
		WorkerID:        "guard-" + leaseID,
		LeaseID:         leaseID,
		Name:            "managed-" + leaseID,
		LifecycleState:  "active",
		ConnectionState: "connected",
		HealthState:     "healthy",
		LastHeartbeatAt: &heartbeatAt,
	})
	if err != nil {
		t.Fatalf("seed managed canonical server runtime: %v", err)
	}
}

func TestWorkerIsDeployCandidate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   string
		approved bool
		want     bool
	}{
		{name: "approved assignment", status: "approved", approved: true, want: true},
		{name: "connected legacy projection", status: "connected", approved: true, want: true},
		{name: "pending", status: "pending", approved: true},
		{name: "not approved", status: "connected"},
		{name: "unknown status", approved: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			worker := controlplane.Worker{Status: tc.status, Approved: tc.approved}
			if got := workerIsDeployCandidate(worker); got != tc.want {
				t.Fatalf("workerIsDeployCandidate() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestServerRuntimeAllowsWorkerDeploy(t *testing.T) {
	now := time.Now().UTC()
	worker := controlplane.Worker{
		ID: "worker-1", TenantID: "tenant-1", StackID: "stack-1",
		OwnerSubjectID: "owner-1", Status: "approved", Approved: true,
	}
	base := controlplane.ServerRuntime{
		ID: "server-1", TenantID: worker.TenantID, StackID: worker.StackID,
		OwnerSubjectID: worker.OwnerSubjectID, WorkerID: worker.ID,
		LifecycleState: "active", ConnectionState: "connected", HealthState: "healthy",
	}
	for _, tc := range []struct {
		name   string
		mutate func(*controlplane.Worker, *controlplane.ServerRuntime)
		age    time.Duration
		seen   bool
		want   bool
	}{
		{name: "fresh healthy", age: runtimehealth.FreshHeartbeatWindow, seen: true, want: true},
		{name: "fresh degraded", age: time.Second, seen: true, want: true, mutate: func(_ *controlplane.Worker, runtime *controlplane.ServerRuntime) {
			runtime.ConnectionState, runtime.HealthState = "degraded", "degraded"
		}},
		{name: "approval without heartbeat", seen: false},
		{name: "stale heartbeat", age: runtimehealth.FreshHeartbeatWindow + time.Second, seen: true},
		{name: "future heartbeat", age: -31 * time.Second, seen: true},
		{name: "inactive lifecycle", age: time.Second, seen: true, mutate: func(_ *controlplane.Worker, runtime *controlplane.ServerRuntime) {
			runtime.LifecycleState = "enrolling"
		}},
		{name: "pending connection", age: time.Second, seen: true, mutate: func(_ *controlplane.Worker, runtime *controlplane.ServerRuntime) {
			runtime.ConnectionState = "pending"
		}},
		{name: "unknown health", age: time.Second, seen: true, mutate: func(_ *controlplane.Worker, runtime *controlplane.ServerRuntime) {
			runtime.HealthState = "unknown"
		}},
		{name: "wrong worker binding", age: time.Second, seen: true, mutate: func(_ *controlplane.Worker, runtime *controlplane.ServerRuntime) {
			runtime.WorkerID = "worker-other"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate, runtime := worker, base
			if tc.seen {
				heartbeatAt := now.Add(-tc.age)
				runtime.LastHeartbeatAt = &heartbeatAt
			}
			if tc.mutate != nil {
				tc.mutate(&candidate, &runtime)
			}
			if got := serverRuntimeAllowsWorkerDeploy(candidate, runtime, now); got != tc.want {
				t.Fatalf("serverRuntimeAllowsWorkerDeploy() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDeployStackRejectsApprovedWorkerWithoutCanonicalGuardHeartbeat(t *testing.T) {
	o, mock, store := createTestOrchestratorWithWorkerStore(t)
	defer o.Stop()
	setupMockCollections(mock)

	stackRecord := core.NewRecord(mock.collections["stacks"])
	stackRecord.Id = "stack-approved-only"
	stackRecord.Set("name", "ApprovedOnly")
	stackRecord.Set("status", "pending")
	stackRecord.Set("owner_id", "owner-1")
	stackRecord.Set("tenant_id", "tenant-1")
	mock.AddRecord("stacks", stackRecord)
	seedStoreWorkerWithStatus(t, store, "worker-approved-only", "tenant-1", "owner-1", "stack-approved-only", "approved", true)

	jobID, err := o.DeployStack("stack-approved-only")
	if !errors.Is(err, ErrNoAssignedWorkers) {
		t.Fatalf("DeployStack error = %v, want ErrNoAssignedWorkers", err)
	}
	if jobID != "" {
		t.Fatalf("jobID = %q, want no deploy job", jobID)
	}
}

// TestDeployStack_ConnectedWorkerRemainsEligible proves the heartbeat-transition
// fix: a BYOS worker that has phoned home (Status="connected") still satisfies
// the deploy filter and does not trip ErrNoAssignedWorkers.
func TestDeployStack_ConnectedWorkerRemainsEligible(t *testing.T) {
	o, mock, store := createTestOrchestratorWithWorkerStore(t)
	defer o.Stop()
	setupMockCollections(mock)

	stacksCol := mock.collections["stacks"]
	stackRecord := core.NewRecord(stacksCol)
	stackRecord.Id = "stack-connected"
	stackRecord.Set("name", "ConnectedStack")
	stackRecord.Set("status", "pending")
	stackRecord.Set("owner_id", "owner-c")
	stackRecord.Set("tenant_id", "tenant-c")
	mock.AddRecord("stacks", stackRecord)

	worker := seedStoreWorkerWithStatus(t, store, "worker-connected", "tenant-c", "owner-c", "stack-connected", "connected", true)
	seedDeployEligibleServerRuntime(t, store, worker, time.Now().UTC())

	o.Start()
	jobID, err := o.DeployStack("stack-connected")
	if err != nil {
		t.Fatalf("DeployStack with a connected worker must succeed, got %v", err)
	}
	if jobID == "" {
		t.Fatal("expected a job id for a connected-worker deploy")
	}
}

// TestDeployStack_Success tests successful deployment.
func TestDeployStack_Success(t *testing.T) {
	o, mock, store := createTestOrchestratorWithWorkerStore(t)
	defer o.Stop()
	setupMockCollections(mock)

	// Create a stack ready for deployment
	stacksCol := mock.collections["stacks"]
	stackRecord := core.NewRecord(stacksCol)
	stackRecord.Id = "stack-deploy"
	stackRecord.Set("name", "DeployStack")
	stackRecord.Set("status", "pending")
	stackRecord.Set("owner_id", "user-123")
	stackRecord.Set("tenant_id", "tenant-deploy")
	mock.AddRecord("stacks", stackRecord)

	seedApprovedStoreWorker(t, store, "worker-deploy", "tenant-deploy", "user-123", "stack-deploy")

	o.Start()

	jobID, err := o.DeployStack("stack-deploy")
	if err != nil {
		t.Fatalf("DeployStack failed: %v", err)
	}

	if jobID == "" {
		t.Error("Expected job ID to be returned")
	}

	savedJob := mock.records["jobs"][jobID]
	if savedJob == nil {
		t.Fatal("expected saved job record")
	}
	if got := savedJob.GetString("type"); got != "update" {
		t.Fatalf("expected persisted job type update, got %q", got)
	}
	if got := savedJob.GetString("stack_id"); got != "stack-deploy" {
		t.Fatalf("expected persisted stack_id stack-deploy, got %q", got)
	}
}

// TestDeployStack_WithApprovedWorkers tests deployment with approved workers.
func TestDeployStack_WithApprovedWorkers(t *testing.T) {
	o, mock, store := createTestOrchestratorWithWorkerStore(t)
	defer o.Stop()
	setupMockCollections(mock)

	// Create a stack
	stacksCol := mock.collections["stacks"]
	stackRecord := core.NewRecord(stacksCol)
	stackRecord.Id = "stack-with-workers"
	stackRecord.Set("name", "StackWithWorkers")
	stackRecord.Set("status", "pending")
	stackRecord.Set("owner_id", "owner-1")
	stackRecord.Set("tenant_id", "tenant-1")
	mock.AddRecord("stacks", stackRecord)

	seedApprovedStoreWorker(t, store, "worker-1", "tenant-1", "owner-1", "stack-with-workers")

	o.Start()

	jobID, err := o.DeployStack("stack-with-workers")
	if err != nil {
		t.Fatalf("DeployStack failed: %v", err)
	}

	if jobID == "" {
		t.Error("Expected job ID")
	}

	savedJob := mock.records["jobs"][jobID]
	if savedJob == nil {
		t.Fatal("expected saved job record")
	}
	if got := savedJob.GetString("type"); got != "update" {
		t.Fatalf("expected persisted job type update, got %q", got)
	}
	if got := savedJob.GetString("stack_id"); got != "stack-with-workers" {
		t.Fatalf("expected persisted stack_id stack-with-workers, got %q", got)
	}
}

func TestDeployStackRequiresAssignedWorker(t *testing.T) {
	o, mock, _ := createTestOrchestratorWithWorkerStore(t)
	defer o.Stop()
	setupMockCollections(mock)

	stacksCol := mock.collections["stacks"]
	stackRecord := core.NewRecord(stacksCol)
	stackRecord.Id = "stack-no-assignment"
	stackRecord.Set("name", "StackNoAssignment")
	stackRecord.Set("status", "pending")
	stackRecord.Set("owner_id", "owner-1")
	stackRecord.Set("tenant_id", "tenant-1")
	mock.AddRecord("stacks", stackRecord)

	workersCol := mock.collections["workers"]
	workerRecord := core.NewRecord(workersCol)
	workerRecord.Id = "worker-unassigned"
	workerRecord.Set("hostname", "legacy-node")
	workerRecord.Set("owner_id", "owner-1")
	workerRecord.Set("approved", true)
	workerRecord.Set("status", "approved")
	mock.AddRecord("workers", workerRecord)
	mock.OnFindRecordsByFilter = func(colArg any, filter, sort string, limit, offset int, params ...dbx.Params) ([]*core.Record, error) {
		colName := getCollectionName(colArg)
		if colName == "workers" {
			return []*core.Record{workerRecord}, nil
		}
		return []*core.Record{}, nil
	}

	_, err := o.DeployStack("stack-no-assignment")
	if !errors.Is(err, ErrNoAssignedWorkers) {
		t.Fatalf("DeployStack error = %v, want ErrNoAssignedWorkers", err)
	}
	if got := stackRecord.GetString("status"); got != "pending" {
		t.Fatalf("stack status = %q, want pending", got)
	}
}

func TestDeployStackRejectsManagedStackRecordWithoutNativeLease(t *testing.T) {
	o, mock := createTestOrchestrator(t)
	defer o.Stop()
	setupMockCollections(mock)

	stacksCol := mock.collections["stacks"]
	stackRecord := core.NewRecord(stacksCol)
	stackRecord.Id = "stack-managed-retry"
	stackRecord.Set("name", "ManagedRetry")
	stackRecord.Set("status", "pending")
	stackRecord.Set("owner_id", "owner-1")
	stackRecord.Set("runtime_lane", "monthly-runtime")
	stackRecord.Set("server_provisioning_mode", "kombify-cloud")
	stackRecord.Set("server_connection_mode", "managed-subscription")
	stackRecord.Set("lease_provider", "centron-managed")
	stackRecord.Set("runtime_offering_id", "monthly-runtime-premium")
	stackRecord.Set("lease_id", "lease-managed-retry")
	mock.AddRecord("stacks", stackRecord)

	mock.OnFindRecordsByFilter = func(colArg any, filter, sort string, limit, offset int, params ...dbx.Params) ([]*core.Record, error) {
		colName := getCollectionName(colArg)
		if colName == "workers" {
			return []*core.Record{}, nil
		}
		return []*core.Record{}, nil
	}

	o.Start()

	jobID, err := o.DeployStack("stack-managed-retry")
	if !errors.Is(err, ErrManagedRuntimeLeaseNotNativeActive) {
		t.Fatalf("DeployStack error = %v, want native-active authority failure", err)
	}
	if jobID != "" {
		t.Fatalf("jobID = %q, want no unsafe deploy job", jobID)
	}
}

// TestDestroyStack_Success tests successful destruction.
func TestDestroyStack_Success(t *testing.T) {
	o, mock := createTestOrchestrator(t)
	defer o.Stop()
	setupMockCollections(mock)

	// Create a running stack
	stacksCol := mock.collections["stacks"]
	stackRecord := core.NewRecord(stacksCol)
	stackRecord.Id = "stack-destroy"
	stackRecord.Set("name", "DestroyStack")
	stackRecord.Set("status", "running")
	stackRecord.Set("owner_id", "owner-destroy")
	stackRecord.Set("tenant_id", "tenant-destroy")
	mock.AddRecord("stacks", stackRecord)

	o.Start()

	jobID, err := o.DestroyStack("stack-destroy")
	if err != nil {
		t.Fatalf("DestroyStack failed: %v", err)
	}

	if jobID == "" {
		t.Error("Expected job ID to be returned")
	}

	// Verify stack status was updated to "stopping"
	savedStack := mock.records["stacks"]["stack-destroy"]
	if savedStack.GetString("status") != "stopping" {
		t.Errorf("Expected status 'stopping', got %q", savedStack.GetString("status"))
	}
}

// TestDestroyStack_StackNotFound tests error when stack doesn't exist.
func TestDestroyStack_StackNotFound(t *testing.T) {
	o, mock := createTestOrchestrator(t)
	defer o.Stop()
	setupMockCollections(mock)

	_, err := o.DestroyStack("nonexistent")
	if err == nil {
		t.Error("Expected error for nonexistent stack")
	}
}

// TestTriggerDriftCheck_Success tests drift detection triggering.
func TestTriggerDriftCheck_Success(t *testing.T) {
	o, mock := createTestOrchestrator(t)
	defer o.Stop()
	setupMockCollections(mock)

	// Create a running stack
	stacksCol := mock.collections["stacks"]
	stackRecord := core.NewRecord(stacksCol)
	stackRecord.Id = "stack-drift"
	stackRecord.Set("name", "DriftStack")
	stackRecord.Set("status", "running")
	mock.AddRecord("stacks", stackRecord)

	o.Start()

	jobID, err := o.TriggerDriftCheck("stack-drift", "manual")
	if err != nil {
		t.Fatalf("TriggerDriftCheck failed: %v", err)
	}

	if jobID == "" {
		t.Error("Expected job ID")
	}

	savedJob := mock.records["jobs"][jobID]
	if savedJob == nil {
		t.Fatal("expected saved job record")
	}
	if got := savedJob.GetString("type"); got != "update" {
		t.Fatalf("expected persisted job type update, got %q", got)
	}
	if got := savedJob.GetString("stack_id"); got != "stack-drift" {
		t.Fatalf("expected persisted stack_id stack-drift, got %q", got)
	}
}

// TestTriggerDriftResolve_Success tests drift resolution triggering.
func TestTriggerDriftResolve_Success(t *testing.T) {
	o, mock := createTestOrchestrator(t)
	defer o.Stop()
	setupMockCollections(mock)

	// Create a stack with drift
	stacksCol := mock.collections["stacks"]
	stackRecord := core.NewRecord(stacksCol)
	stackRecord.Id = "stack-resolve"
	stackRecord.Set("name", "ResolveStack")
	stackRecord.Set("status", "running")
	stackRecord.Set("drift_status", "drifted")
	mock.AddRecord("stacks", stackRecord)

	o.Start()

	jobID, err := o.TriggerDriftResolve("stack-resolve")
	if err != nil {
		t.Fatalf("TriggerDriftResolve failed: %v", err)
	}

	if jobID == "" {
		t.Error("Expected job ID")
	}

	savedJob := mock.records["jobs"][jobID]
	if savedJob == nil {
		t.Fatal("expected saved job record")
	}
	if got := savedJob.GetString("type"); got != "restart" {
		t.Fatalf("expected persisted job type restart, got %q", got)
	}
	if got := savedJob.GetString("stack_id"); got != "stack-resolve" {
		t.Fatalf("expected persisted stack_id stack-resolve, got %q", got)
	}
}

// TestGetJobStatus_Found tests retrieving an existing job.
func TestGetJobStatus_Found(t *testing.T) {
	o, mock := createTestOrchestrator(t)
	defer o.Stop()
	setupMockCollections(mock)

	// Create a stack and enqueue a job
	stacksCol := mock.collections["stacks"]
	stackRecord := core.NewRecord(stacksCol)
	stackRecord.Id = "stack-status"
	stackRecord.Set("name", "StatusStack")
	stackRecord.Set("status", "pending")
	mock.AddRecord("stacks", stackRecord)

	o.Start()

	jobID, err := o.ProvisionStack("stack-status", nil)
	if err != nil {
		t.Fatalf("ProvisionStack failed: %v", err)
	}

	// Get job status
	job, err := o.GetJobStatus(jobID)
	if err != nil {
		t.Fatalf("GetJobStatus failed: %v", err)
	}

	if job.ID != jobID {
		t.Errorf("Expected job ID %q, got %q", jobID, job.ID)
	}
	if job.Type != jobs.JobTypeProvision {
		t.Errorf("Expected job type %q, got %q", jobs.JobTypeProvision, job.Type)
	}
}

// TestGetJobStatus_NotFound tests error for nonexistent job.
func TestGetJobStatus_NotFound(t *testing.T) {
	o, _ := createTestOrchestrator(t)
	defer o.Stop()

	_, err := o.GetJobStatus("nonexistent-job")
	if err == nil {
		t.Error("Expected error for nonexistent job")
	}
}

func TestUpdateStackStatusCopiesRuntimeAndProvisioningFields(t *testing.T) {
	o, mock := createTestOrchestrator(t)
	defer o.Stop()
	setupMockCollections(mock)

	stacksCol := mock.collections["stacks"]
	stackRecord := core.NewRecord(stacksCol)
	stackRecord.Id = "stack-runtime-fields"
	stackRecord.Set("name", "RuntimeStack")
	stackRecord.Set("status", "provisioning")
	mock.AddRecord("stacks", stackRecord)

	job := &jobs.Job{
		ID:       "job-runtime-fields",
		Type:     jobs.JobTypeProvision,
		State:    jobs.JobStateCompleted,
		TargetID: "stack-runtime-fields",
		Result: map[string]interface{}{
			"runtime_phase":                   "lease_ready",
			"server_mode":                     "monthly-runtime",
			"runtime_lane":                    "monthly-runtime",
			"runtime_offering_id":             "monthly-runtime-standard",
			"lease_provider":                  "centron-managed",
			"provider_region":                 "de/fra",
			"ionos_datacenter":                "de/fra",
			"lease_id":                        "lease-test",
			"simulate_provider_id":            "centron-managed",
			"simulate_node_lifecycle":         "pvm",
			"desired_state":                   "running",
			"billing_mode":                    "subscription",
			"billing_cadence":                 "monthly",
			"stackkit_catalog_ref":            "basement-kit",
			"verification_status":             "pending",
			"server_provisioning_mode":        "kombify-cloud",
			"server_connection_mode":          "managed-subscription",
			"server_remote_host_present":      true,
			"server_remote_user_present":      true,
			"server_remote_auth_method":       "ssh-key",
			"server_remote_credential_ref":    "root@server",
			"server_remote_use_sudo":          true,
			"server_install_command_required": false,
		},
	}

	o.updateStackStatus(job)

	updated := mock.records["stacks"]["stack-runtime-fields"]
	for key, want := range map[string]any{
		"runtime_lane":                    "monthly-runtime",
		"runtime_offering_id":             "monthly-runtime-standard",
		"provider_region":                 "de/fra",
		"ionos_datacenter":                "de/fra",
		"simulate_provider_id":            "centron-managed",
		"simulate_node_lifecycle":         "pvm",
		"billing_cadence":                 "monthly",
		"server_provisioning_mode":        "kombify-cloud",
		"server_connection_mode":          "managed-subscription",
		"server_remote_host_present":      true,
		"server_remote_user_present":      true,
		"server_remote_auth_method":       "ssh-key",
		"server_remote_credential_ref":    "root@server",
		"server_remote_use_sudo":          true,
		"server_install_command_required": false,
	} {
		if got := updated.Get(key); got != want {
			t.Fatalf("stack field %q = %v, want %v", key, got, want)
		}
	}
}

func TestUpdateStackStatusPersistsOneLinerPairingToken(t *testing.T) {
	o, mock := createTestOrchestrator(t)
	defer o.Stop()
	setupMockCollections(mock)
	mock.AddCollection("pairing_tokens", "col_pairing_tokens")

	stacksCol := mock.collections["stacks"]
	stackRecord := core.NewRecord(stacksCol)
	stackRecord.Id = "stack-oneliner"
	stackRecord.Set("name", "OneLinerStack")
	stackRecord.Set("owner_id", "owner-1")
	stackRecord.Set("status", "provisioning")
	mock.AddRecord("stacks", stackRecord)

	const enrollmentValue = "ks_test_registration_value"
	job := &jobs.Job{
		ID:       "job-oneliner",
		Type:     jobs.JobTypeProvision,
		State:    jobs.JobStateCompleted,
		TargetID: "stack-oneliner",
		Result: map[string]interface{}{
			"registration_token":              enrollmentValue,
			"server_provisioning_mode":        "install-command",
			"server_connection_mode":          "agent-oneliner",
			"server_install_command_required": true,
			"server_mode":                     "user-owned",
			"runtime_phase":                   "prepared",
			"desired_state":                   "running",
			"stackkit_catalog_ref":            "basement-kit",
			"verification_status":             "pending",
		},
	}

	o.updateStackStatus(job)

	tokens := mock.GetSavedRecords("pairing_tokens")
	if len(tokens) != 1 {
		t.Fatalf("pairing token count = %d, want 1", len(tokens))
	}
	hash := sha256.Sum256([]byte(enrollmentValue))
	if got, want := tokens[0].GetString("token_hash"), hex.EncodeToString(hash[:]); got != want {
		t.Fatalf("token_hash = %q, want %q", got, want)
	}
	if got := tokens[0].GetString("user"); got != "owner-1" {
		t.Fatalf("user = %q, want owner-1", got)
	}
	if tokens[0].GetBool("used") {
		t.Fatal("pairing token should not be marked used before worker registration")
	}
}

func TestUpdateStackStatusSyncsStackKitOutputsToWallet(t *testing.T) {
	o, mock := createTestOrchestrator(t)
	defer o.Stop()
	setupMockCollections(mock)
	walletCol := mock.AddCollection("wallet", "col_wallet")
	walletCol.Fields.Add(
		&core.TextField{Name: "owner_id"},
		&core.TextField{Name: "stack_id"},
		&core.TextField{Name: "service_id"},
		&core.TextField{Name: "source_ref"},
		&core.TextField{Name: "name"},
		&core.SelectField{Name: "kind", Values: []string{"other"}},
		&core.TextField{Name: "username"},
		&core.TextField{Name: "secret"},
		&core.TextField{Name: "url"},
		&core.TextField{Name: "notes"},
		&core.SelectField{Name: "item_class", Values: []string{"launch", "user_account", "recovery"}},
		&core.TextField{Name: "source_type"},
		&core.SelectField{Name: "access_mode", Values: []string{"open", "reveal", "manage"}},
		&core.BoolField{Name: "revealable"},
		&core.BoolField{Name: "auto_generated"},
	)

	mock.OnFindRecordsByFilter = func(collectionModelOrIdentifier any, _ string, _ string, limit int, _ int, params ...dbx.Params) ([]*core.Record, error) {
		if getCollectionName(collectionModelOrIdentifier) != "wallet" || len(params) == 0 {
			return []*core.Record{}, nil
		}
		p := params[0]
		matches := []*core.Record{}
		for _, record := range mock.records["wallet"] {
			if record.GetString("owner_id") == p["ownerID"] &&
				record.GetString("stack_id") == p["stackID"] &&
				record.GetString("service_id") == p["serviceID"] &&
				record.GetString("source_ref") == p["sourceRef"] {
				matches = append(matches, record)
				if limit > 0 && len(matches) >= limit {
					break
				}
			}
		}
		return matches, nil
	}

	stacksCol := mock.collections["stacks"]
	stackRecord := core.NewRecord(stacksCol)
	stackRecord.Id = "stack-wallet-sync"
	stackRecord.Set("name", "Wallet Sync Stack")
	stackRecord.Set("owner_id", "owner-1")
	stackRecord.Set("status", "provisioning")
	mock.AddRecord("stacks", stackRecord)

	job := &jobs.Job{
		ID:       "job-wallet-sync",
		Type:     jobs.JobTypeDeploy,
		State:    jobs.JobStateCompleted,
		TargetID: "stack-wallet-sync",
		Result: map[string]interface{}{
			"stackkit_outputs": map[string]interface{}{
				"identity": map[string]interface{}{
					"owner": map[string]interface{}{
						"email":      "owner@example.com",
						"manage_url": "https://id.example.com/admin",
					},
				},
				"login_gateway": map[string]interface{}{
					"url": "https://login.example.com",
				},
				"recovery": map[string]interface{}{
					"bundle_ref": "secret://stack/recovery-bundle",
				},
			},
		},
	}

	o.updateStackStatus(job)

	walletRecords := mock.GetSavedRecords("wallet")
	if len(walletRecords) != 3 {
		t.Fatalf("wallet records = %d, want 3", len(walletRecords))
	}
	byService := map[string]*core.Record{}
	for _, record := range walletRecords {
		byService[record.GetString("service_id")] = record
	}
	if byService["pocketid:owner"].GetString("access_mode") != "manage" {
		t.Fatalf("owner wallet item = %+v", byService["pocketid:owner"])
	}
	if byService["stackkit:login-gateway"].GetString("url") != "https://login.example.com" {
		t.Fatalf("login gateway wallet item = %+v", byService["stackkit:login-gateway"])
	}
	if byService["stackkit:recovery"].GetString("secret") == "" || !byService["stackkit:recovery"].GetBool("revealable") {
		t.Fatalf("recovery wallet item = %+v", byService["stackkit:recovery"])
	}
}

// TestConcurrentOperations tests thread safety.
func TestConcurrentOperations(t *testing.T) {
	o, mock := createTestOrchestrator(t)
	defer o.Stop()
	setupMockCollections(mock)

	// Create a stack
	stacksCol := mock.collections["stacks"]
	for i := 0; i < 10; i++ {
		rec := core.NewRecord(stacksCol)
		rec.Id = "stack-concurrent-" + string(rune('a'+i))
		rec.Set("name", "ConcurrentStack")
		rec.Set("status", "pending")
		mock.AddRecord("stacks", rec)
	}

	o.Start()

	// Concurrent operations
	done := make(chan bool, 20)
	for i := 0; i < 10; i++ {
		stackID := "stack-concurrent-" + string(rune('a'+i))
		go func(id string) {
			_, _ = o.ProvisionStack(id, nil)
			done <- true
		}(stackID)
		go func() {
			_ = o.GetQueueStats()
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 20; i++ {
		<-done
	}
}

// TestProvisionStack_SaveError tests error handling when save fails.
func TestProvisionStack_JobsCollectionMissing(t *testing.T) {
	o, mock := createTestOrchestrator(t)
	defer o.Stop()

	// Only add stacks collection, not jobs
	stacksCol := mock.AddCollection("stacks", "col_stacks")
	stackRecord := core.NewRecord(stacksCol)
	stackRecord.Id = "stack-no-jobs-col"
	stackRecord.Set("name", "TestStack")
	stackRecord.Set("status", "pending")
	mock.AddRecord("stacks", stackRecord)

	_, err := o.ProvisionStack("stack-no-jobs-col", nil)
	if err == nil {
		t.Error("Expected error when jobs collection is missing")
	}
}
