package routes

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/ril/hostpin"
	"io"
	"net/http"
	"strings"
	"time"
)

// RegisterRILHostIdentityRoute reads no credential from its caller. Proof of
// the root-owned SSH host key must match the existing lease-bound pin.
func RegisterRILHostIdentityRoute(r *httpx.Router, database *sql.DB) {
	r.POST("/api/v1/internal/ril-host-identity", func(e *httpx.Event) error {
		if database == nil {
			http.Error(e.Response, "identity unavailable", 503)
			return nil
		}
		body, err := io.ReadAll(io.LimitReader(e.Request.Body, 8193))
		if err != nil || len(body) == 0 || len(body) > 8192 {
			http.Error(e.Response, "invalid request", 400)
			return nil
		}
		var input hostpin.Request
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
			http.Error(e.Response, "invalid request", 400)
			return nil
		}
		now := time.Now().UTC()
		fingerprint, err := hostpin.Verify(input, now)
		if err != nil {
			http.Error(e.Response, "identity denied", 403)
			return nil
		}
		tx, err := db.BeginTenantTransaction(e.Request.Context(), database, input.TenantID)
		if err != nil {
			http.Error(e.Response, "identity unavailable", 503)
			return nil
		}
		defer tx.Rollback()
		rows, err := tx.QueryContext(e.Request.Context(), `SELECT DISTINCT s.tenant_id,s.owner_subject_id,s.id,s.stack_id FROM servers s JOIN techstack_vm_leases l ON l.tenant_id=s.tenant_id AND l.id=s.lease_id AND l.server_id=s.id AND l.owner_subject_id=s.owner_subject_id JOIN runtime_host_key_pins p ON p.lease_binding=runtime_host_key_pin_lease_binding(s.tenant_id,l.id) WHERE s.tenant_id=$1 AND s.id=$2 AND p.fingerprint=$3 AND s.lifecycle_state='active' AND s.desired_state='running' AND l.desired_state='running' AND l.cancelled_at IS NULL AND l.valid_from<=now() AND l.valid_until>now() LIMIT 2`, input.TenantID, input.ServerID, fingerprint)
		if err != nil {
			http.Error(e.Response, "identity unavailable", 503)
			return nil
		}
		defer rows.Close()
		var result hostpin.Response
		if !rows.Next() || rows.Scan(&result.TenantID, &result.OwnerID, &result.ServerID, &result.StackID) != nil || rows.Next() || rows.Err() != nil || result.TenantID != input.TenantID || result.ServerID != input.ServerID || strings.TrimSpace(result.OwnerID) == "" || strings.TrimSpace(result.StackID) == "" {
			http.Error(e.Response, "identity denied", 403)
			return nil
		}
		result.Nonce = input.Nonce
		result.ExpiresAt = input.IssuedAt + 30
		if result.ExpiresAt <= now.Unix() {
			http.Error(e.Response, "identity denied", http.StatusForbidden)
			return nil
		}
		e.Response.Header().Set("Cache-Control", "no-store")
		e.Response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(e.Response).Encode(result)
		return nil
	})
}
