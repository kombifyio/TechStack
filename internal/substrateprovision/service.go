// Package substrateprovision prepares customer-owned guests through the same
// lease and provider-operation authority used by the rest of Techstack.
package substrateprovision

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/guardbootstrap"
	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/internal/substrate"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
)

type Admission interface {
	AdmitSubstrateGuest(context.Context, providercontrol.NativeProvisionAdmissionRequest) (providercontrol.NativeProvisionAdmissionResult, error)
}

var ErrInsufficientResources = errors.New("insufficient hypervisor resources")

type Placement struct {
	ServerID  string `json:"server_id"`
	ProfileID string `json:"profile_id"`
	Storage   string `json:"storage"`
	Bridge    string `json:"bridge"`
	CPU       int    `json:"cpu"`
	MemoryMiB int    `json:"memory_mib"`
	DiskGiB   int    `json:"disk_gib"`
	Isolated  bool   `json:"isolated,omitempty"`
}
type Request struct {
	TenantID, OwnerID, StackID, NodeID, IdempotencyKey string
	Placement                                          Placement
}
type Result struct {
	LeaseID     string `json:"lease_id"`
	ServerID    string `json:"server_id"`
	OperationID string `json:"operation_id"`
	GuestID     int    `json:"guest_id"`
	ProfileID   string `json:"profile_id"`
}
type Service struct {
	db        *sql.DB
	admission Admission
}

func New(database *sql.DB, admission Admission) *Service { return &Service{database, admission} }

// Get returns the immutable reservation. Readiness is owned by the referenced
// provider operation; a reserved guest is not evidence of a running appliance.
func (s *Service) Get(ctx context.Context, tenantID, ownerID, leaseID string) (Result, error) {
	if s == nil || s.db == nil || tenantID == "" || ownerID == "" || leaseID == "" {
		return Result{}, providercontrol.ErrInvalidRequest
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id',$1,true)`, tenantID); err != nil {
		return Result{}, err
	}
	var result Result
	err = tx.QueryRowContext(ctx, `SELECT g.lease_id,g.server_id,g.operation_id,g.guest_id,l.lease_json #>> '{metadata,runtime_offering_id}'
 FROM substrate_guest_leases g JOIN techstack_vm_leases l ON l.tenant_id=g.tenant_id AND l.id=g.lease_id
 WHERE g.tenant_id=$1 AND g.lease_id=$2 AND l.owner_subject_id=$3`, tenantID, leaseID, ownerID).Scan(&result.LeaseID, &result.ServerID, &result.OperationID, &result.GuestID, &result.ProfileID)
	return result, err
}

