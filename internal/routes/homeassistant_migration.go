package routes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/kombifyio/techstack/internal/homeassistant"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
	"github.com/kombifyio/techstack/pkg/ril/workflows"
	"net/http"
)

type HomeAssistantMigrationRoutes struct {
	Engine   *workflow.Engine
	Bindings map[string]*homeassistant.MigrationBinding
	Tenants  map[string]string
}

func RegisterHomeAssistantMigrationRoutes(r *httpx.Router, c HomeAssistantMigrationRoutes) {
	r.POST("/api/v1/home-assistant/migrations", c.start)
	r.GET("/api/v1/home-assistant/migrations/{id}", c.get)
	r.POST("/api/v1/home-assistant/migrations/{id}/confirm", c.confirm)
	r.POST("/api/v1/home-assistant/migrations/{id}/rollback", c.rollback)
}
func (c HomeAssistantMigrationRoutes) scope(e *httpx.Event) (string, string, error) {
	owner, err := requireAuth(e)
	if err != nil {
		return "", "", err
	}
	tenant, err := requireRegistryRouteTenant(e, owner, "techstack.home_assistant.migrate")
	return owner, tenant, err
}
func (c HomeAssistantMigrationRoutes) start(e *httpx.Event) error {
	owner, tenant, err := c.scope(e)
	if err != nil {
		return err
	}
	var req struct {
		Source        string `json:"source_service_id"`
		Target        string `json:"target_binding_ref"`
		Authorization string `json:"authorization_ref"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(e.Response, e.Request.Body, 8192)).Decode(&req); err != nil {
		return httpx.BadRequest(e, "Invalid migration request", nil)
	}
	b := c.Bindings[req.Target]
	if c.Engine == nil || b == nil {
		return httpx.Error(e, 503, "migration_executor_unavailable", "An authorized native migration binding is required", nil)
	}
	if b.OwnerID != owner || c.Tenants[req.Target] != tenant || b.SourceServiceID != req.Source || b.AuthorizationRef != req.Authorization {
		return httpx.Forbidden(e, "Migration binding does not authorize this owner, source or operation")
	}
	hash := sha256.Sum256([]byte(tenant + "\x00" + owner + "\x00" + req.Target))
	id := "ha-migration-" + hex.EncodeToString(hash[:])
	run := &workflow.Run{RunID: id, Type: workflows.TypeHomeAssistantMigration, OwnerID: owner, Input: map[string]any{"tenant_id": tenant, "source_service_id": req.Source, "target_binding_ref": req.Target, "authorization_ref": req.Authorization}}
	if _, err := c.Engine.StartOrResumeRun(e.Request.Context(), run); err != nil {
		return httpx.Error(e, 409, "migration_not_advanced", "Migration did not advance; inspect its retained run", map[string]any{"run_id": id})
	}
	return httpx.Success(e, http.StatusAccepted, map[string]any{"run_id": id})
}
func (c HomeAssistantMigrationRoutes) owned(e *httpx.Event) (*workflow.Run, error) {
	owner, tenant, err := c.scope(e)
	if err != nil {
		return nil, err
	}
	if c.Engine == nil {
		return nil, httpx.NewAPIError(404, "not_found", "Migration unavailable", nil)
	}
	run, err := c.Engine.GetRun(e.Request.PathValue("id"))
	if err != nil {
		return nil, httpx.NewAPIError(404, "not_found", "Migration not found", nil)
	}
	if run.OwnerID != owner || run.Input["tenant_id"] != tenant || (run.Type != workflows.TypeHomeAssistantMigration && run.Type != workflows.TypeHomeAssistantRollback) {
		return nil, httpx.NewAPIError(404, "not_found", "Migration not found", nil)
	}
	return run, nil
}
func (c HomeAssistantMigrationRoutes) get(e *httpx.Event) error {
	run, err := c.owned(e)
	if err != nil {
		return err
	}
	steps, err := c.Engine.ListSteps(run.RunID)
	if err != nil {
		return httpx.NewInternalServerError("Migration steps unavailable", nil)
	}
	return httpx.Success(e, 200, map[string]any{"run": run, "steps": steps})
}
func (c HomeAssistantMigrationRoutes) confirm(e *httpx.Event) error {
	run, err := c.owned(e)
	if err != nil {
		return err
	}
	var body struct {
		Confirmed bool `json:"confirmed"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(e.Response, e.Request.Body, 1024)).Decode(&body); err != nil || !body.Confirmed {
		return httpx.BadRequest(e, "Explicit confirmation required", nil)
	}
	key := workflows.HomeAssistantConfirmationKey(run.RunID)
	if run.Status != workflow.RunSuspended || run.AwaitingSignal != key {
		return httpx.BadRequest(e, "Migration is not awaiting cutover confirmation", nil)
	}
	if err := c.Engine.Deliver(e.Request.Context(), workflow.Signal{Key: key, Payload: map[string]any{"confirmed": true}}); err != nil {
		return httpx.Error(e, 409, "cutover_not_advanced", "Cutover did not advance; inspect the retained run", nil)
	}
	return httpx.Success(e, 202, map[string]any{"run_id": run.RunID})
}
func (c HomeAssistantMigrationRoutes) rollback(e *httpx.Event) error {
	original, err := c.owned(e)
	if err != nil {
		return err
	}
	if original.Type != workflows.TypeHomeAssistantMigration || (original.Status != workflow.RunCompleted && original.Status != workflow.RunFailed) || original.Context[workflows.ActHASourceArchive] == nil {
		return httpx.BadRequest(e, "An owned completed or failed migration with a retained source archive is required for return", nil)
	}
	input := map[string]any{}
	for k, v := range original.Input {
		input[k] = v
	}
	input["migration_run_id"] = original.RunID
	run := &workflow.Run{RunID: original.RunID + ":rollback", Type: workflows.TypeHomeAssistantRollback, OwnerID: original.OwnerID, Input: input, Context: original.Context}
	if _, err := c.Engine.StartOrResumeRun(e.Request.Context(), run); err != nil {
		return httpx.Error(e, 409, "rollback_not_advanced", "Return did not advance; inspect the retained run", nil)
	}
	return httpx.Success(e, 202, map[string]any{"run_id": run.RunID})
}
