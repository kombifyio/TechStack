package orchestrator

import (
	"context"
	"errors"
	"maps"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/nodehandoff"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/stackrouting"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

type missingPocketBaseApp struct{}

type requestContextKey struct{}

type contextCapturingStore struct {
	*controlplane.MemoryStore
	getStackContext           context.Context
	upsertJobContext          context.Context
	updateStackRuntimeContext context.Context
}

type finalSyncJobStore struct {
	*controlplane.MemoryStore
	onSync func(controlplane.SyncJobSnapshotRequest)
}

type delayedStaleConflictJobStore struct {
	*controlplane.MemoryStore
	delayed          atomic.Bool
	waitingPersisted chan struct{}
	releaseResponse  chan struct{}
}

func (s *delayedStaleConflictJobStore) SyncJobSnapshot(
	ctx context.Context,
	req controlplane.SyncJobSnapshotRequest,
) (*controlplane.Job, error) {
	if req.ObservedState == string(jobs.JobStateWaiting) && s.delayed.CompareAndSwap(false, true) {
		if _, err := s.MemoryStore.SyncJobSnapshot(ctx, req); err != nil {
			return nil, err
		}
		close(s.waitingPersisted)
		select {
		case <-s.releaseResponse:
			return nil, controlplane.ErrConflict
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.MemoryStore.SyncJobSnapshot(ctx, req)
}

type fakeManagedRuntimeLeaseLister struct {
	leases  []vmlease.Lease
	records []vmleases.LeaseInventoryRecord
	err     error
}

func (missingPocketBaseApp) FindRecordById(any, string, ...func(q *dbx.SelectQuery) error) (*core.Record, error) {
	return nil, errors.New("not found")
}

func (missingPocketBaseApp) FindRecordsByFilter(any, string, string, int, int, ...dbx.Params) ([]*core.Record, error) {
	return nil, errors.New("not found")
}

func (missingPocketBaseApp) FindCollectionByNameOrId(string) (*core.Collection, error) {
	return nil, errors.New("not found")
}

func (missingPocketBaseApp) Save(core.Model) error {
	return nil
}

func (s *contextCapturingStore) GetStack(ctx context.Context, tenantID, stackID string) (*controlplane.Stack, error) {
	s.getStackContext = ctx
	return s.MemoryStore.GetStack(ctx, tenantID, stackID)
}

func (s *contextCapturingStore) UpsertJob(ctx context.Context, req controlplane.UpsertJobRequest) (*controlplane.Job, error) {
	s.upsertJobContext = ctx
	return s.MemoryStore.UpsertJob(ctx, req)
}

func (s *contextCapturingStore) UpdateStackRuntime(ctx context.Context, tenantID, stackID string, runtime controlplane.RuntimeUpdate) (*controlplane.Stack, error) {
	s.updateStackRuntimeContext = ctx
	return s.MemoryStore.UpdateStackRuntime(ctx, tenantID, stackID, runtime)
}

func (s *finalSyncJobStore) SyncJobSnapshot(ctx context.Context, req controlplane.SyncJobSnapshotRequest) (*controlplane.Job, error) {
	if s.onSync != nil {
		s.onSync(req)
	}
	return s.MemoryStore.SyncJobSnapshot(ctx, req)
}

func (f fakeManagedRuntimeLeaseLister) ListInventoryByTenant(context.Context, string) ([]vmleases.LeaseInventoryRecord, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.records != nil {
		return f.records, nil
	}
	records := make([]vmleases.LeaseInventoryRecord, 0, len(f.leases))
	for _, lease := range f.leases {
		records = append(records, vmleases.LeaseInventoryRecord{
			Lease:              lease,
			ExecutionAuthority: vmleases.LeaseExecutionAuthorityTechStackProviderControl,
			AuthorityState:     vmleases.LeaseAuthorityStateNativeActive,
		})
	}
	return records, nil
}

func TestProvisionStackWithOptionsUsesControlPlaneStoreWhenPocketBaseStackIsMissing(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-1",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "Demo",
		Mode:           "easy",
		Status:         "pending",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}

	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers:    1,
		StackStore: store,
		JobStore:   store,
	}, nil)
	defer orch.Stop()

	jobID, err := orch.ProvisionStackWithOptions("stack-1", map[string]interface{}{
		"name":     "Demo",
		"stackkit": "basement-kit",
	}, ProvisionStackOptions{
		TenantID:  "tenant-1",
		OwnerID:   "auth0|user-1",
		StackName: "Demo",
	})
	if err != nil {
		t.Fatalf("ProvisionStackWithOptions: %v", err)
	}
	if jobID == "" {
		t.Fatal("jobID is empty")
	}

	job, err := store.GetJob(ctx, "tenant-1", jobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.StackID != "stack-1" || job.Type != "provision" || job.State != "pending" {
		t.Fatalf("unexpected job: %#v", job)
	}
	stack, err := store.GetStack(ctx, "tenant-1", "stack-1")
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	if stack.Status != "provisioning" {
		t.Fatalf("stack status = %q, want provisioning", stack.Status)
	}
}

func TestProvisionStackProjectsPocketBaseStackBeforeControlPlaneJob(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	app := NewMockPocketBaseApp()
	stacksCollection := app.AddCollection("stacks", "stacks")
	stackRecord := core.NewRecord(stacksCollection)
	stackRecord.Id = "stack-pb"
	stackRecord.Set("name", "PocketBase Stack")
	stackRecord.Set("mode", "easy")
	stackRecord.Set("status", "pending")
	stackRecord.Set("owner_id", "auth0|user-1")
	stackRecord.Set("tenant_id", "tenant-1")
	stackRecord.Set("user_config", map[string]any{
		"runtime_lane":             "monthly-runtime",
		"server_provisioning_mode": "kombify-cloud",
		"provider_id":              "ionos",
	})
	app.AddRecord("stacks", stackRecord)

	orch := NewWithApp(app, &Config{
		Workers:  1,
		JobStore: store,
	}, nil)
	defer orch.Stop()

	jobID, err := orch.ProvisionStackWithOptions("stack-pb", map[string]interface{}{
		"name":     "PocketBase Stack",
		"stackkit": "basement-kit",
	}, ProvisionStackOptions{})
	if err != nil {
		t.Fatalf("ProvisionStackWithOptions: %v", err)
	}
	if jobID == "" {
		t.Fatal("jobID is empty")
	}
	stack, err := store.GetStack(ctx, "tenant-1", "stack-pb")
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	if stack.OwnerSubjectID != "auth0|user-1" || stack.Name != "PocketBase Stack" {
		t.Fatalf("unexpected projected stack: %#v", stack)
	}
	if stack.Config["runtime_lane"] != "monthly-runtime" || stack.Config["provider_id"] != "ionos" {
		t.Fatalf("runtime config not projected: %#v", stack.Config)
	}
	job, err := store.GetJob(ctx, "tenant-1", jobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.StackID != "stack-pb" || job.Type != "provision" {
		t.Fatalf("unexpected job: %#v", job)
	}
}

func TestDeployStackWithOptionsUsesControlPlaneStoreAndAssignedWorker(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-deploy",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "Deploy Demo",
		Mode:           "easy",
		Status:         "pending",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	if _, err := store.UpsertWorkerHeartbeat(ctx, controlplane.Worker{
		ID:             "worker-1",
		TenantID:       "tenant-1",
		StackID:        "stack-deploy",
		OwnerSubjectID: "auth0|user-1",
		Hostname:       "worker-1",
		OS:             "linux",
		Arch:           "amd64",
		Status:         "pending",
		Capabilities: map[string]any{
			nodehandoff.KeyServerNodeRole:    "worker",
			nodehandoff.KeyRequestedServices: []string{"ollama", "transcode"},
		},
		Resources: map[string]any{
			nodehandoff.KeyServerRemoteHost:       "worker-1.lan",
			nodehandoff.KeyServerRemoteUser:       "ubuntu",
			nodehandoff.KeyServerRemotePort:       2222,
			nodehandoff.KeyServerRemoteCredential: "ssh-key:worker-1",
		},
	}); err != nil {
		t.Fatalf("UpsertWorkerHeartbeat: %v", err)
	}
	if _, err := store.ApproveWorker(ctx, "tenant-1", "worker-1", "auth0|user-1", time.Now().UTC()); err != nil {
		t.Fatalf("ApproveWorker: %v", err)
	}
	seedDeployEligibleServerRuntime(t, store, controlplane.Worker{
		ID: "worker-1", TenantID: "tenant-1", StackID: "stack-deploy",
		OwnerSubjectID: "auth0|user-1", Hostname: "worker-1",
	}, time.Now().UTC())
	runtime, err := store.GetServerRuntime(ctx, "tenant-1", "server-worker-1")
	if err != nil {
		t.Fatalf("GetServerRuntime: %v", err)
	}
	runtime.ConnectionState = "degraded"
	runtime.HealthState = "degraded"
	if _, upsertErr := store.UpsertServerRuntime(ctx, *runtime); upsertErr != nil {
		t.Fatalf("UpsertServerRuntime(degraded): %v", upsertErr)
	}

	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers:     1,
		StackStore:  store,
		JobStore:    store,
		WorkerStore: store,
		LeaseLister: fakeManagedRuntimeLeaseLister{},
	}, nil)
	defer orch.Stop()

	jobID, err := orch.DeployStackWithOptions("stack-deploy", ProvisionStackOptions{
		TenantID:  "tenant-1",
		OwnerID:   "auth0|user-1",
		StackName: "Deploy Demo",
	})
	if err != nil {
		t.Fatalf("DeployStackWithOptions: %v", err)
	}
	if jobID == "" {
		t.Fatal("jobID is empty")
	}
	job, err := store.GetJob(ctx, "tenant-1", jobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.StackID != "stack-deploy" || job.Type != "deploy" || job.State != "pending" {
		t.Fatalf("unexpected job: %#v", job)
	}
	stack, err := store.GetStack(ctx, "tenant-1", "stack-deploy")
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	if stack.Status != "provisioning" {
		t.Fatalf("stack status = %q, want provisioning", stack.Status)
	}
	queuedJob, err := orch.GetJobStatus(jobID)
	if err != nil {
		t.Fatalf("GetJobStatus: %v", err)
	}
	workers, ok := queuedJob.Payload["workers"].([]map[string]any)
	if !ok || len(workers) != 1 {
		t.Fatalf("queued workers = %#v, want one map worker", queuedJob.Payload["workers"])
	}
	workerPayload := workers[0]
	if workerPayload["status"] != "online" || workerPayload["connection_state"] != "degraded" {
		t.Fatalf("worker placement/canonical connection states = %#v, want online/degraded", workerPayload)
	}
	if workerPayload[nodehandoff.KeyServerNodeRole] != "worker" || workerPayload[nodehandoff.KeyServerRemoteHost] != "worker-1.lan" {
		t.Fatalf("worker handoff metadata missing from deploy payload: %#v", workerPayload)
	}
	if services := nodehandoff.ServiceKeysFromAny(workerPayload[nodehandoff.KeyRequestedServices]); len(services) != 2 || services[0] != "ollama" || services[1] != "transcode" {
		t.Fatalf("worker requested services = %#v, want ollama/transcode", services)
	}
}

func TestDeployStackWithOptionsRejectsStackRecordLeaseWithoutNativeAuthority(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-managed-retry",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "Managed Retry",
		Mode:           "easy",
		Status:         "pending",
		Config: map[string]any{
			"runtime_lane":             "monthly-runtime",
			"server_provisioning_mode": "kombify-cloud",
			"server_connection_mode":   "managed-subscription",
			"lease_provider":           "centron-managed",
			"runtime_offering_id":      "monthly-runtime-premium",
			"lease_id":                 "lease-managed-retry",
		},
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}

	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers:     1,
		StackStore:  store,
		JobStore:    store,
		WorkerStore: store,
	}, nil)
	defer orch.Stop()

	jobID, err := orch.DeployStackWithOptions("stack-managed-retry", ProvisionStackOptions{
		TenantID:  "tenant-1",
		OwnerID:   "auth0|user-1",
		StackName: "Managed Retry",
	})
	if !errors.Is(err, ErrManagedRuntimeLeaseNotNativeActive) {
		t.Fatalf("DeployStackWithOptions error = %v, want native-active authority failure", err)
	}
	if jobID != "" {
		t.Fatalf("jobID = %q, want no unsafe deploy job", jobID)
	}
	jobs, listErr := store.ListJobsByStack(ctx, "tenant-1", "stack-managed-retry", 10)
	if listErr != nil || len(jobs) != 0 {
		t.Fatalf("unsafe jobs = %#v err=%v", jobs, listErr)
	}
	stack, err := store.GetStack(ctx, "tenant-1", "stack-managed-retry")
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	if stack.Status != "pending" {
		t.Fatalf("stack status = %q, want pending after fail-closed admission", stack.Status)
	}
}

