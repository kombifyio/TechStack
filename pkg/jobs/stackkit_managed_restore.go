package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"

	wire "github.com/kombifyio/stackkits/contracts/advancedcapability"
)

const ManagedBackupAdmissionField = "managed_backup_admission"

// prepareManagedRestore refreshes only the exact admitted stack's backup
// authority against the binding the node itself issued. No Inventory or
// credential document is persisted in the job.
func prepareManagedRestore(ctx context.Context, cfg StackKitLifecycleConfig, job *Job, req StackKitLifecycleRequest, release stackkitrelease.Release) (StackKitLifecycleRequest, map[string]interface{}, error) {
	admission, ok := job.Payload[ManagedBackupAdmissionField].(map[string]interface{})
	if !ok || cfg.ManagedStackKitInventory == nil || admission["tenant_id"] != req.TenantID || admission["stack_id"] != req.StackID || admission["owner_id"] != req.OwnerID || admission["agent_id"] != req.AgentID {
		return req, nil, fmt.Errorf("managed restore requires exact admitted backup authority")
	}
	measured, err := time.Parse(time.RFC3339Nano, stringFromInterface(admission["measured_at"]))
	if err != nil || time.Since(measured) > 5*time.Minute || measured.After(time.Now().Add(time.Minute)) {
		return req, nil, fmt.Errorf("managed restore admission expired; request a fresh operation")
	}
	planReq := req
	planReq.Operation = StackKitLifecyclePlan
	planReq.OwnerApproved = false
	command, err := stackKitLifecycleCommand(job.ID+"-current-plan", planReq, release)
	if err != nil {
		return req, nil, err
	}
	result, err := sendStackKitCommandBoundedForTenant(ctx, cfg.Sender, req.TenantID, req.AgentID, command)
	if err != nil {
		return req, nil, err
	}
	plan, planHash, err := stackkitcommand.ManagedBackupPlan(result, req.StackKitInstanceID)
	if err != nil {
		return req, nil, err
	}
	policy, found, err := backupScheduleFromResolvedPlan(plan)
	if err != nil || !found || policy.IncludeContent != boolFromInterface(admission["include_content"]) {
		return req, nil, fmt.Errorf("current backup policy differs from admitted schedule")
	}

	delivery := managedBackupDeliveryOf(cfg.ManagedStackKitInventory)
	if delivery == nil {
		return req, nil, fmt.Errorf("managed backup renewal authority unavailable")
	}
	var original struct {
		Bindings map[string]map[string]map[string]string `json:"externalBackupTargetBindings"`
	}
	if json.Unmarshal(plan, &original) != nil {
		return req, nil, fmt.Errorf("managed backup renewal projection invalid")
	}
	// The applied binding is the one the node issued. Core compares it with the
	// references the node returned when it opened the delivery; it cannot derive
	// them, so a binding that is not the node's own is refused.
	prior := original.Bindings["cloud"]["offsite-object-backup"]
	issued, err := delivery.NodeBinding(ctx, req.TenantID, req.StackID)
	if err != nil {
		return req, nil, err
	}
	if prior["bindingRef"] == "" || prior["backupTargetRef"] == "" || prior["backupTargetRef"] != issued.BackupTargetRef || prior["custodyAttestationRef"] != issued.CustodyAttestationRef {
		return req, nil, fmt.Errorf("managed backup renewal target differs from the node-issued binding")
	}
	fresh, err := delivery.AttestTarget(ctx, req.TenantID, req.StackID)
	if err != nil {
		return req, nil, err
	}
	req.BackupRenewal = &wire.BackupRenewal{AgentID: req.AgentID, BindingHash: prior["bindingHash"], CustodyAttestationRef: prior["custodyAttestationRef"], DeploymentID: req.StackID, FreshAttestationRef: fresh, JobID: job.ID, MeasuredAt: stringFromInterface(admission["measured_at"]), PlanHash: planHash, QuotaBytes: admittedBackupBytes(admission["quota_bytes"]), RepositoryID: "kopia:local:cloud", TargetRef: prior["backupTargetRef"], TenantID: req.TenantID, UsedBytes: admittedBackupBytes(admission["used_bytes"])}
	if err := req.BackupRenewal.Validate(time.Now().UTC()); err != nil {
		return req, nil, err
	}
	proof := map[string]interface{}{}
	for key, value := range admission {
		proof[key] = value
	}
	proof["source_plan_hash"] = planHash
	proof["plan_hash"] = planHash
	proof["spec_path"] = req.SpecPath
	proof["stackkit_instance_id"] = req.StackKitInstanceID
	proof["repository_id"] = "kopia:local:cloud"
	proof["restore_scope"] = "local-staging"
	proof["remote_target_attested"] = true
	proof["binding_hash"] = req.BackupRenewal.BindingHash
	proof["target_ref"] = req.BackupRenewal.TargetRef
	proof["prior_attestation_ref"] = req.BackupRenewal.CustodyAttestationRef
	proof["fresh_attestation_ref"] = req.BackupRenewal.FreshAttestationRef
	// No Inventory, credential or capability is placed in the durable payload.
	return req, proof, nil
}

func admittedBackupBytes(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	parsed, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(parsed, 10)
}

// A successful receipt must acknowledge the exact renewal dispatched, not a
// rollout-time drill or an unrelated staged snapshot.
func verifyManagedDrillReceipt(raw []byte, renewal *wire.BackupRenewal, jobID, capabilityID string) error {
	var report struct {
		Renewal      *wire.BackupRenewal `json:"backupRenewal"`
		DrillID      string              `json:"drillId"`
		CapabilityID string              `json:"capabilityId"`
		AnchorID     string              `json:"anchorId"`
		RestoreID    string              `json:"restoreResultId"`
		Activated    *bool               `json:"activated"`
		Removed      bool                `json:"stagingRemoved"`
	}
	digest := regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	if json.Unmarshal(raw, &report) != nil || report.Renewal == nil || *report.Renewal != *renewal || report.DrillID != jobID || report.CapabilityID != capabilityID || report.Activated == nil || *report.Activated || !report.Removed || !digest.MatchString(report.AnchorID) || !digest.MatchString(report.RestoreID) {
		return fmt.Errorf("managed restore receipt differs from the approved staged drill")
	}
	return nil
}
