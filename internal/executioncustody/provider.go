package executioncustody

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/executionchannel"
	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/middleware"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
)

// Provider transports existing ports only. All admissions, replay decisions,
// claims, provider calls and receipts still belong to the canonical runtime.
type Provider struct {
	Database      *sql.DB
	AuditDatabase *sql.DB
	Runtime       *providercontrol.Runtime
	Actions       jobs.RuntimeActions
	Power         monthlyruntime.PowerController
	Channel       *executionchannel.Channel
	Envelopes     *GatewayAdmission
	BindingBase   []byte
	LocalGrant    func(context.Context, string, string, int64) (*guardbinding.LocalRepairGrant, error)
}

type providerRequest struct {
	Envelope                                                                 *middleware.VerifiedEdgeEnvelope
	Cursor                                                                   string
	Limit                                                                    int
	NativeAdmission                                                          *providercontrol.NativeProvisionAdmissionRequest
	StackID, ServerID                                                        string
	Target                                                                   *jobs.ManagedRuntimeTargetRequest
	Lease                                                                    *jobs.ManagedLeaseRequest
	Decommission                                                             *jobs.ManagedLeaseDecommissionRequest
	Slot                                                                     *jobs.ManagedRuntimeSlotGenerationRequest
	Power                                                                    *monthlyruntime.PowerRequest
	SSH                                                                      *monthlyruntime.SSHAccessRequest
	Reconnect                                                                *monthlyruntime.AgentReconnectRequest
	Discovery                                                                *providercontrol.ProvisionDiscoveryWorkflowRequest
	Resolution                                                               *providercontrol.ProvisionResolutionWorkflowRequest
	TenantID, OwnerID, LeaseID, OperationID, ProviderID, Region, IncidentKey string
}
type providerReply struct {
	Value       json.RawMessage
	Error       string
	Kind        string
	WaitReason  string
	ResumeAfter time.Duration
	Domain      json.RawMessage
}

func (p *Provider) scopeLease(ctx context.Context, grant CallerGrant, tenant, lease string, expectedGeneration ...string) error {
	if tenant != grant.TenantID || lease == "" {
		return errors.New("execution lease scope denied")
	}
	tx, err := db.BeginTenantTransaction(ctx, p.Database, tenant)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner, stack, generation string
	err = tx.QueryRowContext(ctx, `SELECT l.owner_subject_id,s.stack_id,l.resource_generation_id::text FROM techstack_vm_leases l JOIN servers s ON s.tenant_id=l.tenant_id AND s.id=l.server_id AND s.lease_id=l.id WHERE l.tenant_id=$1 AND l.id=$2`, tenant, lease).Scan(&owner, &stack, &generation)
	if err != nil || owner != grant.OwnerID {
		return errors.New("execution lease custody denied")
	}
	// Stack ownership belongs to the existing application pool. The provider
	// role deliberately cannot read stacks and receives no broader grant.
	if err := p.scopeDeployment(ctx, grant, tenant, stack, owner); err != nil {
		return err
	}
	if len(expectedGeneration) > 0 && (expectedGeneration[0] == "" || expectedGeneration[0] != generation) {
		return errors.New("execution lease generation differs from admitted action")
	}
	return nil
}

func (p *Provider) scopeOperation(ctx context.Context, grant CallerGrant, tenant, operation string) (providercontrol.OperationRecord, error) {
	if tenant != grant.TenantID {
		return providercontrol.OperationRecord{}, errors.New("execution operation tenant denied")
	}
	record, err := p.Runtime.LoadProvisionResolutionOperation(ctx, tenant, operation)
	if err != nil {
		return record, err
	}
	return record, p.scopeLease(ctx, grant, tenant, record.Command.LeaseID)
}