func TestDeployStackWithOptionsRejectsNonNativeLeaseInventory(t *testing.T) {
	for _, test := range []struct {
		name      string
		authority vmleases.LeaseExecutionAuthority
		state     vmleases.LeaseAuthorityState
	}{
		{name: "unbound", state: vmleases.LeaseAuthorityStateUnbound},
		{name: "legacy quarantine", authority: vmleases.LeaseExecutionAuthorityLegacySimulate, state: vmleases.LeaseAuthorityStateLegacyQuarantined},
		{name: "native inactive", authority: vmleases.LeaseExecutionAuthorityTechStackProviderControl, state: vmleases.LeaseAuthorityStateNativeInactive},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := controlplane.NewMemoryStore()
			if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
				ID: "stack-authority", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Authority", Status: "pending",
				Config: map[string]any{
					"runtime_lane": "monthly-runtime", "server_provisioning_mode": "kombify-cloud",
					"server_connection_mode": "managed-subscription", "lease_id": "lease-authority",
				},
			}); err != nil {
				t.Fatal(err)
			}
			lease := vmlease.Lease{
				ID: "lease-authority", Subject: vmlease.Subject{Kind: vmlease.SubjectUser, ID: "owner-1", OrgID: "tenant-1"},
				Resource: vmlease.ResourceRef{ProviderID: "ionos-managed"}, DesiredState: vmlease.DesiredStateRunning,
				Metadata: map[string]string{"runtime_lane": "monthly-runtime", "stack_id": "stack-authority", "role": "foundation"},
			}
			orch := NewWithApp(missingPocketBaseApp{}, &Config{
				Workers: 1, StackStore: store, JobStore: store, WorkerStore: store,
				LeaseLister: fakeManagedRuntimeLeaseLister{records: []vmleases.LeaseInventoryRecord{{
					Lease: lease, ExecutionAuthority: test.authority, AuthorityState: test.state,
				}}},
			}, nil)
			defer orch.Stop()

			jobID, err := orch.DeployStackWithOptions("stack-authority", ProvisionStackOptions{
				TenantID: "tenant-1", OwnerID: "owner-1", StackName: "Authority",
			})
			if !errors.Is(err, ErrManagedRuntimeLeaseNotNativeActive) {
				t.Fatalf("error = %v, want native-active authority failure", err)
			}
			if jobID != "" {
				t.Fatalf("jobID = %q, want no unsafe deploy job", jobID)
			}
			jobs, listErr := store.ListJobsByStack(ctx, "tenant-1", "stack-authority", 10)
			if listErr != nil || len(jobs) != 0 {
				t.Fatalf("unsafe jobs = %#v err=%v", jobs, listErr)
			}
		})
	}
}

