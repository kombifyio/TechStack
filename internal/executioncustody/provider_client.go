package executioncustody

import (
	"context"
	"github.com/kombifyio/techstack/internal/providercatalog"
	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/releasechannel"
)

func (client *Remote) CreateOrBindLease(ctx context.Context, request jobs.ManagedLeaseRequest) (*jobs.ManagedLeaseResult, error) {
	provider, err := providercatalog.CanonicalProviderID(request.Provider)
	if err != nil {
		return nil, err
	}
	request.Provider = provider
	var result *jobs.ManagedLeaseResult
	err = client.providerCall(ctx, "create", providerRequest{Lease: &request}, &result)
	return result, err
}
func (client *Remote) ResolveManagedRuntimeTarget(ctx context.Context, request jobs.ManagedRuntimeTargetRequest) (*jobs.ManagedRuntimeTarget, error) {
	var result *jobs.ManagedRuntimeTarget
	err := client.providerCall(ctx, "target", providerRequest{Target: &request}, &result)
	return result, err
}
func (client *Remote) ResolveManagedRuntimeExecutionTarget(ctx context.Context, request jobs.ManagedRuntimeTargetRequest) (*jobs.ManagedRuntimeTarget, error) {
	return client.ResolveManagedRuntimeTarget(ctx, request)
}

func (client *Remote) PrepareManagedRuntimeTarget(ctx context.Context, request jobs.ManagedRuntimeTargetRequest) (*jobs.RuntimeTargetBootstrapResult, error) {
	var result *jobs.RuntimeTargetBootstrapResult
	err := client.providerCall(ctx, "prepare-target", providerRequest{Target: &request}, &result)
	return result, err
}

func (client *Remote) PreflightCreateOrBindLease(ctx context.Context, request jobs.ManagedLeaseRequest) error {
	provider, err := providercatalog.CanonicalProviderID(request.Provider)
	if err != nil {
		return err
	}
	request.Provider = provider
	if err := client.providerCall(ctx, "preflight", providerRequest{Lease: &request}, nil); err != nil {
		return err
	}
	// Only a successful native capacity preflight may admit the original
	// verified POST in this process's same canonical Stage policy.
	return releasechannel.AdmitStageMutation(ctx, request.Provider)
}
func (client *Remote) ResolveManagedRuntimeSlotGeneration(ctx context.Context, request jobs.ManagedRuntimeSlotGenerationRequest) (jobs.ManagedRuntimeSlotGeneration, error) {
	var result jobs.ManagedRuntimeSlotGeneration
	err := client.providerCall(ctx, "slot", providerRequest{Slot: &request}, &result)
	return result, err
}
func (client *Remote) DecommissionManagedLeases(ctx context.Context, request jobs.ManagedLeaseDecommissionRequest) (*jobs.ManagedLeaseDecommissionResult, error) {
	var result *jobs.ManagedLeaseDecommissionResult
	err := client.providerCall(ctx, "decommission", providerRequest{Decommission: &request}, &result)
	return result, err
}
func (client *Remote) ManagedLeaseDecommissionReady(ctx context.Context) error {
	return client.providerCall(ctx, "ready", providerRequest{}, nil)
}
func (client *Remote) VerifyCustomerGuest(ctx context.Context, tenant, owner, stack, lease, server, operation string) error {
	return client.providerCall(ctx, "verify-customer-guest", providerRequest{TenantID: tenant, OwnerID: owner, StackID: stack, LeaseID: lease, ServerID: server, OperationID: operation}, nil)
}
func (client *Remote) AdmitSubstrateGuest(ctx context.Context, request providercontrol.NativeProvisionAdmissionRequest) (providercontrol.NativeProvisionAdmissionResult, error) {
	var result providercontrol.NativeProvisionAdmissionResult
	err := client.providerCall(ctx, "admit-customer-guest", providerRequest{NativeAdmission: &request}, &result)
	return result, err
}
func (client *Remote) ReadManagedRuntimeCleanup(ctx context.Context, tenant string, lease vmlease.LeaseID) (*monthlyruntime.CleanupReadbackFacts, error) {
	var result *monthlyruntime.CleanupReadbackFacts
	err := client.providerCall(ctx, "cleanup", providerRequest{TenantID: tenant, LeaseID: string(lease)}, &result)
	return result, err
}
func (client *Remote) ReadCreateProtection(ctx context.Context, tenant, provider, region string) (providercontrol.CreateProtection, error) {
	var result providercontrol.CreateProtection
	err := client.providerCall(ctx, "protection", providerRequest{TenantID: tenant, ProviderID: provider, Region: region}, &result)
	return result, err
}
func (client *Remote) HeldManagedServers(ctx context.Context, tenant, owner string) (int, error) {
	var result int
	err := client.providerCall(ctx, "held", providerRequest{TenantID: tenant, OwnerID: owner}, &result)
	return result, err
}
func (client *Remote) LoadProvisionResolutionOperation(ctx context.Context, tenant, operation string) (providercontrol.OperationRecord, error) {
	var result providercontrol.OperationRecord
	err := client.providerCall(ctx, "operation", providerRequest{TenantID: tenant, OperationID: operation}, &result)
	return result, err
}
func (client *Remote) DiscoverProvisionOperation(ctx context.Context, request providercontrol.ProvisionDiscoveryWorkflowRequest) (providercontrol.ProvisionDiscoveryWorkflowResult, error) {
	var result providercontrol.ProvisionDiscoveryWorkflowResult
	err := client.providerCall(ctx, "discover", providerRequest{Discovery: &request}, &result)
	return result, err
}
func (client *Remote) CommitProvisionOperation(ctx context.Context, request providercontrol.ProvisionResolutionWorkflowRequest) (providercontrol.ProvisionResolutionWorkflowResult, error) {
	var result providercontrol.ProvisionResolutionWorkflowResult
	err := client.providerCall(ctx, "resolve", providerRequest{Resolution: &request}, &result)
	return result, err
}
func (client *Remote) RequestPower(ctx context.Context, request monthlyruntime.PowerRequest) (monthlyruntime.PowerOperation, error) {
	var result monthlyruntime.PowerOperation
	err := client.providerCall(ctx, "power", providerRequest{Power: &request}, &result)
	return result, err
}
func (client *Remote) LatestPower(ctx context.Context, tenant, lease string) (*monthlyruntime.PowerOperation, error) {
	var result *monthlyruntime.PowerOperation
	err := client.providerCall(ctx, "latest-power", providerRequest{TenantID: tenant, LeaseID: lease}, &result)
	return result, err
}
func (client *Remote) SetOwnerSSHAccess(ctx context.Context, request monthlyruntime.SSHAccessRequest) (monthlyruntime.SSHAccessResult, error) {
	var result monthlyruntime.SSHAccessResult
	err := client.providerCall(ctx, "ssh-access", providerRequest{SSH: &request}, &result)
	return result, err
}
func (client *Remote) RestartAgent(ctx context.Context, request monthlyruntime.AgentReconnectRequest) error {
	return client.providerCall(ctx, "reconnect", providerRequest{Reconnect: &request}, nil)
}

