//nolint:goconst // moved unchanged from deploy_handler.go; repeated field names predate the split
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// managedInventoryKit reports whether the kit's managed rollout hands the host
// a Core-built Inventory: Cloud Kit binds its one Cloud channel, Modern
// Homelab binds one channel per node.
func managedInventoryKit(stackKit string) bool {
	switch strings.ToLower(strings.TrimSpace(stackKit)) {
	case "cloud-kit", "modern-homelab":
		return true
	}
	return false
}

// managedInventoryNodes lists the channel of every node of a multi-node kit,
// so Home's Apply finds a channel for each plan node instead of refusing the
// Cloud node as `execution-channel-unbound`. A one-node kit has none: its
// Inventory is the single Cloud channel.
func managedInventoryNodes(stackKit string) ([]ManagedStackKitNodeChannel, error) {
	if !managedInventoryKit(stackKit) {
		return nil, nil
	}
	nodes, err := localExecutionNodes(stackKit)
	if err != nil {
		return nil, err
	}
	if len(nodes) < 2 {
		return nil, nil
	}
	channels := make([]ManagedStackKitNodeChannel, 0, len(nodes))
	for _, node := range nodes {
		channels = append(channels, ManagedStackKitNodeChannel{ChannelRef: node.ExecutionChannelRef, SiteRef: node.SiteRef, NodeRef: node.NodeRef})
	}
	return channels, nil
}

const managedBackupTargetInventoryResultField = "managed_backup_target_inventory"

// buildManagedStackKitInventory always binds a managed Cloud rollout to the
// enrolled Operations process. The Inventory it returns is channel-only: when
// the plan selects the off-site backup capability, the node issues its own
// backup-target binding from sealed credentials the returned delivery authority
// moves to it, so this helper never builds, signs or attests a binding. A
// missing delivery authority for a selected capability fails closed.
func (r *deployRollout) buildManagedStackKitInventory(
	ctx context.Context,
	resolvedPlan []byte,
) ([]byte, ManagedBackupTargetDelivery, error) {
	required, err := managedBackupTargetInventoryRequired(resolvedPlan)
	if err != nil {
		r.recordManagedBackupTargetInventory("failed", "backup_target_requirements_invalid", false)
		return nil, nil, fmt.Errorf("inspect managed Cloud backup target requirements: %w", err)
	}
	if !required {
		r.recordManagedBackupTargetInventory("not_applicable", "optional_backup_not_selected", false)
	}
	if r == nil || r.cfg == nil || r.job == nil || r.cfg.ManagedStackKitInventory == nil {
		if required {
			r.recordManagedBackupTargetInventory("failed", "managed_backup_inventory_unavailable", false)
			return nil, nil, fmt.Errorf("managed Cloud StackKits Inventory authority is not configured for the selected backup capability")
		}
		return nil, nil, fmt.Errorf("managed Cloud StackKits Operations channel Inventory authority is not configured")
	}
	var delivery ManagedBackupTargetDelivery
	if required {
		if delivery = managedBackupDeliveryOf(r.cfg.ManagedStackKitInventory); delivery == nil {
			r.recordManagedBackupTargetInventory("failed", "managed_backup_delivery_unavailable", false)
			return nil, nil, fmt.Errorf("managed Cloud backup delivery authority is not configured for the selected backup capability")
		}
	}
	var nodes []ManagedStackKitNodeChannel
	if r.unifiedSpec != nil {
		if nodes, err = managedInventoryNodes(r.unifiedSpec.StackKit); err != nil {
			return nil, nil, err
		}
	}
	inventory, err := r.cfg.ManagedStackKitInventory.Build(ctx, ManagedStackKitInventoryRequest{
		TenantID: r.actionReq.TenantID, StackID: r.job.TargetID, ResolvedPlan: resolvedPlan,
		Nodes: nodes,
	})
	if err != nil {
		if !required {
			return nil, nil, fmt.Errorf("build managed Cloud StackKits Operations channel Inventory: %w", err)
		}
		r.recordManagedBackupTargetInventory("failed", managedBackupTargetInventoryFailureReason(err), managedBackupTargetInventoryRetryable(err))
		return nil, nil, fmt.Errorf("build managed Cloud StackKits Inventory: %w", err)
	}
	if required {
		r.recordManagedBackupTargetInventory("ready", "", false)
		r.projectBackupSchedule(ctx, resolvedPlan)
	}
	return inventory, delivery, nil
}