func TestDeployStackWithOptionsHydratesManagedRuntimeLeaseFromLister(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-managed-retry",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "Managed Retry",
		Mode:           "easy",
		Status:         "pending",
		Config: map[string]any{
			"runtime_lane":             "monthly-runtime",
			"server_provisioning_mode": "kombify-cloud",
			"server_connection_mode":   "managed-subscription",
		},
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	now := time.Now().UTC()
	lease := vmlease.Lease{
		ID: "lease-managed-retry",
		Subject: vmlease.Subject{
			Kind:  vmlease.SubjectUser,
			ID:    "auth0|user-1",
			OrgID: "tenant-1",
		},
		Resource: vmlease.ResourceRef{
			ProviderID: "centron-managed",
			Region:     "de-fra",
		},
		DesiredState: vmlease.DesiredStateRunning,
		BillingMode:  vmlease.BillingModeSubscription,
		RenewedAt:    now,
		Metadata: map[string]string{
			"runtime_lane":        "monthly-runtime",
			"stack_id":            "stack-managed-retry",
			"runtime_offering_id": "monthly-runtime-premium",
			"runtime_public_ip":   "203.0.113.55",
			"runtime_ssh_host":    "203.0.113.55",
			"runtime_ssh_user":    "root",
			"runtime_ssh_port":    "22",
		},
	}

	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers:     1,
		StackStore:  store,
		JobStore:    store,
		WorkerStore: store,
		LeaseLister: fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{lease}},
	}, nil)
	defer orch.Stop()

	deployOpts := ProvisionStackOptions{
		TenantID:  "tenant-1",
		OwnerID:   "auth0|user-1",
		StackName: "Managed Retry",
	}
	if jobID, err := orch.DeployStackWithOptions("stack-managed-retry", deployOpts); !errors.Is(err, ErrNoAssignedWorkers) || jobID != "" {
		t.Fatalf("lease-only deploy = (%q, %v), want no job and ErrNoAssignedWorkers", jobID, err)
	}
	seedManagedDeployEligibleServerRuntime(t, store, "tenant-1", "auth0|user-1", "stack-managed-retry", "lease-managed-retry", now)

	jobID, err := orch.DeployStackWithOptions("stack-managed-retry", deployOpts)
	if err != nil {
		t.Fatalf("DeployStackWithOptions: %v", err)
	}
	queuedJob, err := orch.GetJobStatus(jobID)
	if err != nil {
		t.Fatalf("GetJobStatus: %v", err)
	}
	for key, want := range map[string]any{
		"lease_id":            "lease-managed-retry",
		"lease_provider":      "centron-managed",
		"runtime_offering_id": "monthly-runtime-premium",
		"runtime_public_ip":   "203.0.113.55",
		"runtime_ssh_host":    "203.0.113.55",
		"runtime_ssh_user":    "root",
		"runtime_ssh_port":    "22",
	} {
		if got := queuedJob.Payload[key]; got != want {
			t.Fatalf("payload %q = %v, want %v", key, got, want)
		}
	}
}

func TestProvisionAutoDeployAdmissionRequiresNativeLeaseAndFreshCanonicalGuard(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-auto-guard",
		TenantID:       "tenant-1",
		OwnerSubjectID: "owner-1",
		Name:           "Auto Guard",
		Mode:           "easy",
		Status:         "provisioning",
		Config: map[string]any{
			"runtime_lane":             "monthly-runtime",
			"server_provisioning_mode": "kombify-cloud",
			"server_connection_mode":   "managed-subscription",
		},
	}); err != nil {
		t.Fatal(err)
	}
	lease := vmlease.Lease{
		ID: "lease-auto-guard",
		Subject: vmlease.Subject{
			Kind:  vmlease.SubjectUser,
			ID:    "owner-1",
			OrgID: "tenant-1",
		},
		Resource:     vmlease.ResourceRef{ProviderID: "ionos-managed"},
		DesiredState: vmlease.DesiredStateRunning,
		Metadata: map[string]string{
			"runtime_lane": "monthly-runtime",
			"stack_id":     "stack-auto-guard",
			"role":         "foundation",
		},
	}
	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers:     1,
		StackStore:  store,
		JobStore:    store,
		WorkerStore: store,
		LeaseLister: fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{lease}},
	}, nil)
	defer orch.Stop()
	request := jobs.AutoDeployAdmissionRequest{
		StackID:  "stack-auto-guard",
		TenantID: "tenant-1",
		OwnerID:  "owner-1",
		LeaseID:  "lease-auto-guard",
	}

	if err := orch.admitProvisionAutoDeploy(ctx, request); !errors.Is(err, ErrNoAssignedWorkers) {
		t.Fatalf("lease-only admission error = %v, want ErrNoAssignedWorkers", err)
	}
	seedManagedDeployEligibleServerRuntime(t, store, "tenant-1", "owner-1", "stack-auto-guard", "lease-auto-guard", time.Now().UTC())
	if err := orch.admitProvisionAutoDeploy(ctx, request); err != nil {
		t.Fatalf("fresh canonical Guard admission failed: %v", err)
	}

	request.OwnerID = "owner-2"
	if err := orch.admitProvisionAutoDeploy(ctx, request); !errors.Is(err, ErrDeployRuntimeEvidenceUnavailable) {
		t.Fatalf("cross-owner admission error = %v, want fail-closed identity mismatch", err)
	}
}

func TestDeployStackWithOptionsPrefersFoundationLeaseOverWorker(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-t2",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "T2",
		Mode:           "easy",
		Status:         "pending",
		Config: map[string]any{
			"runtime_lane":             "monthly-runtime",
			"server_provisioning_mode": "kombify-cloud",
			"server_connection_mode":   "managed-subscription",
		},
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	now := time.Now().UTC()
	mk := func(id, role, host string, renewed time.Time) vmlease.Lease {
		return vmlease.Lease{
			ID:           vmlease.LeaseID(id),
			Subject:      vmlease.Subject{Kind: vmlease.SubjectUser, ID: "auth0|user-1", OrgID: "tenant-1"},
			Resource:     vmlease.ResourceRef{ProviderID: "centron-managed", Region: "de-fra"},
			DesiredState: vmlease.DesiredStateRunning,
			BillingMode:  vmlease.BillingModeSubscription,
			RenewedAt:    renewed,
			Metadata: map[string]string{
				"runtime_lane":        "monthly-runtime",
				"stack_id":            "stack-t2",
				"role":                role,
				"runtime_offering_id": "monthly-runtime-premium",
				"runtime_public_ip":   host,
				"runtime_ssh_host":    host,
				"runtime_ssh_user":    "root",
				"runtime_ssh_port":    "22",
			},
		}
	}
	// The worker lease is renewed MORE recently than the foundation, so the old
	// most-recently-renewed selection would wrongly send the StackKit rollout to
	// the worker. The deploy must still resolve the foundation (control-plane).
	foundation := mk("lease-foundation", "foundation", "10.0.0.1", now.Add(-1*time.Hour))
	worker := mk("lease-worker", "worker", "10.0.0.2", now)
	seedManagedDeployEligibleServerRuntime(t, store, "tenant-1", "auth0|user-1", "stack-t2", "lease-foundation", now)

	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers:     1,
		StackStore:  store,
		JobStore:    store,
		WorkerStore: store,
		LeaseLister: fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{worker, foundation}},
	}, nil)
	defer orch.Stop()

	jobID, err := orch.DeployStackWithOptions("stack-t2", ProvisionStackOptions{
		TenantID:  "tenant-1",
		OwnerID:   "auth0|user-1",
		StackName: "T2",
	})
	if err != nil {
		t.Fatalf("DeployStackWithOptions: %v", err)
	}
	queuedJob, err := orch.GetJobStatus(jobID)
	if err != nil {
		t.Fatalf("GetJobStatus: %v", err)
	}
	if got := queuedJob.Payload["lease_id"]; got != "lease-foundation" {
		t.Fatalf("lease_id = %v, want lease-foundation (foundation must win over a more-recently-renewed worker)", got)
	}
	if got := queuedJob.Payload["runtime_ssh_host"]; got != "10.0.0.1" {
		t.Fatalf("runtime_ssh_host = %v, want 10.0.0.1 (the foundation host)", got)
	}
	if got := queuedJob.Payload["runtime_agent_id"]; got != "guard-lease-foundation" {
		t.Fatalf("runtime_agent_id = %v, want exact foundation lease Guard", got)
	}
}

