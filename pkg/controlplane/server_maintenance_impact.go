package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/runtimehealth"
)

const ServerMaintenanceImpactDigestKey = "impact_snapshot_digest"

var (
	ErrServerMaintenanceImpactChanged     = errors.New("controlplane: maintenance impact changed; renewed confirmation required")
	ErrServerMaintenanceImpactUnavailable = errors.New("controlplane: complete current maintenance impact unavailable")
)

type MaintenanceImpactStore interface {
	GetServerMaintenanceImpact(context.Context, string, string, string, time.Time) (*ServerMaintenanceImpact, error)
}

type ServerMaintenanceImpact struct {
	OwnerSubjectID    string                           `json:"-"`
	AgentID           string                           `json:"-"`
	StackID           string                           `json:"-"`
	ServerID          string                           `json:"server_id"`
	Action            string                           `json:"action"`
	InventoryRevision int64                            `json:"inventory_revision"`
	SnapshotDigest    string                           `json:"snapshot_digest"`
	Inventory         ServerMaintenanceImpactInventory `json:"inventory"`
	Services          []ServerMaintenanceImpactService `json:"services"`
	Stacks            []ServerMaintenanceImpactStack   `json:"stacks"`
	Confirmable       bool                             `json:"confirmable"`
}

type ServerMaintenanceImpactInventory struct {
	Status      string     `json:"status"`
	ObservedAt  *time.Time `json:"observed_at,omitempty"`
	ReasonCodes []string   `json:"reason_codes"`
}

type ServerMaintenanceImpactService struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	StackID     string   `json:"stack_id,omitempty"`
	State       string   `json:"state"`
	Locked      bool     `json:"locked"`
	ReasonCodes []string `json:"reason_codes"`
}

type ServerMaintenanceImpactStack struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	ReasonCodes []string `json:"reason_codes"`
}

// buildServerMaintenanceImpact projects canonical placement, never guessed
// inter-service dependencies. The digest excludes observation times and sequence
// counters: an unchanged heartbeat cannot invalidate an owner's confirmation.
func buildServerMaintenanceImpact(server ServerRuntime, action string, observedAt *time.Time, services []ServiceRuntime, stacks []Stack, complete bool, at time.Time) *ServerMaintenanceImpact {
	impact := &ServerMaintenanceImpact{
		OwnerSubjectID: server.OwnerSubjectID, AgentID: server.WorkerID, StackID: server.StackID,
		ServerID: server.ID, Action: action, InventoryRevision: server.InventoryRevision,
		Inventory: ServerMaintenanceImpactInventory{Status: "current", ObservedAt: observedAt, ReasonCodes: []string{}},
		Services:  []ServerMaintenanceImpactService{}, Stacks: []ServerMaintenanceImpactStack{},
	}
	if !complete || server.InventoryRevision <= 0 || observedAt == nil {
		impact.Inventory.Status = "incomplete"
		impact.Inventory.ReasonCodes = append(impact.Inventory.ReasonCodes, "inventory_incomplete")
	} else if observedAt.After(at) || at.Sub(*observedAt) > runtimehealth.FreshHeartbeatWindow || server.LastHeartbeatAt == nil || server.LastHeartbeatAt.After(at) || at.Sub(*server.LastHeartbeatAt) > runtimehealth.FreshHeartbeatWindow {
		impact.Inventory.Status = "stale"
		impact.Inventory.ReasonCodes = append(impact.Inventory.ReasonCodes, "inventory_stale")
	}
	impactReason := "reboot_interrupts_services"
	if action == ServerMaintenanceActionUpdate {
		impactReason = "os_update_may_restart_services"
	}
	for _, service := range services {
		if service.ServerID != server.ID {
			continue
		}
		reasons := []string{"service_runs_on_server", impactReason}
		if service.ObservedState == "" || service.ObservedState == "unknown" || service.ObservedState == guardServiceStatePendingVerification {
			reasons = append(reasons, "service_state_unknown")
			impact.Inventory.Status = "incomplete"
			impact.Inventory.ReasonCodes = []string{"inventory_incomplete"}
		}
		if service.MutationLock.Locked() {
			reasons = append(reasons, "service_locked")
		}
		impact.Services = append(impact.Services, ServerMaintenanceImpactService{
			ID: service.ID, Name: service.Name, StackID: service.StackID, State: service.ObservedState,
			Locked: service.MutationLock.Locked(), ReasonCodes: reasons,
		})
	}
	for _, stack := range stacks {
		impact.Stacks = append(impact.Stacks, ServerMaintenanceImpactStack{ID: stack.ID, Name: stack.Name, ReasonCodes: []string{"stack_uses_server", impactReason}})
	}
	sort.Slice(impact.Services, func(i, j int) bool { return impact.Services[i].ID < impact.Services[j].ID })
	sort.Slice(impact.Stacks, func(i, j int) bool { return impact.Stacks[i].ID < impact.Stacks[j].ID })
	impact.Confirmable = impact.Inventory.Status == "current"
	for _, service := range impact.Services {
		if service.Locked {
			impact.Confirmable = false
		}
	}
	if action != ServerMaintenanceActionReboot && action != ServerMaintenanceActionUpdate {
		impact.Confirmable = false
	}
	type serviceMeaning struct{ ID, Management, Placement string }
	meaning := make([]serviceMeaning, 0, len(services))
	for _, service := range services {
		if service.ServerID == server.ID {
			meaning = append(meaning, serviceMeaning{service.ID, service.ManagementState, string(service.Placement.TargetKind)})
		}
	}
	sort.Slice(meaning, func(i, j int) bool { return meaning[i].ID < meaning[j].ID })
	material := struct {
		Schema, Tenant, Owner, Server, Agent, Action string
		Generation                                   int64
		InventoryStatus                              string
		Services                                     []ServerMaintenanceImpactService
		Stacks                                       []ServerMaintenanceImpactStack
		Meaning                                      []serviceMeaning
	}{"kombify-maintenance-impact/v1", server.TenantID, server.OwnerSubjectID, server.ID, server.WorkerID, action, server.Generation, impact.Inventory.Status, impact.Services, impact.Stacks, meaning}
	raw, _ := json.Marshal(material)
	digest := sha256.Sum256(raw)
	impact.SnapshotDigest = "sha256:" + hex.EncodeToString(digest[:])
	return impact
}

