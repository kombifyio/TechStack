package routes

import (
	"encoding/json"
	"errors"
	"github.com/kombifyio/techstack/internal/homeassistant"
	"github.com/kombifyio/techstack/pkg/httpx"
	"io"
	"net/http"
)

type HomeAssistantOperationRoutes struct {
	Enrollments     *homeassistant.EnrollmentStore
	Bindings        []homeassistant.OwnerBinding
	Recovery        map[string]*homeassistant.MigrationBinding
	RecoveryTenants map[string]string
}

func RegisterHomeAssistantOperationRoutes(r *httpx.Router, c HomeAssistantOperationRoutes) {
	for _, action := range []string{"backup", "restore", "update"} {
		r.POST("/api/v1/home-assistant/instances/{binding}/"+action, c.execute(action))
	}
}

func (c HomeAssistantOperationRoutes) execute(action string) httpx.HandlerFunc {
	return func(e *httpx.Event) error {
		owner, err := requireAuth(e)
		if err != nil {
			return err
		}
		tenant, err := requireRegistryRouteTenant(e, owner, "techstack.home_assistant."+action)
		if err != nil {
			return err
		}
		var selected *homeassistant.OwnerBinding
		for i := range c.Bindings {
			b := &c.Bindings[i]
			if b.OwnerID == owner && b.TenantID == tenant && b.Instance != nil && b.Instance.BindingRef == e.Request.PathValue("binding") {
				if selected != nil {
					return httpx.NewAPIError(409, "ambiguous_binding", "Instance binding is ambiguous", nil)
				}
				selected = b
			}
		}
		if selected == nil && c.Enrollments != nil {
			selected, err = c.Enrollments.Owned(e.Request.Context(), tenant, owner, e.Request.PathValue("binding"))
			if err != nil {
				return httpx.NewAPIError(404, "not_found", "Instance binding unavailable", nil)
			}
			defer selected.Instance.Core.Close()
			if selected.Instance.Supervisor != nil {
				defer selected.Instance.Supervisor.Close()
			}
		}
		if selected == nil {
			return httpx.NewAPIError(404, "not_found", "Instance binding unavailable", nil)
		}
		var req struct {
			OperationID       string `json:"operation_id"`
			BackupOperationID string `json:"backup_operation_id"`
			Version           string `json:"version"`
			Target            string `json:"target,omitempty"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(e.Response, e.Request.Body, 8192))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			return httpx.BadRequest(e, "Invalid lifecycle request", nil)
		}
		var trailing any
		if !errors.Is(decoder.Decode(&trailing), io.EOF) || req.OperationID == "" || len(req.OperationID) > 128 || len(req.BackupOperationID) > 128 {
			return httpx.BadRequest(e, "Bounded stable operation identity required", nil)
		}
		var result map[string]any
		switch action {
		case "backup":
			result, err = selected.Instance.Backup(e.Request.Context(), req.OperationID)
		case "update":
			result, err = selected.Instance.UpdateTarget(e.Request.Context(), req.OperationID, req.Target, req.Version, req.BackupOperationID)
		case "restore":
			recovery := c.Recovery[selected.RecoveryBindingRef]
			if recovery == nil || recovery.OwnerID != owner || c.RecoveryTenants[selected.RecoveryBindingRef] != tenant {
				return httpx.NewAPIError(403, "recovery_not_authorized", "An owned isolated recovery binding is required", nil)
			}
			result, err = selected.Instance.Restore(e.Request.Context(), req.OperationID, req.BackupOperationID, recovery)
		}
		if errors.Is(err, homeassistant.ErrUnauthorized) {
			return httpx.Forbidden(e, "Lifecycle operation is not granted for this instance")
		}
		if err != nil {
			return httpx.NewAPIError(409, "operation_not_advanced", "Native operation did not advance; retain the operation ID for reconciliation", nil)
		}
		status := http.StatusOK
		if result["pending"] == true {
			status = http.StatusAccepted
		}
		return httpx.Success(e, status, result)
	}
}
