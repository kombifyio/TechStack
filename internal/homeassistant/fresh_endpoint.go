package homeassistant

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/kombifyio/techstack/internal/substrate"
	"net"
	"net/url"
	"time"
)

// VerifyNativeGuestEndpoint accepts only the native HA API on a literal address
// freshly observed by the enrolled Guard for this exact admitted guest. DNS and
// proxy aliases cannot turn a different original instance into a fresh target.
func (s *EnrollmentStore) VerifyNativeGuestEndpoint(ctx context.Context, tenant, owner, stack, leaseID, endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") {
		return ErrUnauthorized
	}
	// Current HAOS uses the standard HTTP port; older Core installations keep
	// 8123. Admission still binds the exact observed guest, not a redirect.
	port := u.Port()
	if port != "8123" && port != "" && !(u.Scheme == "http" && port == "80") && !(u.Scheme == "https" && port == "443") {
		return ErrUnauthorized
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return ErrUnauthorized
	}
	tx, err := s.transaction(ctx, tenant)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var inventoryRaw, specRaw []byte
	err = tx.QueryRowContext(ctx, `SELECT s.metadata_json->'substrate_inventory',d.spec_json
 FROM substrate_guest_leases g
 JOIN techstack_vm_leases l ON l.tenant_id=g.tenant_id AND l.id=g.lease_id
 JOIN substrate_bindings b ON b.tenant_id=g.tenant_id AND b.server_id=g.substrate_server_id
 JOIN servers s ON s.tenant_id=b.tenant_id AND s.id=b.server_id
 JOIN workers w ON w.tenant_id=b.tenant_id AND w.id=b.worker_id
 JOIN provider_operations o ON o.tenant_id=g.tenant_id AND o.operation_id=g.operation_id AND o.lease_id=g.lease_id
 JOIN provider_desired_spec_revisions d ON d.tenant_id=o.tenant_id AND d.lease_id=o.lease_id AND d.revision=o.desired_spec_revision
 WHERE g.tenant_id=$1 AND g.lease_id=$2 AND l.owner_subject_id=$3 AND l.lease_json #>> '{metadata,stack_id}'=$4
 AND l.lease_json #>> '{metadata,runtime_offering_id}'='haos'
 AND b.enabled AND s.owner_subject_id=$3 AND s.worker_id=b.worker_id AND w.approved AND w.status<>'rejected'
 AND b.revision::text=l.lease_json #>> '{metadata,substrate_binding_revision}'
 AND s.connection_state='connected' AND s.lifecycle_state='active'
 AND s.last_heartbeat_at>clock_timestamp()-interval '2 minutes'
 AND (s.metadata_json->>'inventory_observed_at')::timestamptz>clock_timestamp()-interval '2 minutes'
 AND o.status='succeeded' AND o.phase='present'`, tenant, leaseID, owner, stack).Scan(&inventoryRaw, &specRaw)
	if err != nil {
		return errors.New("fresh authenticated HAOS guest identity unavailable")
	}
	var inventory substrate.GuardObservation
	var spec substrate.ExecutionSpec
	if json.Unmarshal(inventoryRaw, &inventory) != nil || inventory.Validate() != nil || json.Unmarshal(specRaw, &spec) != nil || spec.Action != "provision" || spec.Guest.ProfileID != "haos" {
		return ErrUnauthorized
	}
	guest, ok := inventory.OwnedGuests[spec.Guest.ID]
	if !ok || guest.Identity != spec.Guest.GuestIdentity || guest.SpecDigest != spec.Guest.CreationDigest() || guest.ObservedAt.IsZero() || time.Since(guest.ObservedAt) > 2*time.Minute || guest.ObservedAt.After(time.Now().Add(5*time.Second)) {
		return ErrUnauthorized
	}
	observed := guest.Observation
	if !observed.Present || observed.ID != spec.Guest.ID || observed.Name != spec.Guest.Name || observed.Status != "running" || observed.Locked || !observed.DiskAttached {
		return ErrUnauthorized
	}
	for _, address := range observed.Addresses {
		if native := net.ParseIP(address); native != nil && native.Equal(ip) {
			return nil
		}
	}
	return errors.New("endpoint is not the fresh guest's observed native Home Assistant address")
}