func checkServerMaintenanceImpact(job ServerMaintenanceJob, impact *ServerMaintenanceImpact) error {
	if job.Action == ServerMaintenanceActionPlan {
		return nil
	}
	if impact == nil || !impact.Confirmable {
		return ErrServerMaintenanceImpactUnavailable
	}
	if impact.ServerID != job.ServerID || impact.Action != job.Action || impact.OwnerSubjectID != job.OwnerSubjectID || impact.AgentID != job.AgentID || impact.StackID != job.StackID {
		return ErrServerMaintenanceImpactChanged
	}
	expected, _ := job.Result[ServerMaintenanceImpactDigestKey].(string)
	if strings.TrimSpace(expected) == "" || expected != impact.SnapshotDigest {
		return ErrServerMaintenanceImpactChanged
	}
	return nil
}

func (s *MemoryStore) GetServerMaintenanceImpact(_ context.Context, tenantID, serverID, action string, at time.Time) (*ServerMaintenanceImpact, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.serverMaintenanceImpactLocked(tenantID, serverID, action, at)
}

func (s *MemoryStore) serverMaintenanceImpactLocked(tenantID, serverID, action string, at time.Time) (*ServerMaintenanceImpact, error) {
	server, ok := s.servers[serverID]
	if !ok || server.TenantID != tenantID {
		return nil, ErrNotFound
	}
	scope, err := NewOwnerInventoryReadScope(tenantID, server.OwnerSubjectID)
	if err != nil {
		return nil, err
	}
	services, err := s.inventoryServicesLocked(scope, serverID)
	complete := err == nil
	if err != nil && !errors.Is(err, ErrInventoryProjectionPending) {
		return nil, err
	}
	known := []ServiceRuntime{}
	for _, service := range s.serviceRuntime {
		stack, exists := s.stacks[service.StackID]
		if service.TenantID == tenantID && service.ServerID == serverID && exists && stack.TenantID == tenantID && stack.OwnerSubjectID == server.OwnerSubjectID && stack.DeletedAt == nil {
			known = append(known, service)
		}
	}
	var consistent bool
	services, consistent = mergeMaintenanceImpactServices(services, known, server.InventoryRevision)
	complete = complete && consistent
	var observedAt *time.Time
	discoveryComplete := false
	for _, snapshot := range s.serverInventory[serverID] {
		if snapshot.TenantID == tenantID && snapshot.Revision == server.InventoryRevision {
			t := snapshot.ObservedAt.UTC()
			if observedAt == nil || t.After(*observedAt) {
				observedAt = &t
				discoveryComplete = maintenanceDiscoveryComplete(snapshot.Inventory)
			}
		}
	}
	complete = complete && discoveryComplete
	ids := map[string]bool{}
	if server.StackID != "" {
		ids[server.StackID] = true
	}
	for _, service := range services {
		if service.StackID != "" {
			ids[service.StackID] = true
		}
	}
	stacks := []Stack{}
	for id := range ids {
		stack, ok := s.stacks[id]
		if !ok || stack.TenantID != tenantID || stack.OwnerSubjectID != server.OwnerSubjectID || stack.DeletedAt != nil {
			complete = false
			continue
		}
		stacks = append(stacks, stack)
	}
	return buildServerMaintenanceImpact(server, action, observedAt, services, stacks, complete, at.UTC()), nil
}

// Coherent projection is not complete discovery. Older observations cannot
// establish that an empty service set means there is no affected workload.
func maintenanceDiscoveryComplete(inventory map[string]any) bool {
	deployment, _ := inventory["deployment"].(map[string]any)
	envelope, _ := deployment[guardProjectionEnvelopeKey].(map[string]any)
	complete, _ := envelope["discovery_complete"].(bool)
	return envelope["schema"] == guardProjectionSchema && complete
}

// Include every owner-consistent actual placement, even if a narrower current
// projection filters out another stack. Such extra or stale rows are visible
// evidence of incomplete coverage, never evidence of an empty affected node.
func mergeMaintenanceImpactServices(current, known []ServiceRuntime, revision int64) ([]ServiceRuntime, bool) {
	rows := map[string]ServiceRuntime{}
	consistent := true
	for _, service := range current {
		rows[service.ID] = service
	}
	for _, service := range known {
		if _, ok := rows[service.ID]; !ok {
			consistent = false
			rows[service.ID] = service
		}
		if observed, ok := inventoryMetadataInt64(service.Metadata, "inventory_revision"); !ok || observed != revision {
			consistent = false
		}
	}
	out := make([]ServiceRuntime, 0, len(rows))
	for _, service := range rows {
		out = append(out, service)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > 500 {
		out = out[:500]
		consistent = false
	}
	return out, consistent
}