// Prepare is replayable across HTTP retries and process restarts. Reservation
// happens before submission; an unknown provider response never selects a new ID.
func (s *Service) Prepare(ctx context.Context, r Request) (Result, error) {
	if s == nil || s.db == nil || s.admission == nil {
		return Result{}, errors.New("substrate provisioning unavailable")
	}
	for _, id := range []string{r.TenantID, r.OwnerID, r.StackID, r.NodeID, r.IdempotencyKey, r.Placement.ServerID} {
		if strings.TrimSpace(id) != id || id == "" || len(id) > 512 || strings.ContainsAny(id, "\x00\r\n") {
			return Result{}, providercontrol.ErrInvalidRequest
		}
	}
	intent, _ := json.Marshal(r)
	hash := sha256.Sum256(intent)
	intentHash := hex.EncodeToString(hash[:])
	requestKey := sha256.Sum256([]byte(r.TenantID + "\x00" + r.OwnerID + "\x00" + r.StackID + "\x00" + r.IdempotencyKey))
	requestKeyHash := hex.EncodeToString(requestKey[:])
	identity := sha256.Sum256([]byte(r.TenantID + "\x00" + r.OwnerID + "\x00" + r.StackID + "\x00" + r.NodeID))
	leaseID := "substrate-" + hex.EncodeToString(identity[:20])
	// The lock spans the separate native admission transaction. It selects only
	// a free slot; the native database unique constraint remains the final fence.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return Result{}, err
	}
	defer conn.Close()
	for _, lock := range []string{"substrate-request:" + requestKeyHash, "substrate-placement:" + r.TenantID + ":" + r.Placement.ServerID} {
		if _, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtextextended($1,0))`, lock); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			return Result{}, err
		}
		defer func() {
			release, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var released bool
			if err := conn.QueryRowContext(release, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, lock).Scan(&released); err != nil || !released {
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}()
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT set_config('app.tenant_id',$1,true)`, r.TenantID); err != nil {
		return Result{}, err
	}
	var allowed bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM stacks WHERE tenant_id=$1 AND id=$2 AND owner_subject_id=$3)`, r.TenantID, r.StackID, r.OwnerID).Scan(&allowed); err != nil || !allowed {
		return Result{}, errors.New("substrate stack ownership unavailable")
	}
	var existing Result
	var storedHash string
	err = tx.QueryRowContext(ctx, `SELECT g.lease_id,g.server_id,g.operation_id,g.guest_id,l.lease_json #>> '{metadata,runtime_offering_id}',l.lease_json #>> '{metadata,substrate_intent_hash}'
 FROM substrate_guest_leases g JOIN techstack_vm_leases l ON l.tenant_id=g.tenant_id AND l.id=g.lease_id
 WHERE g.tenant_id=$1 AND g.lease_id=$2 AND l.owner_subject_id=$3`, r.TenantID, leaseID, r.OwnerID).Scan(&existing.LeaseID, &existing.ServerID, &existing.OperationID, &existing.GuestID, &existing.ProfileID, &storedHash)
	if err == nil {
		if storedHash != intentHash {
			return Result{}, providercontrol.ErrNativeAdmissionConflict
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, err
	}
	var occupied bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM techstack_vm_leases WHERE tenant_id=$1 AND owner_subject_id=$2 AND lease_json #>> '{metadata,substrate_request_key}'=$3)`, r.TenantID, r.OwnerID, requestKeyHash).Scan(&occupied); err != nil {
		return Result{}, err
	}
	if occupied {
		return Result{}, providercontrol.ErrNativeAdmissionConflict
	}
	if r.Placement.ProfileID == "haos" {
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM servers WHERE tenant_id=$1 AND stack_id=$2 AND (node_id=$3 OR metadata_json->>'node_id'=$3) AND lifecycle_state!='decommissioned') OR EXISTS(SELECT 1 FROM nodes WHERE tenant_id=$1 AND stack_id=$2 AND id=$3)`, r.TenantID, r.StackID, r.NodeID).Scan(&occupied); err != nil {
			return Result{}, err
		}
		if occupied {
			return Result{}, providercontrol.ErrNativeAdmissionConflict
		}
	}
	if r.Placement.ProfileID == "ubuntu-24.04" {
		if r.Placement.Isolated {
			return Result{}, errors.New("Ubuntu enrollment requires reachable LAN")
		}
		if _, err := guardbootstrap.NewEnrollmentIssuer(s.db, nil); err != nil {
			return Result{}, err
		}
	}
	var bound, observed substrate.GuardObservation
	var bindingJSON, inventoryJSON []byte
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT b.revision,b.config_json,s.metadata_json->'substrate_inventory'
 FROM substrate_bindings b JOIN servers s ON s.tenant_id=b.tenant_id AND s.id=b.server_id
 WHERE b.tenant_id=$1 AND b.server_id=$2 AND b.enabled AND s.owner_subject_id=$3 AND s.worker_id=b.worker_id
 AND s.metadata_json->>'server_node_role'='substrate' AND s.connection_state='connected' AND s.lifecycle_state='active'
 AND s.last_heartbeat_at>clock_timestamp()-interval '2 minutes'
 AND (s.metadata_json->>'inventory_observed_at')::timestamptz>clock_timestamp()-interval '2 minutes'`, r.TenantID, r.Placement.ServerID, r.OwnerID).Scan(&revision, &bindingJSON, &inventoryJSON)
	if err != nil || json.Unmarshal(bindingJSON, &bound) != nil || json.Unmarshal(inventoryJSON, &observed) != nil || bound.Validate() != nil || observed.Validate() != nil {
		return Result{}, errors.New("current authorized substrate inventory required")
	}
	if bound.Node != observed.Node || bound.MinGuestID != observed.MinGuestID || bound.MaxGuestID != observed.MaxGuestID {
		return Result{}, errors.New("substrate binding differs from Guard")
	}
	if err = reserveUnobservedGuests(ctx, tx, r.TenantID, r.Placement.ServerID, &observed.Inventory); err != nil {
		return Result{}, err
	}
	if err = validatePlacement(r.Placement, observed.Inventory); err != nil {
		return Result{}, err
	}
	image, ok := bound.Images[r.Placement.ProfileID]
	if !ok || image != observed.Images[r.Placement.ProfileID] {
		return Result{}, errors.New("profile image pin unavailable")
	}
	used := map[int]bool{}
	for _, g := range observed.Inventory.Guests {
		used[g.ID] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT guest_id FROM substrate_guest_leases WHERE tenant_id=$1 AND substrate_server_id=$2`, r.TenantID, r.Placement.ServerID)
	if err != nil {
		return Result{}, err
	}
	for rows.Next() {
		var id int
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return Result{}, err
		}
		used[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Result{}, err
	}
	guestID := 0
	for id := bound.MinGuestID; id <= bound.MaxGuestID; id++ {
		if !used[id] {
			guestID = id
			break
		}
	}
	if guestID == 0 {
		return Result{}, errors.New("substrate guest range exhausted")
	}
	if err = tx.Commit(); err != nil {
		return Result{}, err
	}
	guest := substrate.GuestSpec{GuestIdentity: substrate.GuestIdentity{ID: guestID, Name: "kombify-" + hex.EncodeToString(identity[:8]), OperationTag: "kombify-op-" + hex.EncodeToString(identity[:])}, ProfileID: r.Placement.ProfileID, CPU: r.Placement.CPU, MemoryMiB: r.Placement.MemoryMiB, DiskGiB: r.Placement.DiskGiB, Storage: r.Placement.Storage, Bridge: r.Placement.Bridge, Isolated: r.Placement.Isolated, ImageImport: substrate.ImageImport{Image: image, Storage: r.Placement.Storage}, Enrollment: r.Placement.ProfileID == "ubuntu-24.04"}
	desired, err := json.Marshal(substrate.ExecutionSpec{Action: "provision", Guest: guest})
	if err != nil {
		return Result{}, err
	}
	serverID := runtimeidentity.LeaseServerID(leaseID)
	slotKey := "guest-" + hex.EncodeToString(identity[:12])
	result, err := s.admission.AdmitSubstrateGuest(ctx, providercontrol.NativeProvisionAdmissionRequest{
		TenantID: r.TenantID, OwnerSubjectID: r.OwnerID, RuntimeSlotKey: slotKey, RuntimeSlotID: providercontrol.DeriveManagedRuntimeSlotID(r.TenantID, r.StackID, slotKey), RuntimeServerID: serverID, IdempotencyKey: leaseID,
		Lease: vmlease.Lease{ID: vmlease.LeaseID(leaseID), Subject: vmlease.Subject{Kind: vmlease.SubjectUser, ID: r.OwnerID, OrgID: r.TenantID}, Resource: vmlease.ResourceRef{ProviderID: "proxmox", EngineVMID: r.Placement.ServerID + "/" + strconv.Itoa(guestID)}, CustodyClass: vmlease.CustodyCustomerSubstrate, BillingMode: vmlease.BillingModeLocal, LifecycleClass: vmlease.LifecycleClassOneTime, DesiredState: vmlease.DesiredStateRunning, RestartPolicy: vmlease.RestartPolicyNone, RecreatePolicy: vmlease.RecreatePolicyNever, Metadata: map[string]string{
			"runtime_offering_id": r.Placement.ProfileID, "stack_id": r.StackID, "node_id": r.NodeID, "substrate_intent_hash": intentHash, "substrate_request_key": requestKeyHash, providercontrol.SubstrateServerMetadata: r.Placement.ServerID, providercontrol.SubstrateRevisionMetadata: strconv.FormatInt(revision, 10), providercontrol.SubstrateGuestMetadata: strconv.Itoa(guestID)}},
		Server: providercontrol.NativeAdmissionServer{StackID: r.StackID, Name: guest.Name, Metadata: map[string]any{"server_provisioning_mode": "hypervisor", "server_connection_mode": "substrate-guard", "node_id": r.NodeID, "substrate_profile": r.Placement.ProfileID}}, DesiredSpecRef: "desired-spec://techstack/leases/" + leaseID + "/revisions/1", DesiredSpec: desired,
	})
	if err != nil {
		return Result{}, err
	}
	return Result{LeaseID: result.LeaseID, ServerID: result.RuntimeServerID, OperationID: result.Operation.Command.OperationID, GuestID: guestID, ProfileID: r.Placement.ProfileID}, nil
}

// Inventory can lag the previous admitted creation. Deduct its retained request
// until the guest is visible, while the placement lock serializes new requests.
func reserveUnobservedGuests(ctx context.Context, tx *sql.Tx, tenant, server string, inventory *substrate.Inventory) error {
	visible := map[int]bool{}
	for _, guest := range inventory.Guests {
		visible[guest.ID] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT g.guest_id,d.spec_json FROM substrate_guest_leases g
 JOIN servers s ON s.tenant_id=g.tenant_id AND s.id=g.server_id
 JOIN provider_operations o ON o.tenant_id=g.tenant_id AND o.operation_id=g.operation_id
 JOIN provider_desired_spec_revisions d ON d.tenant_id=o.tenant_id AND d.lease_id=o.lease_id AND d.revision=o.desired_spec_revision
 WHERE g.tenant_id=$1 AND g.substrate_server_id=$2 AND s.lifecycle_state!='decommissioned'`, tenant, server)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return err
		}
		if visible[id] {
			continue
		}
		var spec substrate.ExecutionSpec
		if json.Unmarshal(raw, &spec) != nil || spec.Action != "provision" || spec.Guest.ID != id || spec.Guest.CPU < 1 || spec.Guest.MemoryMiB < 1 || spec.Guest.DiskGiB < 1 {
			return errors.New("retained guest resource reservation unavailable")
		}
		inventory.CPU -= spec.Guest.CPU
		deduct := func(value *uint64, amount uint64) {
			if *value < amount {
				*value = 0
			} else {
				*value -= amount
			}
		}
		deduct(&inventory.MemoryAvailable, uint64(spec.Guest.MemoryMiB)*1024*1024)
		for i := range inventory.Storage {
			if inventory.Storage[i].ID == spec.Guest.Storage {
				deduct(&inventory.Storage[i].Available, uint64(spec.Guest.DiskGiB)*1024*1024*1024)
			}
		}
	}
	return rows.Err()
}