// TestDeployStackWithOptionsPrefersActiveFoundationLeaseOverStaleStackLeaseID
// locks in the real-world regression the demo hit: the stack record carries a
// stale/cancelled subscription lease_id (from a failed first rollout), while a
// fresh add-server minted an active foundation lease. The deploy must resolve
// the SSH target from the active foundation lease, never the stale stack-record
// lease_id. Before the fix, stackManagedRuntimePayload short-circuited on the
// record lease_id and the foundation-preference logic never ran — the rollout
// kept polling the cancelled subscription lease and timed out.
func TestDeployStackWithOptionsPrefersActiveFoundationLeaseOverStaleStackLeaseID(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-stale",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "Stale Primary",
		Mode:           "easy",
		Status:         "error",
		Config: map[string]any{
			"runtime_lane":             "monthly-runtime",
			"server_provisioning_mode": "kombify-cloud",
			"server_connection_mode":   "managed-subscription",
			// The stack record still points at the cancelled subscription lease.
			"lease_id":            "lease-cancelled-subscription",
			"lease_provider":      "centron-managed",
			"runtime_offering_id": "monthly-runtime-premium",
		},
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	now := time.Now().UTC()
	foundation := vmlease.Lease{
		ID:           "lease-fresh-foundation",
		Subject:      vmlease.Subject{Kind: vmlease.SubjectUser, ID: "auth0|user-1", OrgID: "tenant-1"},
		Resource:     vmlease.ResourceRef{ProviderID: "centron-managed", Region: "de-fra"},
		DesiredState: vmlease.DesiredStateRunning,
		BillingMode:  vmlease.BillingModeSubscription,
		RenewedAt:    now,
		Metadata: map[string]string{
			"runtime_lane":        "monthly-runtime",
			"stack_id":            "stack-stale",
			"role":                "foundation",
			"runtime_offering_id": "monthly-runtime-premium",
			"runtime_public_ip":   "198.51.100.7",
			"runtime_ssh_host":    "198.51.100.7",
			"runtime_ssh_user":    "root",
			"runtime_ssh_port":    "22",
		},
	}
	seedManagedDeployEligibleServerRuntime(t, store, "tenant-1", "auth0|user-1", "stack-stale", "lease-fresh-foundation", now)

	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers:     1,
		StackStore:  store,
		JobStore:    store,
		WorkerStore: store,
		LeaseLister: fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{foundation}},
	}, nil)
	defer orch.Stop()

	jobID, err := orch.DeployStackWithOptions("stack-stale", ProvisionStackOptions{
		TenantID:  "tenant-1",
		OwnerID:   "auth0|user-1",
		StackName: "Stale Primary",
	})
	if err != nil {
		t.Fatalf("DeployStackWithOptions: %v", err)
	}
	queuedJob, err := orch.GetJobStatus(jobID)
	if err != nil {
		t.Fatalf("GetJobStatus: %v", err)
	}
	if got := queuedJob.Payload["lease_id"]; got != "lease-fresh-foundation" {
		t.Fatalf("lease_id = %v, want lease-fresh-foundation (active foundation must supersede the stale stack-record lease_id)", got)
	}
	if got := queuedJob.Payload["runtime_ssh_host"]; got != "198.51.100.7" {
		t.Fatalf("runtime_ssh_host = %v, want 198.51.100.7 (the fresh foundation host)", got)
	}
}

func TestDispatchRoutingRolloutReusesExactFoundationLeaseAndReplaysJob(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-routing", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Routing", Status: "running",
		Config: map[string]any{"runtime_lane": "monthly-runtime", "server_provisioning_mode": "kombify-cloud"},
	}); err != nil {
		t.Fatal(err)
	}
	lease := vmlease.Lease{
		ID: "lease-routing", Subject: vmlease.Subject{Kind: vmlease.SubjectUser, ID: "owner-1", OrgID: "tenant-1"},
		Resource: vmlease.ResourceRef{ProviderID: "ionos-managed"}, DesiredState: vmlease.DesiredStateRunning,
		BillingMode: vmlease.BillingModeSubscription, LifecycleClass: vmlease.LifecycleClassSubscription,
		RestartPolicy: vmlease.RestartPolicyOnUnexpectedStop, RecreatePolicy: vmlease.RecreatePolicyManual,
		ValidFrom: time.Now().UTC().Add(-time.Hour), ValidUntil: time.Now().UTC().Add(time.Hour), RenewedAt: time.Now().UTC(),
		Metadata: map[string]string{
			"runtime_lane": "monthly-runtime", "stack_id": "stack-routing", "role": "foundation",
			"lease_provider": "ionos-managed", "runtime_ssh_host": "198.51.100.44", "runtime_ssh_user": "root", "runtime_ssh_port": "22",
		},
	}
	newerFoundation := lease
	newerFoundation.ID = "lease-newer-foundation"
	newerFoundation.RenewedAt = lease.RenewedAt.Add(time.Minute)
	newerFoundation.Metadata = maps.Clone(lease.Metadata)
	newerFoundation.Metadata["runtime_ssh_host"] = "198.51.100.99"
	seedManagedDeployEligibleServerRuntime(t, store, "tenant-1", "owner-1", "stack-routing", "lease-routing", time.Now().UTC())
	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers: 1, StackStore: store, JobStore: store, WorkerStore: store,
		LeaseLister: fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{newerFoundation, lease}},
	}, nil)
	defer orch.Stop()
	req := stackrouting.RolloutRequest{
		TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-routing",
		LeaseID: "lease-routing", ServerID: runtimeidentity.LeaseServerID("lease-routing"),
		RoutingRevision: 3, IdempotencyKey: "routing-dispatch-1",
	}
	first, err := orch.DispatchRoutingRollout(ctx, req)
	if err != nil {
		t.Fatalf("DispatchRoutingRollout: %v", err)
	}
	second, err := orch.DispatchRoutingRollout(ctx, req)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if first.JobID == "" || second.JobID != first.JobID {
		t.Fatalf("dispatch IDs first=%#v second=%#v", first, second)
	}
	changedRevision := req
	changedRevision.RoutingRevision++
	if _, conflictErr := orch.DispatchRoutingRollout(ctx, changedRevision); !errors.Is(conflictErr, stackrouting.ErrIdempotencyConflict) {
		t.Fatalf("same dispatch key with changed revision error = %v, want ErrIdempotencyConflict", conflictErr)
	}
	stored, err := store.ListJobsByStack(ctx, "tenant-1", "stack-routing", 10)
	if err != nil || len(stored) != 1 {
		t.Fatalf("durable jobs = %#v err=%v", stored, err)
	}
	job, err := orch.GetJobStatus(first.JobID)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"lease_id": "lease-routing", routingDispatchKeyField: "routing-dispatch-1",
		routingDispatchServerField: runtimeidentity.LeaseServerID("lease-routing"), routingDispatchRevisionField: int64(3),
	} {
		if got := job.Payload[key]; got != want {
			t.Fatalf("payload[%s] = %#v, want %#v", key, got, want)
		}
	}
}

func TestDispatchRoutingRolloutFailsBeforeJobForWrongExactTarget(t *testing.T) {
	for _, test := range []struct {
		name, leaseID, serverID string
	}{
		{name: "wrong selected lease", leaseID: "lease-other", serverID: runtimeidentity.LeaseServerID("lease-other")},
		{name: "wrong canonical server", leaseID: "lease-foundation", serverID: "server-invented"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := controlplane.NewMemoryStore()
			if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
				ID: "stack-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Exact", Status: "error",
			}); err != nil {
				t.Fatal(err)
			}
			lease := vmlease.Lease{
				ID: "lease-foundation", Subject: vmlease.Subject{Kind: vmlease.SubjectUser, ID: "owner-1", OrgID: "tenant-1"},
				Resource: vmlease.ResourceRef{ProviderID: "ionos-managed"}, DesiredState: vmlease.DesiredStateRunning,
				BillingMode: vmlease.BillingModeSubscription, LifecycleClass: vmlease.LifecycleClassSubscription,
				RestartPolicy: vmlease.RestartPolicyOnUnexpectedStop, RecreatePolicy: vmlease.RecreatePolicyManual,
				ValidFrom: time.Now().UTC().Add(-time.Hour), ValidUntil: time.Now().UTC().Add(time.Hour),
				Metadata: map[string]string{"runtime_lane": "monthly-runtime", "stack_id": "stack-1", "role": "foundation"},
			}
			orch := NewWithApp(missingPocketBaseApp{}, &Config{
				Workers: 1, StackStore: store, JobStore: store, WorkerStore: store,
				LeaseLister: fakeManagedRuntimeLeaseLister{leases: []vmlease.Lease{lease}},
			}, nil)
			defer orch.Stop()
			_, err := orch.DispatchRoutingRollout(ctx, stackrouting.RolloutRequest{
				TenantID: "tenant-1", OwnerSubjectID: "owner-1", StackID: "stack-1",
				LeaseID: test.leaseID, ServerID: test.serverID, RoutingRevision: 1, IdempotencyKey: "routing-denied",
			})
			if !errors.Is(err, stackrouting.ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
			jobs, listErr := store.ListJobsByStack(ctx, "tenant-1", "stack-1", 10)
			if listErr != nil || len(jobs) != 0 {
				t.Fatalf("unsafe jobs = %#v err=%v", jobs, listErr)
			}
		})
	}
}

