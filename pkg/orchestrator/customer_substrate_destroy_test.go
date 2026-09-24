package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

type customerGuestCleanupProbe struct {
	request *jobs.ManagedLeaseDecommissionRequest
	pending error
}

func (p *customerGuestCleanupProbe) DecommissionManagedLeases(_ context.Context, request jobs.ManagedLeaseDecommissionRequest) (*jobs.ManagedLeaseDecommissionResult, error) {
	p.request = &request
	return nil, p.pending
}

// Exercise destroy admission and its existing job handler together: a local
// customer guest must reach native cleanup before workspace retirement.
func TestDestroyCustomerSubstrateRequiresOwnedNativeCleanup(t *testing.T) {
	for _, scenario := range []struct {
		name, owner, tenant, deployment string
		required                        bool
	}{
		{"owned", "owner-1", "tenant-1", "deployment-1", true},
		{"foreign owner", "owner-2", "tenant-1", "deployment-1", false},
		{"foreign tenant", "owner-1", "tenant-2", "deployment-1", false},
		{"foreign deployment", "owner-1", "tenant-1", "deployment-2", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			store := controlplane.NewMemoryStore()
			if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
				ID: "deployment-1", TenantID: "tenant-1", OwnerSubjectID: "owner-1", Name: "Home", Status: "error",
			}); err != nil {
				t.Fatal(err)
			}
			lease := vmlease.Lease{
				ID: "guest-lease", Subject: vmlease.Subject{Kind: vmlease.SubjectUser, ID: scenario.owner, OrgID: scenario.tenant},
				Resource:     vmlease.ResourceRef{ProviderID: "proxmox", EngineVMID: "home/2000"},
				CustodyClass: vmlease.CustodyCustomerSubstrate, BillingMode: vmlease.BillingModeLocal,
				LifecycleClass: vmlease.LifecycleClassOneTime, RecreatePolicy: vmlease.RecreatePolicyNever,
				DesiredState: vmlease.DesiredStateRunning,
				Metadata:     map[string]string{"stack_id": scenario.deployment, "runtime_offering_id": "ubuntu-24.04"},
			}
			orch := New(&Config{Workers: 1, WorkDir: t.TempDir(), StackStore: store, JobStore: store,
				LeaseLister: fakeManagedRuntimeLeaseLister{records: []vmleases.LeaseInventoryRecord{{
					Lease: lease, ExecutionAuthority: vmleases.LeaseExecutionAuthorityTechStackProviderControl,
					AuthorityState: vmleases.LeaseAuthorityStateNativeActive,
				}}},
			}, nil)
			defer orch.Stop()
			jobID, err := orch.DestroyStackWithOptions("deployment-1", ProvisionStackOptions{TenantID: "tenant-1", OwnerID: "owner-1"})
			if err != nil {
				t.Fatal(err)
			}
			job, err := orch.GetJobStatus(jobID)
			if err != nil {
				t.Fatal(err)
			}
			probe := &customerGuestCleanupProbe{pending: errors.New("native absence not yet observed")}
			handler := jobs.DestroyHandler(&jobs.ProvisionConfig{WorkDir: t.TempDir(), RuntimeActions: jobs.RuntimeActions{LeaseDecommissioner: probe}})
			err = handler(ctx, job, orch.queue)
			if !scenario.required {
				if probe.request != nil {
					t.Fatalf("foreign lease triggered native cleanup: %#v", probe.request)
				}
				return
			}
			if probe.request == nil {
				t.Fatal("customer guest bypassed native decommission")
			}
			if probe.request.TenantID != "tenant-1" || probe.request.OwnerID != "owner-1" || probe.request.StackID != "deployment-1" {
				t.Fatalf("native cleanup lost owner scope: %#v", probe.request)
			}
			if !errors.Is(err, probe.pending) {
				t.Fatalf("destroy did not retain native cleanup failure: %v", err)
			}
		})
	}
}