var (
	_ jobs.ManagedRuntimeTargetPreparer         = (*Remote)(nil)
	_ jobs.ManagedLeaseManager                  = (*Remote)(nil)
	_ jobs.ManagedLeaseAdmissionPreflighter     = (*Remote)(nil)
	_ jobs.ManagedRuntimeSlotGenerationResolver = (*Remote)(nil)
	_ jobs.ManagedLeaseDecommissioner           = (*Remote)(nil)
	_ monthlyruntime.CleanupReadbackSource      = (*Remote)(nil)
	_ monthlyruntime.PowerController            = (*Remote)(nil)
	_ monthlyruntime.SSHAccessController        = (*Remote)(nil)
	_ monthlyruntime.AgentReconnector           = (*Remote)(nil)
)

// Recovery remains in the existing orchestrator. The native authority projects
// only this dedicated caller's exact tenant coordinates.
func (client *Remote) ListProviderProvisionWaits(ctx context.Context, cursor string, limit int) (providercontrol.ProviderProvisionWaitPage, error) {
	var result providercontrol.ProviderProvisionWaitPage
	err := client.providerCall(ctx, "provision-waits", providerRequest{Cursor: cursor, Limit: limit}, &result)
	return result, err
}
func (client *Remote) ListDueProviderDecommissionTenants(ctx context.Context, cursor string, limit int) (providercontrol.RunnableTenantPage, error) {
	var result providercontrol.RunnableTenantPage
	err := client.providerCall(ctx, "decommission-waits", providerRequest{Cursor: cursor, Limit: limit}, &result)
	return result, err
}

var _ providercontrol.ProviderProvisionWaitSource = (*Remote)(nil)
var _ providercontrol.DueProviderDecommissionTenantSource = (*Remote)(nil)
