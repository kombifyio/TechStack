package routes

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/kombifyio/techstack/internal/substrate"
	"github.com/kombifyio/techstack/pkg/httpx"
)

type substrateRoutes struct{ db *sql.DB }

// RegisterSubstrateRoutes uses the existing tenant/owner boundary and observed Guard inventory.
func RegisterSubstrateRoutes(r *httpx.Router, db *sql.DB) {
	h := substrateRoutes{db: db}
	r.GET("/api/v1/substrates", h.list)
	r.GET("/api/v1/substrates/profiles", h.profiles)
	r.GET("/api/v1/substrates/{id}/inventory", h.inventory)
	r.PUT("/api/v1/substrates/{id}/binding", h.binding)
}
func (h substrateRoutes) scope(e *httpx.Event) (*sql.Tx, string, string, error) {
	owner, err := requireAuth(e)
	if err != nil {
		return nil, "", "", err
	}
	capability := "techstack.registry.read"
	if e.Request.Method == http.MethodPut {
		capability = "techstack.substrates.manage"
	}
	tenant, err := requireRegistryRouteTenant(e, owner, capability)
	if err != nil {
		return nil, "", "", err
	}
	if h.db == nil {
		return nil, "", "", httpx.NewInternalServerError("Substrate storage unavailable", nil)
	}
	tx, err := h.db.BeginTx(e.Request.Context(), nil)
	if err != nil {
		return nil, "", "", err
	}
	if _, err = tx.ExecContext(e.Request.Context(), `SELECT set_config('app.tenant_id',$1,true)`, tenant); err != nil {
		tx.Rollback()
		return nil, "", "", err
	}
	return tx, tenant, owner, nil
}
func (h substrateRoutes) profiles(e *httpx.Event) error {
	if _, err := requireAuth(e); err != nil {
		return err
	}
	return httpx.Success(e, http.StatusOK, map[string]any{"profiles": substrate.Profiles()})
}
func (h substrateRoutes) list(e *httpx.Event) error {
	tx, tenant, owner, err := h.scope(e)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(e.Request.Context(), `SELECT s.id,s.name,s.worker_id,COALESCE(b.node,s.metadata_json #>> '{substrate_inventory,node}',''),COALESCE(b.revision,0),
 COALESCE(b.enabled,false), s.connection_state='connected' AND s.lifecycle_state='active' AND s.last_heartbeat_at>clock_timestamp()-interval '2 minutes' AND (s.metadata_json->>'inventory_observed_at')::timestamptz>clock_timestamp()-interval '2 minutes'
 FROM servers s LEFT JOIN substrate_bindings b ON b.tenant_id=s.tenant_id AND b.server_id=s.id
 WHERE s.tenant_id=$1 AND s.owner_subject_id=$2 AND s.metadata_json->>'server_node_role'='substrate' ORDER BY s.name,s.id`, tenant, owner)
	if err != nil {
		return err
	}
	defer rows.Close()
	entries := []map[string]any{}
	for rows.Next() {
		var id, name, worker, node string
		var revision int64
		var connected sql.NullBool
		var enabled bool
		if err = rows.Scan(&id, &name, &worker, &node, &revision, &enabled, &connected); err != nil {
			return err
		}
		entries = append(entries, map[string]any{"server_id": id, "name": name, "worker_id": worker, "node": node, "revision": revision, "available": enabled && connected.Bool, "connected": connected.Bool, "enabled": enabled})
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return httpx.Success(e, http.StatusOK, map[string]any{"substrates": entries})
}
func (h substrateRoutes) observation(e *httpx.Event, tx *sql.Tx, tenant, owner string) (string, substrate.GuardObservation, error) {
	var worker string
	var raw []byte
	var observed substrate.GuardObservation
	err := tx.QueryRowContext(e.Request.Context(), `SELECT worker_id,metadata_json->'substrate_inventory' FROM servers
 WHERE tenant_id=$1 AND id=$2 AND owner_subject_id=$3 AND metadata_json->>'server_node_role'='substrate'
 AND connection_state='connected' AND lifecycle_state='active' AND last_heartbeat_at>clock_timestamp()-interval '2 minutes'
 AND (metadata_json->>'inventory_observed_at')::timestamptz>clock_timestamp()-interval '2 minutes' FOR SHARE`, tenant, e.Request.PathValue("id"), owner).Scan(&worker, &raw)
	if err != nil {
		return "", observed, httpx.NewAPIError(http.StatusConflict, "substrate_inventory_unavailable", "A current authenticated substrate inventory is required", nil)
	}
	if json.Unmarshal(raw, &observed) != nil || observed.Validate() != nil {
		return "", observed, httpx.NewAPIError(http.StatusConflict, "substrate_configuration_unavailable", "Configure the node-local substrate client first", nil)
	}
	return worker, observed, nil
}
func (h substrateRoutes) inventory(e *httpx.Event) error {
	tx, tenant, owner, err := h.scope(e)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, observed, err := h.observation(e, tx, tenant, owner)
	if err != nil {
		return err
	}
	return httpx.Success(e, http.StatusOK, observed.Inventory)
}
func (h substrateRoutes) binding(e *httpx.Event) error {
	tx, tenant, owner, err := h.scope(e)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var req struct {
		Enabled  bool  `json:"enabled"`
		Revision int64 `json:"revision"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(e.Response, e.Request.Body, 1024))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&req) != nil || req.Revision < 0 {
		return httpx.BadRequest(e, "Expected enabled and current binding revision", nil)
	}
	worker, observed, err := h.observation(e, tx, tenant, owner)
	if err != nil {
		return err
	}
	// Serialize revision changes with native admission before inspecting guests.
	// A just-committed admission then appears in the next statement's snapshot.
	var currentRevision int64
	err = tx.QueryRowContext(e.Request.Context(), `SELECT revision FROM substrate_bindings WHERE tenant_id=$1 AND server_id=$2 FOR UPDATE`, tenant, e.Request.PathValue("id")).Scan(&currentRevision)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if currentRevision != req.Revision {
		return httpx.Error(e, http.StatusConflict, "substrate_revision_conflict", "Reload the binding before updating it", nil)
	}
	var active bool
	if err = tx.QueryRowContext(e.Request.Context(), `SELECT EXISTS(SELECT 1 FROM substrate_guest_leases g JOIN techstack_vm_leases l ON l.tenant_id=g.tenant_id AND l.id=g.lease_id JOIN servers s ON s.tenant_id=l.tenant_id AND s.id=l.server_id WHERE g.tenant_id=$1 AND g.substrate_server_id=$2 AND s.lifecycle_state!='decommissioned')`, tenant, e.Request.PathValue("id")).Scan(&active); err != nil {
		return err
	}
	if active {
		return httpx.Error(e, http.StatusConflict, "substrate_binding_in_use", "Existing guest custody must finish before changing the binding", nil)
	}
	config, err := json.Marshal(observed)
	if err != nil {
		return err
	}
	var revision int64
	err = tx.QueryRowContext(e.Request.Context(), `INSERT INTO substrate_bindings(tenant_id,server_id,worker_id,node,revision,config_json,enabled)
 SELECT $1,$2,$3,$4,1,$5::jsonb,$6 WHERE $7::bigint=0
 ON CONFLICT(tenant_id,server_id) DO UPDATE SET worker_id=EXCLUDED.worker_id,node=EXCLUDED.node,revision=substrate_bindings.revision+1,config_json=EXCLUDED.config_json,enabled=EXCLUDED.enabled
 WHERE substrate_bindings.revision=$7 RETURNING revision`, tenant, e.Request.PathValue("id"), worker, observed.Node, config, req.Enabled, req.Revision).Scan(&revision)
	if err == sql.ErrNoRows && req.Revision > 0 {
		err = tx.QueryRowContext(e.Request.Context(), `UPDATE substrate_bindings SET revision=revision+1,worker_id=$3,node=$4,config_json=$5::jsonb,enabled=$6 WHERE tenant_id=$1 AND server_id=$2 AND revision=$7 RETURNING revision`, tenant, e.Request.PathValue("id"), worker, observed.Node, config, req.Enabled, req.Revision).Scan(&revision)
	}
	if err == sql.ErrNoRows {
		return httpx.Error(e, http.StatusConflict, "substrate_revision_conflict", "Reload the binding before updating it", nil)
	}
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return httpx.Success(e, http.StatusOK, map[string]any{"server_id": e.Request.PathValue("id"), "revision": revision, "enabled": req.Enabled})
}