func (p *Provider) serve(w http.ResponseWriter, r *http.Request, grant CallerGrant) {
	if p.Database == nil || p.Runtime == nil || p.Actions.LeaseManager == nil || p.Actions.LeaseDecommissioner == nil || p.Power == nil || p.Channel == nil {
		http.Error(w, "native execution unavailable", http.StatusServiceUnavailable)
		return
	}
	var request providerRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(w, "execution request invalid", http.StatusBadRequest)
		return
	}
	var value any
	var err error
	deny := func() { http.Error(w, "execution scope denied", http.StatusForbidden) }
	ctx := r.Context()
	if request.Envelope != nil {
		if p.Envelopes == nil {
			deny()
			return
		}
		original, parseErr := url.ParseRequestURI(request.Envelope.RequestURI)
		if parseErr != nil {
			deny()
			return
		}
		if strings.HasPrefix(original.Path, "/api/v1/monthly-runtimes/") && strings.HasSuffix(original.Path, "/recreate") {
			lease := strings.TrimSuffix(strings.TrimPrefix(original.Path, "/api/v1/monthly-runtimes/"), "/recreate")
			if request.Lease == nil || p.scopeLease(ctx, grant, grant.TenantID, lease) != nil {
				deny()
				return
			}
			tx, lookupErr := db.BeginTenantTransaction(ctx, p.Database, grant.TenantID)
			if lookupErr != nil {
				deny()
				return
			}
			var sameStack bool
			lookupErr = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM servers WHERE tenant_id=$1 AND lease_id=$2 AND stack_id=$3 AND owner_subject_id=$4)`, grant.TenantID, lease, request.Lease.StackID, grant.OwnerID).Scan(&sameStack)
			_ = tx.Rollback()
			if lookupErr != nil || !sameStack {
				deny()
				return
			}
		}
		ctx, err = p.Envelopes.admit(ctx, grant, r.URL.Path, request)
		if err != nil {
			deny()
			return
		}
	}
	switch strings.TrimPrefix(r.URL.Path, remotePath) {
	case "provider/provision-waits":
		value, err = p.Runtime.Ledger().ListProviderProvisionWaitsForTenant(ctx, grant.TenantID, request.Cursor, request.Limit)
	case "provider/decommission-waits":
		if request.Limit < 1 || request.Limit > 100 || (request.Cursor != "" && request.Cursor != grant.TenantID) {
			deny()
			return
		}
		page := providercontrol.RunnableTenantPage{TenantIDs: []string{}}
		if request.Cursor == "" {
			var due bool
			due, err = p.Runtime.Ledger().DueProviderDecommissionTenant(ctx, grant.TenantID)
			if due {
				page.TenantIDs = append(page.TenantIDs, grant.TenantID)
			}
		}
		value = page
	case "provider/preflight", "provider/create":
		if request.Lease == nil || request.Lease.TenantID != grant.TenantID || request.Lease.OwnerID != grant.OwnerID || request.Lease.OwnerID == "" {
			deny()
			return
		}
		if grant.DeploymentAdmission != "owned-records" && !grant.admits(request.Lease.TenantID, request.Lease.StackID, request.Lease.OwnerID) {
			deny()
			return
		}
		if strings.HasSuffix(r.URL.Path, "preflight") {
			preflight, ok := p.Actions.LeaseManager.(jobs.ManagedLeaseAdmissionPreflighter)
			if !ok {
				err = errors.New("native preflight unavailable")
			} else {
				err = preflight.PreflightCreateOrBindLease(ctx, *request.Lease)
			}
		} else {
			if p.scopeDeployment(ctx, grant, request.Lease.TenantID, request.Lease.StackID, request.Lease.OwnerID) != nil {
				deny()
				return
			}
			value, err = p.Actions.LeaseManager.CreateOrBindLease(ctx, *request.Lease)
		}
	case "provider/slot":
		if request.Slot == nil || p.scopeDeployment(ctx, grant, request.Slot.TenantID, request.Slot.StackID, grant.OwnerID) != nil {
			deny()
			return
		}
		resolver, ok := p.Actions.LeaseManager.(jobs.ManagedRuntimeSlotGenerationResolver)
		if !ok {
			err = errors.New("native slot resolver unavailable")
		} else {
			value, err = resolver.ResolveManagedRuntimeSlotGeneration(ctx, *request.Slot)
		}
	case "provider/target":
		if request.Target == nil || p.scopeDeployment(ctx, grant, request.Target.TenantID, request.Target.StackID, request.Target.OwnerID) != nil || p.scopeLease(ctx, grant, request.Target.TenantID, request.Target.LeaseID) != nil {
			deny()
			return
		}
		var target *jobs.ManagedRuntimeTarget
		target, err = p.Channel.Targets.ResolveManagedRuntimeTarget(ctx, *request.Target)
		if err == nil && target != nil {
			// Only the address projection crosses the seam. In particular no
			// node private key, password or provider-local file path is returned.
			value = &jobs.ManagedRuntimeTarget{Host: target.Host, PublicIP: target.PublicIP, PrivateIP: target.PrivateIP, SSHUser: target.SSHUser, SSHPort: target.SSHPort, SSHHostKey: target.SSHHostKey, Source: "native-execution"}
		}
	case "provider/prepare-target":
		if request.Target == nil || p.scopeDeployment(ctx, grant, request.Target.TenantID, request.Target.StackID, request.Target.OwnerID) != nil || p.scopeLease(ctx, grant, request.Target.TenantID, request.Target.LeaseID) != nil {
			deny()
			return
		}
		value, err = p.Channel.PrepareManagedRuntimeTarget(ctx, *request.Target)
	case "provider/decommission":
		if request.Decommission == nil || request.Decommission.TenantID != grant.TenantID || request.Decommission.OwnerID != grant.OwnerID || (request.Decommission.StackID == "" && request.Decommission.LeaseID == "") {
			deny()
			return
		}
		if request.Decommission.StackID != "" && p.scopeDeployment(ctx, grant, request.Decommission.TenantID, request.Decommission.StackID, request.Decommission.OwnerID) != nil {
			deny()
			return
		}
		if request.Decommission.LeaseID != "" && p.scopeLease(ctx, grant, request.Decommission.TenantID, request.Decommission.LeaseID) != nil {
			deny()
			return
		}
		value, err = p.Actions.LeaseDecommissioner.DecommissionManagedLeases(ctx, *request.Decommission)
	case "provider/ready":
		ready, ok := p.Actions.LeaseDecommissioner.(interface{ ManagedLeaseDecommissionReady(context.Context) error })
		if !ok {
			err = errors.New("native decommission unavailable")
		} else {
			err = ready.ManagedLeaseDecommissionReady(ctx)
		}
	case "provider/verify-customer-guest":
		if p.scopeDeployment(ctx, grant, request.TenantID, request.StackID, request.OwnerID) != nil || p.scopeLease(ctx, grant, request.TenantID, request.LeaseID) != nil {
			deny()
			return
		}
		verifier, ok := p.Actions.LeaseManager.(interface {
			VerifyCustomerGuest(context.Context, string, string, string, string, string, string) error
		})
		if !ok {
			err = errors.New("native customer guest verifier unavailable")
		} else {
			err = verifier.VerifyCustomerGuest(ctx, request.TenantID, request.OwnerID, request.StackID, request.LeaseID, request.ServerID, request.OperationID)
		}
	case "provider/admit-customer-guest":
		if request.NativeAdmission == nil || p.scopeDeployment(ctx, grant, request.NativeAdmission.TenantID, request.NativeAdmission.Server.StackID, request.NativeAdmission.OwnerSubjectID) != nil {
			deny()
			return
		}
		admission, ok := p.Actions.LeaseManager.(interface {
			AdmitSubstrateGuest(context.Context, providercontrol.NativeProvisionAdmissionRequest) (providercontrol.NativeProvisionAdmissionResult, error)
		})
		if !ok {
			err = errors.New("native customer guest admission unavailable")
		} else {
			value, err = admission.AdmitSubstrateGuest(ctx, *request.NativeAdmission)
		}
	case "provider/cleanup":
		if p.scopeLease(ctx, grant, request.TenantID, request.LeaseID) != nil {
			deny()
			return
		}
		value, err = p.Runtime.ReadManagedRuntimeCleanup(ctx, request.TenantID, vmlease.LeaseID(request.LeaseID))
	case "provider/protection":
		if request.TenantID != grant.TenantID {
			deny()
			return
		}
		value, err = p.Runtime.ReadCreateProtection(ctx, request.TenantID, request.ProviderID, request.Region)
	case "provider/held":
		if request.TenantID != grant.TenantID || request.OwnerID != grant.OwnerID {
			deny()
			return
		}
		value, err = providercontrol.HeldManagedRuntimeCapacity(ctx, p.Database, request.TenantID, request.OwnerID)
	case "provider/operation":
		value, err = p.scopeOperation(ctx, grant, request.TenantID, request.OperationID)
	case "provider/discover":
		if request.Discovery == nil || request.Discovery.OperatorSubjectID != grant.OwnerID {
			deny()
			return
		}
		if _, scopeErr := p.scopeOperation(ctx, grant, request.Discovery.TenantID, request.Discovery.OperationID); scopeErr != nil {
			deny()
			return
		}
		value, err = p.Runtime.DiscoverProvisionOperation(ctx, *request.Discovery)
	case "provider/resolve":
		if request.Resolution == nil || request.Resolution.OperatorSubjectID != grant.OwnerID {
			deny()
			return
		}
		if _, scopeErr := p.scopeOperation(ctx, grant, request.Resolution.TenantID, request.Resolution.OperationID); scopeErr != nil {
			deny()
			return
		}
		value, err = p.Runtime.CommitProvisionOperation(ctx, *request.Resolution)
	case "provider/power":
		if request.Power == nil || request.Power.OwnerID != grant.OwnerID || p.scopeLease(ctx, grant, request.Power.TenantID, request.Power.LeaseID) != nil {
			deny()
			return
		}
		value, err = p.Power.RequestPower(ctx, *request.Power)
	case "provider/latest-power":
		if p.scopeLease(ctx, grant, request.TenantID, request.LeaseID) != nil {
			deny()
			return
		}
		value, err = p.Power.LatestPower(ctx, request.TenantID, request.LeaseID)
	case "provider/ssh-access":
		if request.SSH == nil || request.SSH.OwnerID != grant.OwnerID || request.SSH.Actor != grant.OwnerID || p.scopeLease(ctx, grant, request.SSH.TenantID, request.SSH.LeaseID, request.SSH.ResourceGenerationID) != nil {
			deny()
			return
		}
		value, err = p.Channel.SetOwnerSSHAccess(ctx, *request.SSH)
	case "provider/reconnect":
		if request.Reconnect == nil || request.Reconnect.OwnerID != grant.OwnerID || p.scopeLease(ctx, grant, request.Reconnect.TenantID, request.Reconnect.LeaseID) != nil {
			deny()
			return
		}
		err = p.Channel.RestartAgent(ctx, *request.Reconnect)
	default:
		http.NotFound(w, r)
		return
	}
	reply := providerReply{}
	reply.Value, _ = json.Marshal(value)
	if err != nil {
		// Domain reasons are returned only from the existing redacted error
		// contracts; arbitrary database/provider strings never cross this seam.
		reply.Error = "native execution operation denied"
		encodeProviderError(&reply, err)
	}
	_ = json.NewEncoder(w).Encode(reply)
}

func (client *Remote) providerCall(ctx context.Context, action string, input providerRequest, output any) error {
	if action == "preflight" || action == "create" {
		input.Envelope = middleware.VerifiedEdgeEnvelopeFromContext(ctx)
	}
	var reply providerReply
	if err := client.call(ctx, "provider/"+action, input, &reply); err != nil {
		return err
	}
	if output != nil && len(reply.Value) > 0 && string(reply.Value) != "null" {
		if err := json.Unmarshal(reply.Value, output); err != nil {
			return errors.New("execution response invalid")
		}
	}
	if reply.Error == "" {
		return nil
	}
	return decodeProviderError(reply)
}