func validatePlacement(p Placement, inventory substrate.Inventory) error {
	var profile *substrate.Profile
	for _, candidate := range substrate.Profiles() {
		if candidate.ID == p.ProfileID {
			copy := candidate
			profile = &copy
			break
		}
	}
	if profile == nil || p.CPU < profile.MinCPU || p.CPU > 256 || p.MemoryMiB < profile.MinMemoryMiB || p.MemoryMiB > 1048576 || p.DiskGiB < profile.MinDiskGiB || p.DiskGiB > 65536 {
		return errors.New("guest resources outside profile limits")
	}
	if inventory.Architecture != profile.Architecture || !inventory.HardwareVirtualization {
		return substrate.ErrCapability
	}
	if p.CPU > inventory.CPU || uint64(p.MemoryMiB)*1024*1024 > inventory.MemoryAvailable {
		return ErrInsufficientResources
	}
	storageOK, bridgeOK := false, false
	for _, s := range inventory.Storage {
		if s.ID == p.Storage && s.Active == 1 && s.Available >= uint64(p.DiskGiB)*1024*1024*1024 {
			content := "," + s.Content + ","
			storageOK = strings.Contains(content, ",images,") && strings.Contains(content, ",import,") && (!profile.CloudInit || strings.Contains(content, ",snippets,")) && (!profile.Appliance || strings.Contains(content, ",vztmpl,"))
		}
	}
	for _, n := range inventory.Networks {
		if n.Name == p.Bridge && n.Type == "bridge" && n.Active == 1 {
			bridgeOK = true
		}
	}
	if !storageOK || !bridgeOK {
		return fmt.Errorf("active LAN bridge and suitable image/import storage required")
	}
	return nil
}
