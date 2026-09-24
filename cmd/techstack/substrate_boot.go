package main

import (
	"github.com/kombifyio/techstack/internal/routes"
	"github.com/kombifyio/techstack/internal/routes/stacks"
	"github.com/kombifyio/techstack/internal/substrateprovision"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/orchestrator"
)

func registerSubstrateAppliances(router *httpx.Router, deps routeDeps, manager jobs.ManagedLeaseManager) {
	if deps.v2 == nil || deps.v2.db == nil {
		return
	}
	var provisioner routes.ApplianceProvisioner
	if admission, ok := manager.(substrateprovision.Admission); ok {
		provisioner = substrateprovision.New(deps.v2.db.DB, admission)
	}
	routes.RegisterSubstrateApplianceRoutes(router, deps.v2.db.DB, provisioner)
}

func wizardCustomerGuestProvisioner(deps routeDeps, manager jobs.ManagedLeaseManager) stacks.WizardGuestProvisioner {
	admission, ok := manager.(substrateprovision.Admission)
	if !ok || deps.v2 == nil || deps.v2.db == nil || deps.orch == nil {
		return nil
	}
	service := substrateprovision.New(deps.v2.db.DB, admission)
	return stacks.WizardGuestProvisionerFunc(func(e *httpx.Event, r stacks.WizardGuestProvisionRequest) (*stacks.WizardGuestProvisionResult, error) {
		guest, err := service.Prepare(e.Request.Context(), substrateprovision.Request{TenantID: r.TenantID, OwnerID: r.OwnerID, StackID: r.StackID, NodeID: r.NodeID, IdempotencyKey: r.IdempotencyKey, Placement: substrateprovision.Placement{ServerID: r.Guest.ServerID, ProfileID: r.Guest.ProfileID, Storage: r.Guest.Storage, Bridge: r.Guest.Bridge, CPU: r.Guest.CPU, MemoryMiB: r.Guest.MemoryMiB, DiskGiB: r.Guest.DiskGiB}})
		if err != nil {
			return nil, err
		}
		var appliance *jobs.CustomerGuestBinding
		if a := r.Guest.Appliance; a != nil {
			admitted, prepareErr := service.Prepare(e.Request.Context(), substrateprovision.Request{TenantID: r.TenantID, OwnerID: r.OwnerID, StackID: r.StackID, NodeID: r.NodeID + "-home-assistant", IdempotencyKey: r.IdempotencyKey + "/home-assistant", Placement: substrateprovision.Placement{ServerID: r.Guest.ServerID, ProfileID: "haos", Storage: a.Storage, Bridge: a.Bridge, CPU: a.CPU, MemoryMiB: a.MemoryMiB, DiskGiB: a.DiskGiB}})
			if prepareErr != nil {
				return nil, prepareErr
			}
			appliance = &jobs.CustomerGuestBinding{LeaseID: admitted.LeaseID, ServerID: admitted.ServerID, OperationID: admitted.OperationID}
		}
		jobID, err := deps.orch.ProvisionCustomerGuest(r.StackID, jobs.CustomerGuestBinding{LeaseID: guest.LeaseID, ServerID: guest.ServerID, OperationID: guest.OperationID}, orchestrator.ProvisionStackOptions{RequestContext: e.Request.Context(), TenantID: r.TenantID, OwnerID: r.OwnerID, OwnerSpecBootstrap: r.OwnerSpecBootstrap, IdempotencyKey: "substrate-rollout:" + guest.LeaseID})
		if err != nil {
			return nil, err
		}
		return &stacks.WizardGuestProvisionResult{JobID: jobID, ServerID: guest.ServerID, OperationID: guest.OperationID, Appliance: appliance}, nil
	})
}
