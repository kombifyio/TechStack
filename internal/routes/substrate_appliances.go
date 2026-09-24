package routes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/internal/substrateprovision"
	"github.com/kombifyio/techstack/pkg/httpx"
)

type ApplianceProvisioner interface {
	Prepare(context.Context, substrateprovision.Request) (substrateprovision.Result, error)
}

type substrateApplianceRoutes struct {
	db          *sql.DB
	provisioner ApplianceProvisioner
}

// Appliances share native guest admission, but never start Ubuntu enrollment
// or a StackKits installation inside the appliance.
func RegisterSubstrateApplianceRoutes(r *httpx.Router, db *sql.DB, provisioner ApplianceProvisioner) {
	h := substrateApplianceRoutes{db: db, provisioner: provisioner}
	r.POST("/api/v1/substrates/{id}/appliances", h.create)
	r.GET("/api/v1/substrates/{id}/appliances/{lease}", h.get)
}

func (h substrateApplianceRoutes) create(e *httpx.Event) error {
	owner, err := requireAuth(e)
	if err != nil {
		return err
	}
	tenant, err := requireRegistryRouteTenant(e, owner, "techstack.substrates.manage")
	if err != nil {
		return err
	}
	key, err := readManagedRuntimeIdempotencyKey(e.Request)
	if err != nil {
		return httpx.BadRequest(e, "A bounded Idempotency-Key is required", nil)
	}
	var body struct {
		StackID   string                       `json:"stack_id"`
		NodeID    string                       `json:"node_id"`
		Placement substrateprovision.Placement `json:"placement"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(e.Response, e.Request.Body, 8192))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF || body.Placement.ProfileID != "haos" || body.Placement.ServerID != e.Request.PathValue("id") {
		return httpx.BadRequest(e, "An explicit HAOS placement on this substrate is required", nil)
	}
	if h.provisioner == nil {
		return httpx.Error(e, 503, "substrate_provisioning_unavailable", "Native guest admission is unavailable", nil)
	}
	result, err := h.provisioner.Prepare(e.Request.Context(), substrateprovision.Request{TenantID: tenant, OwnerID: owner, StackID: body.StackID, NodeID: body.NodeID, IdempotencyKey: key, Placement: body.Placement})
	if errors.Is(err, providercontrol.ErrInvalidRequest) {
		return httpx.BadRequest(e, "Invalid appliance placement", nil)
	}
	if err != nil {
		return httpx.Error(e, 409, "appliance_not_admitted", "The placement could not be admitted; retain the same request when retrying", nil)
	}
	return httpx.Success(e, http.StatusAccepted, map[string]any{"guest": result, "application_status": "authenticated_check_pending"})
}

func (h substrateApplianceRoutes) get(e *httpx.Event) error {
	tx, tenant, owner, err := (substrateRoutes{db: h.db}).scope(e)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var guest substrateprovision.Result
	var status, phase string
	err = tx.QueryRowContext(e.Request.Context(), `SELECT g.lease_id,g.server_id,g.operation_id,g.guest_id,l.lease_json #>> '{metadata,runtime_offering_id}',o.status,o.phase
 FROM substrate_guest_leases g JOIN techstack_vm_leases l ON l.tenant_id=g.tenant_id AND l.id=g.lease_id
 JOIN provider_operations o ON o.tenant_id=g.tenant_id AND o.operation_id=g.operation_id
 WHERE g.tenant_id=$1 AND g.substrate_server_id=$2 AND g.lease_id=$3 AND l.owner_subject_id=$4
 AND l.lease_json #>> '{metadata,runtime_offering_id}'='haos'`, tenant, e.Request.PathValue("id"), e.Request.PathValue("lease"), owner).Scan(&guest.LeaseID, &guest.ServerID, &guest.OperationID, &guest.GuestID, &guest.ProfileID, &status, &phase)
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.NotFound(e, "Appliance not found")
	}
	if err != nil {
		return httpx.NewInternalServerError("Appliance state unavailable", nil)
	}
	return httpx.Success(e, http.StatusOK, map[string]any{"guest": guest, "status": status, "phase": phase, "application_status": "not_assessed_by_substrate"})
}
