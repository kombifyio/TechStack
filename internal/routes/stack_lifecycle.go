package routes

import (
	"context"

	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	jobruntime "github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/vmleases"
	"github.com/pocketbase/pocketbase/core"
)

const managedRuntimeDetailsRetryableKey = "retryable"

type stackLifecycleLeaseService interface {
	Get(context.Context, string, vmlease.LeaseID) (*vmlease.Lease, error)
	ListByTenant(context.Context, string) ([]vmlease.Lease, error)
	Patch(context.Context, string, vmlease.LeaseID, vmleases.PatchRequest) (*vmlease.Lease, error)
}

type stackLifecycleRuntimeClient interface {
	RuntimeAction(context.Context, serverruntime.LeaseRuntimeActionRequest) (*serverruntime.LeaseRuntimeActionResponse, error)
}

type stackLifecycleRouteHandlers struct {
	app            core.App
	leases         stackLifecycleLeaseService
	runtime        stackLifecycleRuntimeClient
	features       monthlyruntime.FeatureChecker
	stacks         controlplane.StackStore
	workers        controlplane.WorkerStore
	jobs           controlplane.JobStore
	managedLeases  jobruntime.ManagedLeaseManager
	decommissioner jobruntime.ManagedLeaseDecommissioner
}

type StackLifecycleStores struct {
	Stacks         controlplane.StackStore
	Workers        controlplane.WorkerStore
	Jobs           controlplane.JobStore
	Registry       controlplane.ServerRuntimeStore
	ManagedLeases  jobruntime.ManagedLeaseManager
	Decommissioner jobruntime.ManagedLeaseDecommissioner
}

func RegisterStackLifecycleRoutesWithStores(r *httpx.Router, app core.App, leases stackLifecycleLeaseService, runtime stackLifecycleRuntimeClient, featureChecker monthlyruntime.FeatureChecker, stores StackLifecycleStores) {
	if r == nil || app == nil {
		return
	}
	h := stackLifecycleRouteHandlers{app: app, leases: leases, runtime: runtime, features: featureChecker, stacks: stores.Stacks, workers: stores.Workers, jobs: stores.Jobs, managedLeases: stores.ManagedLeases, decommissioner: stores.Decommissioner}
	r.POST("/api/v1/stacks/prune-orphans", h.pruneOrphanStacks)
}
