package executioncustody

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/vmleases"
	"github.com/google/uuid"
)

// The existing native per-worker signer derives the payload independently.
// HTTP supplies no intent, proof boolean, provenance or install grant here.
func (p *Provider) candidateInstallBinding(ctx context.Context, grant CallerGrant, worker *controlplane.Worker, requested *guardbinding.Binding, generation int64) (*guardbinding.Binding, error) {
	deny := errors.New("native candidate install custody denied")
	now := time.Now().UTC()
	command, ok := strings.CutPrefix(requested.CommandID, "runtime-install-")
	parsed, err := uuid.Parse(command)
	if !ok || err != nil || parsed.String() != command || requested.Reoffer || requested.ServerID == "" || requested.AuthorityExpiresAt.After(now.Add(5*time.Minute)) {
		return nil, deny
	}
	server, err := controlplane.NewPostgresStore(p.AuditDatabase).GetServerRuntime(ctx, grant.TenantID, requested.ServerID)
	if err != nil || server == nil || server.TenantID != grant.TenantID || server.OwnerSubjectID != grant.OwnerID || server.WorkerID != worker.ID || server.StackID != worker.StackID || server.LeaseID == "" || server.ID != runtimeidentity.LeaseServerID(server.LeaseID) {
		return nil, deny
	}
	if p.scopeLease(ctx, grant, grant.TenantID, server.LeaseID) != nil {
		return nil, deny
	}
	record, err := vmleases.NewService(vmleases.NewPostgresStore(p.Database), vmleases.ServiceConfig{}).GetInventory(ctx, grant.TenantID, vmlease.LeaseID(server.LeaseID))
	if err != nil || record == nil || !record.NativeActive() || !monthlyruntime.LeaseVisibleToOwner(record.Lease, grant.TenantID, grant.OwnerID) || record.Lease.Metadata["stack_id"] != worker.StackID {
		return nil, deny
	}
	resolver, configured, err := jobs.StageCandidateForStack(ctx, grant.TenantID, worker.StackID)
	if err != nil || !configured {
		return nil, deny
	}
	intent, err := resolver.InstallIntent()
	if err != nil {
		return nil, err
	}
	digest, err := guardbinding.DigestOf(intent)
	if err != nil || digest != requested.PayloadDigest {
		return nil, deny
	}
	return &guardbinding.Binding{CommandID: requested.CommandID, TenantID: grant.TenantID, WorkerID: worker.ID, ServerID: server.ID, Operation: "runtime.install", PayloadDigest: digest, WorkerGeneration: generation, CurrentGeneration: generation, AuthorityExpiresAt: requested.AuthorityExpiresAt.UTC(), ContractVersion: guardbinding.ContractVersion}, nil
}