func TestDestroyStackWithOptionsUsesControlPlaneStoreWhenPocketBaseStackIsMissing(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-destroy",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "Destroy Demo",
		Mode:           "easy",
		Status:         "pending",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}

	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers:     1,
		StackStore:  store,
		JobStore:    store,
		LeaseLister: fakeManagedRuntimeLeaseLister{},
	}, nil)
	defer orch.Stop()

	jobID, err := orch.DestroyStackWithOptions("stack-destroy", ProvisionStackOptions{
		TenantID:  "tenant-1",
		OwnerID:   "auth0|user-1",
		StackName: "Destroy Demo",
	})
	if err != nil {
		t.Fatalf("DestroyStackWithOptions: %v", err)
	}
	if jobID == "" {
		t.Fatal("jobID is empty")
	}
	job, err := store.GetJob(ctx, "tenant-1", jobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.StackID != "stack-destroy" || job.Type != "destroy" || job.State != "pending" {
		t.Fatalf("unexpected job: %#v", job)
	}
	stack, err := store.GetStack(ctx, "tenant-1", "stack-destroy")
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	if stack.Status != "stopping" {
		t.Fatalf("stack status = %q, want stopping", stack.Status)
	}
}

func TestDestroyStackWithoutWorkspaceArchivesExactNoLeaseControlPlaneStack(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	for _, stack := range []controlplane.CreateStackRequest{
		{
			ID:             "stack-no-workspace",
			TenantID:       "tenant-1",
			OwnerSubjectID: "auth0|user-1",
			Name:           "Fresh failed managed stack",
			Mode:           "easy",
			Status:         "error",
		},
		{
			ID:             "stack-unrelated",
			TenantID:       "tenant-1",
			OwnerSubjectID: "auth0|user-1",
			Name:           "Unrelated stack",
			Mode:           "easy",
			Status:         "running",
		},
	} {
		if _, err := store.CreateStack(ctx, stack); err != nil {
			t.Fatalf("CreateStack(%s): %v", stack.ID, err)
		}
	}

	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers:    1,
		WorkDir:    t.TempDir(),
		StackStore: store,
		JobStore:   store,
	}, nil)
	orch.Start()
	defer orch.Stop()

	jobID, err := orch.DestroyStackWithOptions("stack-no-workspace", ProvisionStackOptions{
		RequestContext: ctx,
		TenantID:       "tenant-1",
		OwnerID:        "auth0|user-1",
		StackName:      "Fresh failed managed stack",
	})
	if err != nil {
		t.Fatalf("DestroyStackWithOptions: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	archived, completed := false, false
	for time.Now().Before(deadline) {
		if _, err := store.GetStack(ctx, "tenant-1", "stack-no-workspace"); errors.Is(err, controlplane.ErrNotFound) {
			archived = true
		}
		if job, err := store.GetJob(ctx, "tenant-1", jobID); err == nil && job.State == "completed" {
			completed = true
		}
		if archived && completed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !archived {
		if _, err := store.GetStack(ctx, "tenant-1", "stack-no-workspace"); !errors.Is(err, controlplane.ErrNotFound) {
			t.Fatalf("no-workspace stack remains visible after completed destroy: %v", err)
		}
	}
	if _, err := store.GetStack(ctx, "tenant-1", "stack-unrelated"); err != nil {
		t.Fatalf("unrelated stack was mutated while reconciling no-workspace destroy: %v", err)
	}
	job, err := store.GetJob(ctx, "tenant-1", jobID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.State != "completed" {
		t.Fatalf("destroy job state = %q, want completed", job.State)
	}
	if !completed {
		t.Fatal("no-workspace destroy did not persist a completed durable receipt")
	}
}

func TestReconcileNoWorkspaceDestroyRefusesActiveManagedLeaseWithoutForeignMutation(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	for _, stack := range []controlplane.CreateStackRequest{
		{
			ID:             "stack-managed-live-lease",
			TenantID:       "tenant-1",
			OwnerSubjectID: "owner-1",
			Name:           "Managed stack",
			Mode:           "easy",
			Status:         "error",
			Config: map[string]any{
				"runtime_lane":             runtimeLaneMonthly,
				"server_provisioning_mode": runtimeProvisionKombify,
				"server_connection_mode":   runtimeConnectionManaged,
			},
		},
		{
			ID:             "stack-unrelated",
			TenantID:       "tenant-1",
			OwnerSubjectID: "owner-1",
			Name:           "Unrelated stack",
			Mode:           "easy",
			Status:         "running",
		},
	} {
		if _, err := store.CreateStack(ctx, stack); err != nil {
			t.Fatalf("CreateStack(%s): %v", stack.ID, err)
		}
	}

	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		StackStore: store,
		LeaseLister: fakeManagedRuntimeLeaseLister{records: []vmleases.LeaseInventoryRecord{{
			Lease: vmlease.Lease{
				ID:       "lease-still-live",
				Metadata: map[string]string{"stack_id": "stack-managed-live-lease"},
			},
		}}},
	}, nil)
	defer orch.Stop()

	err := orch.reconcileNoWorkspaceDestroy(ctx, jobs.NoWorkspaceDestroyReconcileRequest{
		StackID: "stack-managed-live-lease", TenantID: "tenant-1", OwnerID: "owner-1",
	})
	if !errors.Is(err, ErrNoWorkspaceDestroyLeasePresent) {
		t.Fatalf("reconcile error = %v, want active managed lease rejection", err)
	}
	if _, err := store.GetStack(ctx, "tenant-1", "stack-managed-live-lease"); err != nil {
		t.Fatalf("managed stack was archived despite an active lease: %v", err)
	}
	if _, err := store.GetStack(ctx, "tenant-1", "stack-unrelated"); err != nil {
		t.Fatalf("unrelated stack was mutated while rejecting active lease: %v", err)
	}
}

func TestReconcileNoWorkspaceDestroyManagedIntentWaitsForLeaseAuthority(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-managed-no-authority", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Managed stack", Mode: "easy", Status: "error",
		Config: map[string]any{"runtime_lane": runtimeLaneMonthly},
	}); err != nil {
		t.Fatal(err)
	}
	orch := NewWithApp(missingPocketBaseApp{}, &Config{StackStore: store}, nil)
	defer orch.Stop()

	err := orch.reconcileNoWorkspaceDestroy(ctx, jobs.NoWorkspaceDestroyReconcileRequest{
		StackID: "stack-managed-no-authority", TenantID: "tenant-1", OwnerID: "owner-1",
	})
	var waitErr *jobs.JobWaitError
	if !errors.As(err, &waitErr) || waitErr.Reason != "waiting_no_workspace_destroy_reconciliation" {
		t.Fatalf("reconcile error = %#v, want durable authority wait", err)
	}
	if _, err := store.GetStack(ctx, "tenant-1", "stack-managed-no-authority"); err != nil {
		t.Fatalf("managed stack was archived without current lease authority: %v", err)
	}
}

