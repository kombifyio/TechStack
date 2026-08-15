// Package routes provides custom HTTP routes for kombifyTechstack API.
// This file contains the RIL self-heal audit and recipe management endpoints.
package routes

import (
	"net/http"
	"time"

	"github.com/kombifyio/techstack/pkg/agent/watchdog"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/pocketbase/pocketbase/core"
)

// RILHealEventResponse represents a self-heal event in the API response.
type RILHealEventResponse struct {
	ID            string     `json:"id"`
	EventID       string     `json:"event_id"`
	ServerID      string     `json:"server_id"`
	RecipeName    string     `json:"recipe_name"`
	TriggerReason string     `json:"trigger_reason,omitempty"`
	AutoExecuted  bool       `json:"auto_executed"`
	Success       bool       `json:"success"`
	ActionTaken   string     `json:"action_taken,omitempty"`
	RollbackInfo  string     `json:"rollback_info,omitempty"`
	ErrorMessage  string     `json:"error_message,omitempty"`
	Severity      string     `json:"severity"`
	OccurredAt    *time.Time `json:"occurred_at,omitempty"`
	Created       time.Time  `json:"created"`
}

// RILRecipeResponse represents a self-heal recipe configuration.
type RILRecipeResponse struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	AutoExec    bool   `json:"auto_exec"`
	Enabled     bool   `json:"enabled"`
}

// RegisterRILHealRoutes adds RIL self-heal audit and recipe endpoints.
func RegisterRILHealRoutes(r *httpx.Router, app core.App, recipes []watchdog.Recipe) {
	h := rilHealHandler{app: app, recipes: recipes}
	r.GET("/api/v1/ril/heal/audit", h.listAudit)
	r.GET("/api/v1/ril/heal/recipes", h.listRecipes)
	r.PATCH("/api/v1/ril/heal/recipes/{recipeName}", h.toggleRecipe)
}

type rilHealHandler struct {
	app     core.App
	recipes []watchdog.Recipe
}

func (h rilHealHandler) listAudit(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}

	serverFilter := e.Request.URL.Query().Get("server_id")

	filter := "owner_id = {:owner}"
	params := map[string]any{"owner": ownerID}

	if serverFilter != "" {
		filter += " && server_id = {:sid}"
		params["sid"] = serverFilter
	}

	records, err := h.app.FindRecordsByFilter(
		"ril_heal_events",
		filter,
		"-occurred_at",
		0, 0,
		params,
	)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, "ril_audit_failed", "Failed to list heal events", nil)
	}

	events := make([]RILHealEventResponse, 0, len(records))
	for _, r := range records {
		events = append(events, healEventFromRecord(r))
	}

	pagination := ksapi.ParsePagination(e.Request)
	total := len(events)
	paged := ksapi.Paginate(events, pagination)
	return httpx.SuccessWithMeta(e, http.StatusOK, paged, ksapi.NewPaginatedMeta(total, pagination.Page, pagination.PerPage))
}

func (h rilHealHandler) listRecipes(e *httpx.Event) error {
	if _, err := requireAuth(e); err != nil {
		return err
	}

	descriptions := map[string]string{
		"service-restart":  "Auto-restart failed systemd services after 3 consecutive checks",
		"tunnel-reconnect": "Reconnect lost Core-Agent tunnel after 90s heartbeat loss",
		"cert-rotate":      "Auto-rotate mTLS certificates expiring within 7 days",
		"disk-cleanup":     "Clean safe targets when disk usage exceeds 90%",
		"oom-recovery":     "Restart OOM-killed processes with increased memory limit",
	}

	resp := make([]RILRecipeResponse, 0, len(h.recipes))
	for _, r := range h.recipes {
		desc := descriptions[r.Name()]
		if desc == "" {
			desc = r.Name()
		}
		resp = append(resp, RILRecipeResponse{
			Name:        r.Name(),
			Description: desc,
			AutoExec:    r.AutoExec(),
			Enabled:     r.Enabled(),
		})
	}

	return httpx.Success(e, http.StatusOK, resp)
}

func (h rilHealHandler) toggleRecipe(e *httpx.Event) error {
	if _, err := requireAuth(e); err != nil {
		return err
	}

	name := e.Request.PathValue("recipeName")
	r, ok := watchdog.RecipeByName(h.recipes, name)
	if !ok {
		return httpx.NotFound(e, "Recipe not found")
	}

	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := e.BindBody(&body); err != nil || body.Enabled == nil {
		return httpx.BadRequest(e, "Request body must contain 'enabled' boolean", nil)
	}

	r.SetEnabled(*body.Enabled)

	desc := map[string]string{
		"service-restart":  "Auto-restart failed systemd services after 3 consecutive checks",
		"tunnel-reconnect": "Reconnect lost Core-Agent tunnel after 90s heartbeat loss",
		"cert-rotate":      "Auto-rotate mTLS certificates expiring within 7 days",
		"disk-cleanup":     "Clean safe targets when disk usage exceeds 90%",
		"oom-recovery":     "Restart OOM-killed processes with increased memory limit",
	}
	d := desc[r.Name()]
	if d == "" {
		d = r.Name()
	}

	return httpx.Success(e, http.StatusOK, RILRecipeResponse{
		Name:        r.Name(),
		Description: d,
		AutoExec:    r.AutoExec(),
		Enabled:     r.Enabled(),
	})
}

func healEventFromRecord(r *core.Record) RILHealEventResponse {
	ev := RILHealEventResponse{
		ID:            r.Id,
		EventID:       r.GetString("event_id"),
		ServerID:      r.GetString("server_id"),
		RecipeName:    r.GetString("recipe_name"),
		TriggerReason: r.GetString("trigger_reason"),
		AutoExecuted:  r.GetBool("auto_executed"),
		Success:       r.GetBool("success"),
		ActionTaken:   r.GetString("action_taken"),
		RollbackInfo:  r.GetString("rollback_info"),
		ErrorMessage:  r.GetString("error_message"),
		Severity:      r.GetString("severity"),
		Created:       r.GetDateTime("created").Time(),
	}
	if t := r.GetDateTime("occurred_at"); !t.IsZero() {
		ts := t.Time()
		ev.OccurredAt = &ts
	}
	return ev
}
