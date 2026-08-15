package main

import (
	"context"

	"github.com/kombifyio/techstack/internal/managedstackkit"
	"github.com/kombifyio/techstack/internal/routes"
	storageauth "github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/backupstore"
	"github.com/kombifyio/techstack/pkg/jobs"
)

type managedStackKitInventoryAdapter struct {
	builder *managedstackkit.RolloutInventory
}

func (adapter managedStackKitInventoryAdapter) Build(ctx context.Context, request jobs.ManagedStackKitInventoryRequest) ([]byte, error) {
	return adapter.builder.Build(ctx, managedstackkit.RolloutInventoryRequest{
		TenantID: request.TenantID, StackID: request.StackID, ResolvedPlan: request.ResolvedPlan,
		StackKitsVersion: request.StackKitsVersion, CandidateDigest: request.CandidateDigest, ValidFor: request.ValidFor,
	})
}

func managedStackKitInventoryBuilder(deps routeDeps) jobs.ManagedStackKitInventoryBuilder {
	executableDigest, err := routes.CurrentAgentBinarySHA256()
	if err != nil {
		deps.log.Warn("managed_stackkit_inventory_unavailable", "reason", "operations_binary_unavailable", "error", err)
		return nil
	}
	channelOnlyBuilder, err := managedstackkit.NewRolloutInventory(nil, nil, executableDigest)
	if err != nil {
		deps.log.Warn("managed_stackkit_inventory_unavailable", "reason", "inventory_builder_invalid", "error", err)
		return nil
	}
	if deps.v2 == nil || deps.v2.db == nil || deps.v2.db.DB == nil {
		deps.log.Warn("managed_stackkit_backup_inventory_unavailable", "reason", "database_not_configured")
		return managedStackKitInventoryAdapter{builder: channelOnlyBuilder}
	}
	repository, err := backupstore.NewPostgresCustodyStore(deps.v2.db.DB, storageauth.GetEncryptor())
	if err != nil {
		deps.log.Warn("managed_stackkit_backup_inventory_unavailable", "reason", "encrypted_custody_not_configured", "error", err)
		return managedStackKitInventoryAdapter{builder: channelOnlyBuilder}
	}
	providerConfig, err := backupstore.FromEnv()
	if err != nil {
		deps.log.Warn("managed_stackkit_backup_inventory_unavailable", "reason", "backup_provider_not_configured", "error", err)
		return managedStackKitInventoryAdapter{builder: channelOnlyBuilder}
	}
	provider, err := backupstore.NewClient(providerConfig)
	if err != nil {
		deps.log.Warn("managed_stackkit_backup_inventory_unavailable", "reason", "backup_provider_invalid", "error", err)
		return managedStackKitInventoryAdapter{builder: channelOnlyBuilder}
	}
	custodian, err := backupstore.NewCustodian(provider, repository)
	if err != nil {
		deps.log.Warn("managed_stackkit_backup_inventory_unavailable", "reason", "custodian_not_configured", "error", err)
		return managedStackKitInventoryAdapter{builder: channelOnlyBuilder}
	}
	builder, err := managedstackkit.NewRolloutInventory(custodian, repository, executableDigest)
	if err != nil {
		deps.log.Warn("managed_stackkit_inventory_unavailable", "reason", "inventory_builder_invalid", "error", err)
		return nil
	}
	return managedStackKitInventoryAdapter{builder: builder}
}