func TestDestroyStackRequiresCleanupForQuarantinedManagedLease(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-quarantine", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Quarantine", Status: "error",
	}); err != nil {
		t.Fatal(err)
	}
	lease := vmlease.Lease{
		ID: "lease-quarantine", Subject: vmlease.Subject{Kind: vmlease.SubjectUser, ID: "owner-1", OrgID: "tenant-1"},
		Resource: vmlease.ResourceRef{ProviderID: "centron-managed"}, DesiredState: vmlease.DesiredStateArchived,
		Metadata: map[string]string{"runtime_lane": "monthly-runtime", "stack_id": "stack-quarantine"},
	}
	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers: 1, StackStore: store, JobStore: store,
		LeaseLister: fakeManagedRuntimeLeaseLister{records: []vmleases.LeaseInventoryRecord{{
			Lease: lease, ExecutionAuthority: vmleases.LeaseExecutionAuthorityLegacySimulate,
			AuthorityState: vmleases.LeaseAuthorityStateLegacyQuarantined,
		}}},
	}, nil)
	defer orch.Stop()

	jobID, err := orch.DestroyStackWithOptions("stack-quarantine", ProvisionStackOptions{
		TenantID: "tenant-1", OwnerID: "owner-1", StackName: "Quarantine",
	})
	if err != nil {
		t.Fatalf("DestroyStackWithOptions: %v", err)
	}
	job, err := orch.GetJobStatus(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if required, ok := job.Payload[jobs.ManagedRuntimeDecommissionRequiredField].(bool); !ok || !required {
		t.Fatalf("managed runtime cleanup classification = %#v, want true", job.Payload[jobs.ManagedRuntimeDecommissionRequiredField])
	}
}

func TestSyncJobProgressPersistsTerminalControlPlaneStateAfterRunningRace(t *testing.T) {
	baseStore := controlplane.NewMemoryStore()
	store := &finalSyncJobStore{MemoryStore: baseStore}
	ctx := context.Background()
	if _, err := baseStore.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-final-sync",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "Final Sync",
		Mode:           "easy",
		Status:         "provisioning",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	startedAt := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := baseStore.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-final-sync", TenantID: "tenant-1", StackID: "stack-final-sync", Type: "deploy", State: "pending",
	}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := baseStore.StartJob(ctx, "tenant-1", "job-final-sync", startedAt); err != nil {
		t.Fatalf("StartJob: %v", err)
	}

	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers:    1,
		StackStore: baseStore,
		JobStore:   store,
	}, nil)
	defer orch.Stop()

	job := &jobs.Job{
		ID:       "job-final-sync",
		Type:     jobs.JobTypeDeploy,
		TargetID: "stack-final-sync",
		Payload: map[string]interface{}{
			"tenant_id": "tenant-1",
		},
	}
	if err := orch.queue.Enqueue(job); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	job.State = jobs.JobStateRunning
	job.StartedAt = &startedAt
	job.Step = jobs.StepRolloutRunner
	job.Progress = 75
	job.Message = "Running StackKits rollout..."

	observedStates := []string{}
	flipped := false
	store.onSync = func(req controlplane.SyncJobSnapshotRequest) {
		observedStates = append(observedStates, req.Job.State)
		if !flipped && req.Job.State == string(jobs.JobStateRunning) {
			flipped = true
			job.State = jobs.JobStateFailed
			job.Step = jobs.StepGenerateIaC
			job.Error = "StackKits artifact generation failed"
			job.ErrorDetails = "Could not generate StackKits rollout artifacts."
			job.Result = map[string]any{
				"runtime_phase":  string(jobs.RuntimePhaseLeaseReady),
				"lease_id":       "lease-ionos",
				"lease_provider": "ionos-managed",
			}
		}
	}

	done := make(chan struct{})
	go func() {
		orch.syncJobProgress(job.ID, "", "tenant-1")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("syncJobProgress did not stop after terminal state")
	}

	storedJob, err := baseStore.GetJob(ctx, "tenant-1", job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if storedJob.State != string(jobs.JobStateFailed) {
		t.Fatalf("stored job state = %q, want failed; observed states=%v", storedJob.State, observedStates)
	}
	if storedJob.Step != jobs.StepGenerateIaC {
		t.Fatalf("stored job step = %q, want %q", storedJob.Step, jobs.StepGenerateIaC)
	}
	if storedJob.Error != "StackKits artifact generation failed" || storedJob.ErrorDetails != "Could not generate StackKits rollout artifacts." {
		t.Fatalf("stored job error = %q details=%q", storedJob.Error, storedJob.ErrorDetails)
	}
	if storedJob.Result["runtime_phase"] != string(jobs.RuntimePhaseLeaseReady) ||
		storedJob.Result["lease_id"] != "lease-ionos" ||
		storedJob.Result["lease_provider"] != "ionos-managed" {
		t.Fatalf("stored job result missing lease metadata: %#v", storedJob.Result)
	}
}

func TestStopPersistsRunningJobCancellationBeforeSyncShutdown(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-shutdown", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Shutdown", Status: "provisioning",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-shutdown", TenantID: "tenant-1", StackID: "stack-shutdown", Type: "deploy", State: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	orch := NewWithApp(missingPocketBaseApp{}, &Config{Workers: 1, StackStore: store, JobStore: store}, nil)
	started := make(chan struct{})
	orch.Queue().RegisterHandler(jobs.JobTypeDeploy, func(handlerCtx context.Context, _ *jobs.Job, _ *jobs.Queue) error {
		close(started)
		<-handlerCtx.Done()
		return handlerCtx.Err()
	})
	job := &jobs.Job{
		ID: "job-shutdown", Type: jobs.JobTypeDeploy, TargetType: targetTypeStack, TargetID: "stack-shutdown",
		Payload: map[string]any{"tenant_id": "tenant-1"}, MaxAttempts: 1,
	}
	if err := orch.enqueueWithSync(job, nil, "tenant-1"); err != nil {
		t.Fatal(err)
	}
	orch.Start()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	orch.Stop()

	stored, err := store.GetJob(ctx, "tenant-1", "job-shutdown")
	if err != nil {
		t.Fatal(err)
	}
	if canonicalEnrollmentJobState(stored.State) != string(jobs.JobStateCancelled) || stored.CompletedAt == nil {
		t.Fatalf("graceful shutdown left nonterminal durable job: %#v", stored)
	}
}

func TestEnqueueWithSyncCreatesMissingDurableJobAndBindsTenant(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-durable-admission", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Durable Admission", Status: "ready",
	}); err != nil {
		t.Fatal(err)
	}
	orch := NewWithApp(missingPocketBaseApp{}, &Config{Workers: 1, StackStore: store, JobStore: store}, nil)
	job := &jobs.Job{
		ID: "job-durable-admission", Type: jobs.JobTypeDriftResolve, TargetType: targetTypeStack,
		TargetID: "stack-durable-admission", MaxAttempts: 1,
	}
	if err := orch.enqueueWithSync(job, nil, "tenant-1"); err != nil {
		t.Fatal(err)
	}
	orch.Stop()

	stored, err := store.GetJob(ctx, "tenant-1", job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != persistentStatePending || stored.Type != string(jobs.JobTypeDriftResolve) {
		t.Fatalf("durable admission = %#v", stored)
	}
	if got := job.Snapshot().Payload["tenant_id"]; got != "tenant-1" {
		t.Fatalf("bound tenant_id = %#v", got)
	}
}