// projectBackupSchedule records the cadence the plan selected, so the control
// plane can later tell that this stack is due. It runs only after the inventory
// bound successfully: projecting a schedule for a stack whose backup target
// failed to attest would queue runs against a repository that does not exist.
//
// A projection failure never fails the deploy. The rollout already succeeded
// and the backup target is bound; losing the cadence delays backups until the
// next deploy, while failing here would roll back a healthy deployment over a
// scheduling record.
func (r *deployRollout) projectBackupSchedule(ctx context.Context, resolvedPlan []byte) {
	if r == nil || r.cfg == nil || r.cfg.BackupScheduleProjector == nil || r.job == nil {
		return
	}
	projection, found, err := backupScheduleFromResolvedPlan(resolvedPlan)
	if err != nil {
		r.recordBackupScheduleProjection("failed", "backup_schedule_invalid")
		return
	}
	if !found {
		// A backup target without a schedule is a manual-only repository, not
		// a defect: nothing is due until someone asks.
		r.recordBackupScheduleProjection("not_applicable", "no_schedule_selected")
		return
	}
	projection.TenantID = strings.TrimSpace(r.actionReq.TenantID)
	projection.StackID = strings.TrimSpace(r.job.TargetID)
	projection.OwnerID = strings.TrimSpace(r.actionReq.OwnerID)
	projection.StackName = strings.TrimSpace(r.job.TargetName)
	if err := r.cfg.BackupScheduleProjector(ctx, projection); err != nil {
		r.recordBackupScheduleProjection("failed", "backup_schedule_not_persisted")
		return
	}
	r.recordBackupScheduleProjection("ready", "")
}

func (r *deployRollout) recordBackupScheduleProjection(status, reasonCode string) {
	if r == nil || r.job == nil {
		return
	}
	r.job.mutateResult(func(result map[string]interface{}) {
		result["managed_backup_schedule"] = map[string]interface{}{
			"status": status, "reason_code": reasonCode,
		}
	})
}

func managedBackupTargetInventoryRequired(resolvedPlan []byte) (bool, error) {
	if len(resolvedPlan) == 0 || len(resolvedPlan) > 32<<20 {
		return false, errors.New("managed Cloud ResolvedPlan must be a bounded document")
	}
	var plan map[string]interface{}
	if err := json.Unmarshal(resolvedPlan, &plan); err != nil || plan == nil {
		if err != nil {
			return false, fmt.Errorf("decode managed Cloud ResolvedPlan: %w", err)
		}
		return false, errors.New("managed Cloud ResolvedPlan must be an object")
	}
	requirements, exists := plan["backupTargetRequirements"]
	if !exists || requirements == nil {
		return false, nil
	}
	sites, ok := requirements.(map[string]interface{})
	if !ok {
		return false, errors.New("managed Cloud backup target requirements must be an object")
	}
	return len(sites) != 0, nil
}

func (r *deployRollout) recordManagedBackupTargetInventory(status, reasonCode string, retryable bool) {
	if r == nil || r.job == nil {
		return
	}
	r.job.mutateResult(func(result map[string]interface{}) {
		result[managedBackupTargetInventoryResultField] = map[string]interface{}{
			"status": status, "reason_code": reasonCode, "retryable": retryable,
		}
	})
}

func managedBackupTargetInventoryFailureReason(err error) string {
	message := strings.ToLower(fmt.Sprint(err))
	if strings.Contains(message, "putobject") || strings.Contains(message, "attest managed backup target") {
		return "backup_target_attestation_failed"
	}
	return "managed_backup_inventory_unavailable"
}

func managedBackupTargetInventoryRetryable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	message := strings.ToLower(fmt.Sprint(err))
	return strings.Contains(message, "timeout") || strings.Contains(message, "temporarily unavailable") || strings.Contains(message, "connection refused")
}

func readManagedResolvedPlan(path string) ([]byte, error) {
	const maxBytes = 32 << 20
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("managed Cloud StackKits Inventory requires the canonical ResolvedPlan")
	}
	file, err := os.Open(path) // #nosec G304 -- path is the pinned generator's canonical ResolvedPlan output.
	if err != nil {
		return nil, fmt.Errorf("open managed Cloud ResolvedPlan: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxBytes {
		return nil, errors.New("managed Cloud ResolvedPlan must be a non-empty bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxBytes {
		return nil, errors.New("read managed Cloud ResolvedPlan within the closed limit")
	}
	return data, nil
}
