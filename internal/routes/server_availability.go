package routes

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/availability"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// serverAvailabilityResponse is what the server's own page shows about how
// available the machine is: the class the Layout Planner will use, where it
// came from, and the observed history next to the owner's word.
type serverAvailabilityResponse struct {
	ServerID string `json:"server_id"`
	// Class is the class the planner reads: the owner's confirmation when
	// there is one, otherwise the observed class. "always-on", "part-time" or
	// "unknown".
	Class string `json:"class"`
	// Source is "user-confirmed", "server-transitions" or "none".
	Source string `json:"source"`
	// ObservedClass is what the connection history alone supports.
	ObservedClass string `json:"observed_class"`
	// UptimeRatio is nil when the window holds no observed time.
	UptimeRatio     *float64 `json:"uptime_ratio"`
	ObservedSeconds int64    `json:"observed_seconds"`
	WindowDays      int      `json:"window_days"`
	Confirmed       bool     `json:"confirmed"`
	ConfirmedAt     *string  `json:"confirmed_at,omitempty"`
}

type serverAvailabilityRequest struct {
	// Class is "always-on" or "part-time" to confirm, and "unknown" or null to
	// withdraw the confirmation and go back to what the history supports. It
	// must be present, so an empty body never clears anything.
	Class json.RawMessage `json:"class"`
}

const serverAvailabilityRequestMaxSize = 1024

// availabilityView reads the server's connection history and folds it with the
// owner's confirmation. The history is read for the whole tenant window, the
// same read the availability report uses, and only this server's rows are kept.
func (h serverRuntimeHandlers) availabilityView(e *httpx.Event, server controlplane.ServerRuntime) (*serverAvailabilityResponse, error) {
	now := h.now().UTC()
	window := availability.NodeEvidenceWindow(now)
	rows, err := h.availabilityReader.ListServerTransitionsSince(e.Request.Context(), server.TenantID, "connection", window.Start, 0)
	if err != nil {
		httpx.RecordDependencyFailure(e, "servers.availability.transitions", err)
		return nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Availability history is unavailable", nil)
	}
	own := make([]controlplane.ServerStateTransition, 0, len(rows))
	for _, row := range rows {
		if row.ServerID == server.ID {
			own = append(own, row)
		}
	}
	report := nodeAvailabilityReport(server, own, now)
	effective := workerAvailabilityFrom(server, report)
	view := &serverAvailabilityResponse{
		ServerID: server.ID, Class: effective.Class, Source: effective.Source,
		ObservedClass: availability.ClassifyNode(report), UptimeRatio: report.UptimeRatio,
		ObservedSeconds: report.ObservedSeconds, WindowDays: availability.NodeEvidenceWindowDays,
		Confirmed: effective.Confirmed,
	}
	if confirmation, confirmed := controlplane.ServerAvailabilityConfirmationOf(server); confirmed && !confirmation.ConfirmedAt.IsZero() {
		at := confirmation.ConfirmedAt.Format(time.RFC3339)
		view.ConfirmedAt = &at
	}
	return view, nil
}

// availabilityRead serves GET /api/v1/servers/{serverId}/availability.
func (h serverRuntimeHandlers) availabilityRead(e *httpx.Event) error {
	server, err := h.ownedServer(e, "techstack.servers.availability.read")
	if err != nil || server == nil {
		return err
	}
	view, err := h.availabilityView(e, *server)
	if err != nil {
		return err
	}
	return httpx.Success(e, http.StatusOK, view)
}

// availabilityConfirm serves PUT /api/v1/servers/{serverId}/availability. Only
// the server's owner can state how available the machine is; the confirmation
// is stored beside the Guard's observations, never over them.
func (h serverRuntimeHandlers) availabilityConfirm(e *httpx.Event) error {
	server, err := h.ownedServer(e, "techstack.servers.availability.confirm")
	if err != nil || server == nil {
		return err
	}
	if serverRuntimeHiddenFromCurrentInventory(*server) {
		return httpx.NotFound(e, "Server not found")
	}
	ownerID, _, _ := authenticatedUser(e)
	raw, readErr := io.ReadAll(io.LimitReader(e.Request.Body, serverAvailabilityRequestMaxSize+1))
	if readErr != nil || len(raw) > serverAvailabilityRequestMaxSize {
		return httpx.BadRequest(e, "Invalid request body", nil)
	}
	var request serverAvailabilityRequest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF || len(request.Class) == 0 {
		return httpx.BadRequest(e, "Send the class as \"always-on\", \"part-time\" or \"unknown\" (null withdraws the confirmation)", nil)
	}
	class, valid := confirmedAvailabilityClass(request.Class)
	if !valid {
		return httpx.BadRequest(e, "The class must be \"always-on\", \"part-time\" or \"unknown\"", nil)
	}
	updated, setErr := h.availabilityConfirmer.SetServerAvailabilityConfirmation(e.Request.Context(), server.TenantID, ownerID, server.ID, class, h.now().UTC())
	switch {
	case errors.Is(setErr, controlplane.ErrNotFound):
		return httpx.NotFound(e, "Server not found")
	case setErr != nil:
		httpx.RecordDependencyFailure(e, "servers.availability.confirm", setErr)
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "The availability could not be saved", nil)
	}
	slog.Info("server_availability_confirmed", "tenant_id", server.TenantID, "server_id", server.ID, "class", class, "actor", ownerID)
	view, err := h.availabilityView(e, *updated)
	if err != nil {
		return err
	}
	return httpx.Success(e, http.StatusOK, view)
}

// confirmedAvailabilityClass maps the request member onto the stored class:
// "always-on" and "part-time" confirm, and "unknown" or null withdraw (the
// empty class).
func confirmedAvailabilityClass(raw json.RawMessage) (class string, valid bool) {
	if strings.TrimSpace(string(raw)) == "null" {
		return "", true
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	switch strings.TrimSpace(value) {
	case availability.NodeAlwaysOn:
		return availability.NodeAlwaysOn, true
	case availability.NodePartTime:
		return availability.NodePartTime, true
	case availability.NodeUnknown:
		return "", true
	default:
		return "", false
	}
}