func TestStopPreservesDurableEnrollmentWaitForAnotherReplica(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-wait-shutdown", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Wait Shutdown", Status: "provisioning",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-wait-shutdown", TenantID: "tenant-1", StackID: "stack-wait-shutdown", Type: "deploy", State: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	orch := NewWithApp(missingPocketBaseApp{}, &Config{Workers: 1, StackStore: store, JobStore: store}, nil)
	orch.Queue().RegisterHandler(jobs.JobTypeDeploy, func(context.Context, *jobs.Job, *jobs.Queue) error {
		return &jobs.JobWaitError{Reason: jobs.WaitReasonManagedRuntimeEnrollment, ResumeAfter: time.Hour}
	})
	job := &jobs.Job{
		ID: "job-wait-shutdown", Type: jobs.JobTypeDeploy, TargetType: targetTypeStack, TargetID: "stack-wait-shutdown",
		Result: map[string]any{"lease_id": "lease-wait-shutdown"}, MaxAttempts: 1,
	}
	if err := orch.enqueueWithSync(job, nil, "tenant-1"); err != nil {
		t.Fatal(err)
	}
	orch.Start()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stored, err := store.GetJob(ctx, "tenant-1", job.ID)
		if err == nil {
			wait, _ := stored.Result["job_wait"].(map[string]any)
			if stored.State == persistentStatePending && wait["reason"] == jobs.WaitReasonManagedRuntimeEnrollment {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	orch.Stop()

	stored, err := store.GetJob(ctx, "tenant-1", job.ID)
	if err != nil {
		t.Fatal(err)
	}
	wait, _ := stored.Result["job_wait"].(map[string]any)
	if stored.State != persistentStatePending || stored.CompletedAt != nil ||
		wait["state"] != string(jobs.JobStateWaiting) || wait["reason"] != jobs.WaitReasonManagedRuntimeEnrollment || wait["next_resume_at"] == "" {
		t.Fatalf("shutdown destroyed durable enrollment wait: %#v", stored)
	}
}

func TestRetryableHandlerPersistsPendingBeforeNextDurableClaim(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-transient-retry", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Transient Retry", Status: "provisioning",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-transient-retry", TenantID: "tenant-1", StackID: "stack-transient-retry", Type: "deploy", State: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	orch := NewWithApp(missingPocketBaseApp{}, &Config{Workers: 1, StackStore: store, JobStore: store}, nil)
	var attempts atomic.Int32
	orch.Queue().RegisterHandler(jobs.JobTypeDeploy, func(context.Context, *jobs.Job, *jobs.Queue) error {
		if attempts.Add(1) == 1 {
			return jobs.NewTransientError(errors.New("temporary runtime failure"))
		}
		return nil
	})
	job := &jobs.Job{
		ID: "job-transient-retry", Type: jobs.JobTypeDeploy, TargetType: targetTypeStack, TargetID: "stack-transient-retry",
		MaxAttempts: 2,
	}
	if err := orch.enqueueWithSync(job, nil, "tenant-1"); err != nil {
		t.Fatal(err)
	}
	orch.Start()
	defer orch.Stop()

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		stored, err := store.GetJob(ctx, "tenant-1", job.ID)
		if err == nil && stored.State == string(jobs.JobStateCompleted) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	stored, err := store.GetJob(ctx, "tenant-1", job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := attempts.Load(); got != 2 || stored.State != string(jobs.JobStateCompleted) || stored.CompletedAt == nil {
		t.Fatalf("retry did not complete through durable claims: attempts=%d job=%#v", got, stored)
	}
}

func TestStaleWaitingSyncConflictKeepsTrackingNewExecutionGeneration(t *testing.T) {
	ctx := context.Background()
	baseStore := controlplane.NewMemoryStore()
	store := &delayedStaleConflictJobStore{
		MemoryStore: baseStore, waitingPersisted: make(chan struct{}), releaseResponse: make(chan struct{}),
	}
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-stale-wait", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Stale Wait", Status: "provisioning",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: "job-stale-wait", TenantID: "tenant-1", StackID: "stack-stale-wait", Type: "deploy", State: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	orch := NewWithApp(missingPocketBaseApp{}, &Config{Workers: 1, StackStore: store, JobStore: store}, nil)
	// Force the delayed periodic-sync race. Production has an additional
	// synchronous durable ack before resume; the periodic tracker must still be
	// safe when an old response arrives after a newer generation starts.
	orch.Queue().SetExecutionSnapshotSyncer(nil)
	var calls atomic.Int32
	secondStarted := make(chan struct{})
	releaseSecond := make(chan struct{})
	orch.Queue().RegisterHandler(jobs.JobTypeDeploy, func(handlerCtx context.Context, _ *jobs.Job, _ *jobs.Queue) error {
		if calls.Add(1) == 1 {
			return &jobs.JobWaitError{Reason: jobs.WaitReasonManagedRuntimeEnrollment, ResumeAfter: 700 * time.Millisecond}
		}
		close(secondStarted)
		select {
		case <-releaseSecond:
			return nil
		case <-handlerCtx.Done():
			return handlerCtx.Err()
		}
	})
	job := &jobs.Job{
		ID: "job-stale-wait", Type: jobs.JobTypeDeploy, TargetType: targetTypeStack, TargetID: "stack-stale-wait", MaxAttempts: 1,
	}
	if err := orch.enqueueWithSync(job, nil, "tenant-1"); err != nil {
		t.Fatal(err)
	}
	orch.Start()
	defer orch.Stop()

	select {
	case <-store.waitingPersisted:
	case <-time.After(2 * time.Second):
		t.Fatal("waiting snapshot was not persisted")
	}
	select {
	case <-secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("new execution generation did not start")
	}
	close(store.releaseResponse)
	close(releaseSecond)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stored, err := store.GetJob(ctx, "tenant-1", job.ID)
		if err == nil && stored.State == string(jobs.JobStateCompleted) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	stored, _ := store.GetJob(ctx, "tenant-1", job.ID)
	t.Fatalf("stale conflict stopped final sync: %#v", stored)
}

func TestSharedControlPlaneSerializesDifferentStackJobsAcrossReplicas(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-replica-barrier", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Replica Barrier", Status: "provisioning",
	}); err != nil {
		t.Fatal(err)
	}
	for _, jobID := range []string{"job-replica-a", "job-replica-b"} {
		if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
			ID: jobID, TenantID: "tenant-1", StackID: "stack-replica-barrier", Type: "deploy", State: "pending",
		}); err != nil {
			t.Fatal(err)
		}
	}

	orchA := NewWithApp(missingPocketBaseApp{}, &Config{Workers: 1, StackStore: store, JobStore: store}, nil)
	orchB := NewWithApp(missingPocketBaseApp{}, &Config{Workers: 1, StackStore: store, JobStore: store}, nil)
	defer orchA.Stop()
	defer orchB.Stop()

	startedA := make(chan struct{})
	releaseA := make(chan struct{})
	startedB := make(chan struct{}, 1)
	orchA.Queue().RegisterHandler(jobs.JobTypeDeploy, func(handlerCtx context.Context, _ *jobs.Job, _ *jobs.Queue) error {
		close(startedA)
		select {
		case <-releaseA:
			return nil
		case <-handlerCtx.Done():
			return handlerCtx.Err()
		}
	})
	orchB.Queue().RegisterHandler(jobs.JobTypeDeploy, func(context.Context, *jobs.Job, *jobs.Queue) error {
		startedB <- struct{}{}
		return nil
	})

	jobA := &jobs.Job{
		ID: "job-replica-a", Type: jobs.JobTypeDeploy, TargetType: targetTypeStack, TargetID: "stack-replica-barrier",
		Payload: map[string]any{"tenant_id": "tenant-1"}, MaxAttempts: 1,
	}
	jobB := &jobs.Job{
		ID: "job-replica-b", Type: jobs.JobTypeDeploy, TargetType: targetTypeStack, TargetID: "stack-replica-barrier",
		Payload: map[string]any{"tenant_id": "tenant-1"}, MaxAttempts: 1,
	}
	if err := orchA.enqueueWithSync(jobA, nil, "tenant-1"); err != nil {
		t.Fatal(err)
	}
	if err := orchB.enqueueWithSync(jobB, nil, "tenant-1"); err != nil {
		t.Fatal(err)
	}
	orchA.Start()
	select {
	case <-startedA:
	case <-time.After(time.Second):
		t.Fatal("first replica handler did not start")
	}
	orchB.Start()

	waitingDeadline := time.Now().Add(time.Second)
	for time.Now().Before(waitingDeadline) {
		snapshot := jobB.Snapshot()
		if snapshot.State == jobs.JobStateWaiting && snapshot.WaitReason == jobs.WaitReasonStackExecution {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if snapshot := jobB.Snapshot(); snapshot.State != jobs.JobStateWaiting || snapshot.WaitReason != jobs.WaitReasonStackExecution {
		t.Fatalf("second replica did not wait behind durable claim: %#v", snapshot)
	}
	select {
	case <-startedB:
		t.Fatal("second replica handler overlapped the first stack operation")
	case <-time.After(100 * time.Millisecond):
	}
	storedB, err := store.GetJob(ctx, "tenant-1", jobB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedB.State != string(jobs.JobStatePending) {
		t.Fatalf("busy second job durable state = %q, want pending", storedB.State)
	}

	close(releaseA)
	select {
	case <-startedB:
	case <-time.After(3 * time.Second):
		t.Fatal("second replica handler did not start after the first released its durable claim")
	}
}

func TestSyncControlPlaneJobProjectsMissingStackBeforeJobUpsert(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers:  1,
		JobStore: store,
	}, nil)
	defer orch.Stop()

	job := &jobs.Job{
		ID:         "job-missing-stack",
		Type:       jobs.JobTypeProvision,
		TargetID:   "stack-missing",
		TargetName: "Projected Stack",
		State:      jobs.JobStateRunning,
		Progress:   75,
		Payload: map[string]interface{}{
			"owner_id": "auth0|user-1",
			"spec": map[string]interface{}{
				"runtime_lane": "monthly-runtime",
			},
		},
		Result: map[string]any{
			"lease_provider":   "ionos-managed",
			"provider_region":  "us/ewr",
			"ionos_datacenter": "us/ewr",
		},
	}
	startedAt := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: job.ID, TenantID: "tenant-1", StackID: job.TargetID, Type: string(job.Type), State: "pending",
	}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := store.StartJob(ctx, "tenant-1", job.ID, startedAt); err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	job.StartedAt = &startedAt

	orch.syncControlPlaneJob(job, "tenant-1")

	stack, err := store.GetStack(ctx, "tenant-1", "stack-missing")
	if err != nil {
		t.Fatalf("GetStack: %v", err)
	}
	if stack.OwnerSubjectID != "auth0|user-1" || stack.Name != "Projected Stack" {
		t.Fatalf("unexpected projected stack: %#v", stack)
	}
	if stack.Config["lease_provider"] != "ionos-managed" ||
		stack.Config["provider_region"] != "us/ewr" ||
		stack.Config["ionos_datacenter"] != "us/ewr" {
		t.Fatalf("lease provider not projected: %#v", stack.Config)
	}
	storedJob, err := store.GetJob(ctx, "tenant-1", "job-missing-stack")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if storedJob.StackID != "stack-missing" || storedJob.State != string(jobs.JobStateRunning) {
		t.Fatalf("unexpected stored job: %#v", storedJob)
	}
}

