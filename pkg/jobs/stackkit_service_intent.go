package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// Declared application components retain their identities; a control key
// changes the desired state of the same canonical application on one server.
func captureDeclaredServiceIntent(ctx context.Context, cfg StackKitLifecycleConfig, req StackKitLifecycleRequest) ([]controlplane.ServiceRuntime, error) {
	if req.ServiceID == "" || (req.Operation != StackKitLifecycleServiceStart && req.Operation != StackKitLifecycleServiceStop) {
		return nil, nil
	}
	if cfg.Services == nil || cfg.ServiceRuntimeAuthorize == nil {
		return nil, fmt.Errorf("service aggregate authority is unavailable")
	}
	anchor, err := cfg.Services.GetServiceRuntime(ctx, req.TenantID, req.ServiceID)
	if err != nil {
		return nil, err
	}
	if anchor == nil || anchor.StackID != req.StackID || anchor.ServerID == "" || !declaredServiceControlMember(*anchor, req.ServiceKey) {
		return nil, fmt.Errorf("service control target changed")
	}
	if err := cfg.ServiceRuntimeAuthorize(ctx, req, anchor.ServerID); err != nil {
		return nil, err
	}
	rows, err := cfg.Services.ListServiceRuntimes(ctx, req.TenantID, req.StackID, anchor.ServerID)
	if err != nil {
		return nil, err
	}
	var control *controlplane.ServiceRuntime
	for i := range rows {
		row := &rows[i]
		if row.TenantID == req.TenantID && row.StackID == req.StackID && row.ServerID == anchor.ServerID && row.ServiceKey == req.ServiceKey && declaredServiceControlMember(*row, req.ServiceKey) {
			if control != nil {
				return nil, fmt.Errorf("declared service control is ambiguous")
			}
			control = row
		}
	}
	if control == nil {
		return nil, fmt.Errorf("declared service control is unavailable")
	}
	if anchor.ID != control.ID && !sameServiceControlRevision(*anchor, *control) {
		return nil, fmt.Errorf("service component control revision changed")
	}
	var captured []controlplane.ServiceRuntime
	anchorCaptured := false
	for _, row := range rows {
		if row.TenantID != req.TenantID || row.StackID != req.StackID || row.ServerID != anchor.ServerID || !declaredServiceControlMember(row, req.ServiceKey) {
			continue
		}
		if row.ID != control.ID && !sameServiceControlRevision(row, *control) {
			continue
		}
		if row.MutationLock.Locked() {
			return nil, fmt.Errorf("declared service control group is locked")
		}
		captured = append(captured, row)
		anchorCaptured = anchorCaptured || row.ID == anchor.ID
	}
	if !anchorCaptured {
		return nil, fmt.Errorf("declared service control group is unavailable")
	}
	return captured, nil
}

func sameServiceControlRevision(component, control controlplane.ServiceRuntime) bool {
	left, _ := json.Marshal(component.Metadata["inventory_revision"])
	right, _ := json.Marshal(control.Metadata["inventory_revision"])
	var componentRevision, controlRevision int64
	_ = json.Unmarshal(left, &componentRevision)
	_ = json.Unmarshal(right, &controlRevision)
	return componentRevision > 0 && componentRevision == controlRevision
}

func declaredServiceControlMember(row controlplane.ServiceRuntime, key string) bool {
	if row.Source != serviceregistry.SourceStackKitsInventory || row.ManagementState != "managed" {
		return false
	}
	if row.ServiceKey == key {
		return true
	}
	identity, _ := row.Metadata["runtime_identity"].(map[string]any)
	return stringFromInterface(row.Metadata["application_key"]) == key && stringFromInterface(identity["container_name"]) != ""
}

func projectDeclaredServiceIntent(ctx context.Context, cfg StackKitLifecycleConfig, jobID string, req StackKitLifecycleRequest, captured []controlplane.ServiceRuntime, evidence stackkitcommand.ServiceActionEvidence) error {
	if len(captured) > 0 {
		if err := cfg.ServiceRuntimeAuthorize(ctx, req, captured[0].ServerID); err != nil {
			return err
		}
	}
	heads := make([]*controlplane.ServiceEventResult, 0, len(captured))
	for _, before := range captured {
		head, err := cfg.Services.ReadServiceEventHead(ctx, req.TenantID, before.ID)
		if err != nil {
			return err
		}
		current := head.Service
		if current == nil || current.StackID != before.StackID || current.ServerID != before.ServerID || current.ServiceKey != before.ServiceKey || current.ServiceInstance != before.ServiceInstance || !declaredServiceControlMember(*current, req.ServiceKey) || current.MutationLock.Locked() || current.DesiredState != before.DesiredState || ApplicationIntentRevision(current) != ApplicationIntentRevision(&before) || !reflect.DeepEqual(current.Metadata["runtime_identity"], before.Metadata["runtime_identity"]) {
			return fmt.Errorf("successful service effect retained but service intent changed before projection")
		}
		heads = append(heads, head)
	}
	var control *controlplane.ServiceRuntime
	for _, head := range heads {
		if head.Service.ServiceKey == req.ServiceKey {
			control = head.Service
		}
	}
	for _, head := range heads {
		if control == nil || (head.Service.ID != control.ID && !sameServiceControlRevision(*head.Service, *control)) {
			return fmt.Errorf("successful service effect retained but control group revision changed")
		}
	}
	for _, head := range heads {
		before := head.Service
		_, err := cfg.Services.ApplyServiceEvent(ctx, controlplane.ServiceEvent{
			TenantID: req.TenantID, ServiceID: before.ID, ExpectedRevision: &head.Revision,
			Authority: controlplane.ServiceEventAuthorityControlPlane, Source: "stackkit-service-control", ReasonCode: req.Operation, EnforceIdentityBinding: true,
			Runtime:  controlplane.ServiceRuntime{StackID: before.StackID, ServerID: before.ServerID, ServiceKey: before.ServiceKey, ServiceInstance: before.ServiceInstance, DesiredState: evidence.DesiredState},
			Evidence: map[string]any{"job_id": jobID, "service_key": evidence.ServiceKey, "evidence_ref": evidence.EvidenceRef, "stackkit_instance_id": req.StackKitInstanceID},
		})
		if err != nil {
			return err
		}
	}
	return nil
}