func TestSyncControlPlaneJobPersistsWaitingAsHonestPendingProjection(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	orch := NewWithApp(missingPocketBaseApp{}, &Config{Workers: 1, JobStore: store}, nil)
	defer orch.Stop()

	nextResumeAt := time.Now().UTC().Add(20 * time.Second).Truncate(time.Nanosecond)
	job := &jobs.Job{
		ID:           "job-waiting-enrollment",
		Type:         jobs.JobTypeDeploy,
		TargetID:     "stack-waiting-enrollment",
		TargetName:   "Waiting Enrollment",
		State:        jobs.JobStateWaiting,
		WaitReason:   jobs.WaitReasonManagedRuntimeEnrollment,
		NextResumeAt: &nextResumeAt,
		Message:      "Managed VM is still enrolling; the next rollout check is scheduled.",
		Payload: map[string]interface{}{
			"owner_id": "auth0|user-1",
		},
		Result: map[string]interface{}{"lease_id": "lease-1"},
	}
	startedAt := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: job.ID, TenantID: "tenant-1", StackID: job.TargetID, Type: string(job.Type), State: "pending",
	}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := store.StartJob(ctx, "tenant-1", job.ID, startedAt); err != nil {
		t.Fatalf("StartJob: %v", err)
	}
	job.StartedAt = &startedAt

	orch.syncControlPlaneJob(job, "tenant-1")

	stored, err := store.GetJob(ctx, "tenant-1", job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != string(jobs.JobStatePending) || stored.CompletedAt != nil || !stored.ScheduledFor.Equal(nextResumeAt) {
		t.Fatalf("legacy wait projection = %#v", stored)
	}
	wait, _ := stored.Result["job_wait"].(map[string]interface{})
	if wait["state"] != string(jobs.JobStateWaiting) || wait["reason"] != jobs.WaitReasonManagedRuntimeEnrollment || wait["message"] != job.Message || wait["next_resume_at"] == "" {
		t.Fatalf("job_wait result = %#v", wait)
	}
}

func TestSyncControlPlaneJobUsesDatabaseCancellationSpelling(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	orch := NewWithApp(missingPocketBaseApp{}, &Config{Workers: 1, JobStore: store}, nil)
	defer orch.Stop()

	job := &jobs.Job{
		ID:         "job-canceled",
		Type:       jobs.JobTypeDeploy,
		TargetID:   "stack-canceled",
		TargetName: "Canceled Stack",
		State:      jobs.JobStateCancelled,
		Payload: map[string]interface{}{
			"owner_id": "auth0|user-1",
		},
	}
	if _, err := store.CreateJob(ctx, controlplane.UpsertJobRequest{
		ID: job.ID, TenantID: "tenant-1", StackID: job.TargetID, Type: string(job.Type), State: "pending",
	}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	orch.syncControlPlaneJob(job, "tenant-1")
	stored, err := store.GetJob(ctx, "tenant-1", job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != "cancelled" {
		t.Fatalf("control-plane cancellation state = %q, want cancelled", stored.State)
	}
}

func TestProvisionStackWithOptionsUsesRequestContextForControlPlaneStart(t *testing.T) {
	baseStore := controlplane.NewMemoryStore()
	store := &contextCapturingStore{MemoryStore: baseStore}
	ctx := context.Background()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-ctx",
		TenantID:       "tenant-ctx",
		OwnerSubjectID: "auth0|user-ctx",
		Name:           "Context Demo",
		Mode:           "easy",
		Status:         "pending",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}

	orch := NewWithApp(missingPocketBaseApp{}, &Config{
		Workers:    1,
		StackStore: store,
		JobStore:   store,
	}, nil)
	defer orch.Stop()

	requestCtx := context.WithValue(context.Background(), requestContextKey{}, "create-stack")
	if _, err := orch.ProvisionStackWithOptions("stack-ctx", map[string]interface{}{
		"name":     "Context Demo",
		"stackkit": "basement-kit",
	}, ProvisionStackOptions{
		RequestContext: requestCtx,
		TenantID:       "tenant-ctx",
		OwnerID:        "auth0|user-ctx",
		StackName:      "Context Demo",
	}); err != nil {
		t.Fatalf("ProvisionStackWithOptions: %v", err)
	}

	for name, observed := range map[string]context.Context{
		"GetStack":           store.getStackContext,
		"UpsertJob":          store.upsertJobContext,
		"UpdateStackRuntime": store.updateStackRuntimeContext,
	} {
		if observed == nil {
			t.Fatalf("%s did not receive a context", name)
		}
		if got := observed.Value(requestContextKey{}); got != "create-stack" {
			t.Fatalf("%s context marker = %#v, want create-stack", name, got)
		}
	}
}

func TestManagedLeaseReconciliationFailsClosedWithoutDurableCustody(t *testing.T) {
	store := controlplane.NewMemoryStore()
	orch := NewWithApp(missingPocketBaseApp{}, &Config{Workers: 1, JobStore: store}, nil)
	defer orch.Stop()

	if orch.DurableReconciliationReady() {
		t.Fatal("generic orchestrator queue must not claim restart-safe reconciliation custody")
	}
	_, err := orch.EnqueueManagedLeaseReconciliation(
		context.Background(),
		"stack-1",
		"tenant-1",
		"owner-1",
		"lease-1",
		strings.Repeat("a", 64),
	)
	if !errors.Is(err, ErrManagedLeaseReconciliationDurabilityUnavailable) {
		t.Fatalf("error = %v, want ErrManagedLeaseReconciliationDurabilityUnavailable", err)
	}
	stored, listErr := store.ListJobsByTenant(context.Background(), "tenant-1", 10)
	if listErr != nil {
		t.Fatalf("ListJobsByTenant: %v", listErr)
	}
	if len(stored) != 0 {
		t.Fatalf("unsafe reconciliation persisted jobs: %#v", stored)
	}
	if got := orch.GetQueueStats()["total"]; got != 0 {
		t.Fatalf("unsafe reconciliation admitted %d process-local jobs", got)
	}
}
