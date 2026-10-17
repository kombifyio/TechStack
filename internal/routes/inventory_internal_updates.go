package routes

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/guardposture"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// ---------------------------------------------------------------------------
// GET /api/v1/internal/runtime/updates
// ---------------------------------------------------------------------------
//
// The private Cloud monitoring read of the owner's pending updates, next to
// /runtime/backups and behind the same servicecall boundary and owner-scoped
// collection policy. It is a projection only: the operating-system facts come
// from the security evidence the node's Guard already reports (the same
// guardposture derivation the server read model uses), and the service facts
// from the same version comparison the services view uses. Techstack adds no
// custody and runs nothing on the node to answer it.
//
// What is absent stays absent: an OS fact the evidence does not state is
// omitted, and a service whose version was not compared is counted as
// not_compared, never as up to date.

const (
	runtimeUpdatesOSReported = "reported"
	runtimeUpdatesOSUnknown  = "unknown"
)

var runtimeUpdatesSurface = cloudReadSurface{
	errorPrefix: "techstack.runtime_updates.", unavailableReason: "runtime_updates_unavailable",
	title: "Runtime updates unavailable", body: "Techstack could not resolve these update facts safely.",
}

type runtimeUpdatesOS struct {
	// State is "reported" when the Guard's current evidence states at least
	// one OS update fact, otherwise "unknown" with a reason code.
	State                  string     `json:"state"`
	ReasonCode             string     `json:"reason_code,omitempty"`
	PendingSecurityUpdates *int       `json:"pending_security_updates,omitempty"`
	RebootRequired         *bool      `json:"reboot_required,omitempty"`
	LastUnattendedRun      *time.Time `json:"last_unattended_run,omitempty"`
}

type runtimeServiceUpdate struct {
	ServiceID string `json:"service_id"`
	Name      string `json:"name"`
	Current   string `json:"current,omitempty"`
	Latest    string `json:"latest"`
}

type runtimeServiceUpdates struct {
	// Compared counts services whose version was compared with a published
	// release; NotCompared counts the rest (no measured version, unsupported
	// registry, check pending or disabled).
	Compared    int                    `json:"compared"`
	NotCompared int                    `json:"not_compared"`
	Available   []runtimeServiceUpdate `json:"available"`
}

type runtimeUpdatesServer struct {
	ServerID string                `json:"server_id"`
	OS       runtimeUpdatesOS      `json:"os"`
	Services runtimeServiceUpdates `json:"services"`
}

type runtimeUpdates struct {
	runtimeMonitoringEnvelope
	Servers []runtimeUpdatesServer `json:"servers"`
}

func (h inventoryHandlers) httpInternalRuntimeUpdates(e *httpx.Event) error {
	return h.serveCloudRead(e, runtimeUpdatesSurface, h.serveRuntimeUpdates)
}

func (h inventoryHandlers) serveRuntimeUpdates(e *httpx.Event, ctx context.Context, scope inventoryScope) {
	app := h.ownerReadApplication()
	serverScope, err := app.authorize(ctx, scope, InventoryActionRead, controlplane.InventoryReadTargetServerCollection, "")
	if err != nil {
		h.writeCloudReadError(e, runtimeUpdatesSurface, err)
		return
	}
	serviceScope, err := app.authorize(ctx, scope, InventoryActionRead, controlplane.InventoryReadTargetServiceCollection, "")
	if err != nil {
		h.writeCloudReadError(e, runtimeUpdatesSurface, err)
		return
	}
	servers, err := allInventoryServerRows(ctx, app.read, serverScope)
	if err != nil {
		h.writeCloudReadError(e, runtimeUpdatesSurface, inventoryStoreError(err))
		return
	}
	services, err := allInventoryServiceRows(ctx, app.read, serviceScope)
	if err != nil {
		h.writeCloudReadError(e, runtimeUpdatesSurface, inventoryStoreError(err))
		return
	}

	var views map[string]guardposture.View
	postureReadable := false
	if h.posture != nil {
		if read, readErr := h.posture.Views(ctx, scope.tenantID); readErr == nil {
			views, postureReadable = read, true
		}
	}
	versions := &serviceRuntimeHandlers{updates: h.updates}

	now := app.now().UTC()
	response := runtimeUpdates{runtimeMonitoringEnvelope: newRuntimeMonitoringEnvelope(now), Servers: make([]runtimeUpdatesServer, 0, len(servers))}
	index := make(map[string]int, len(servers))
	for _, server := range servers {
		index[server.ID] = len(response.Servers)
		response.Servers = append(response.Servers, runtimeUpdatesServer{
			ServerID: safeLabel(server.ID, 256),
			OS:       h.runtimeUpdatesOSFrom(server.WorkerID, views, postureReadable),
			Services: runtimeServiceUpdates{Available: []runtimeServiceUpdate{}},
		})
	}
	for _, service := range services {
		position, ok := index[service.ServerID]
		if !ok {
			continue
		}
		summary := &response.Servers[position].Services
		version := versions.serviceVersionFor(service)
		if version == nil || version.Latest == "" {
			summary.NotCompared++
			continue
		}
		summary.Compared++
		if version.UpdateAvailable {
			summary.Available = append(summary.Available, runtimeServiceUpdate{
				ServiceID: safeLabel(service.ID, 256), Name: safeLabel(service.Name, 256),
				Current: safeLabel(version.Current, 128), Latest: safeLabel(version.Latest, 128),
			})
		}
	}
	_ = e.JSON(http.StatusOK, response)
}

// runtimeUpdatesOSFrom projects the OS update facts of the server's Guard.
func (h inventoryHandlers) runtimeUpdatesOSFrom(workerID string, views map[string]guardposture.View, postureReadable bool) runtimeUpdatesOS {
	switch {
	case !postureReadable:
		return runtimeUpdatesOS{State: runtimeUpdatesOSUnknown, ReasonCode: "posture_unavailable"}
	case strings.TrimSpace(workerID) == "":
		return runtimeUpdatesOS{State: runtimeUpdatesOSUnknown, ReasonCode: "no_guard"}
	}
	view, ok := views[strings.TrimSpace(workerID)]
	if !ok {
		view = h.posture.View(nil)
	}
	facts := view.Updates.OS
	if facts.PendingSecurityUpdates == nil && facts.RebootRequired == nil && facts.LastUnattendedRun == nil {
		return runtimeUpdatesOS{State: runtimeUpdatesOSUnknown, ReasonCode: "update_evidence_unavailable"}
	}
	return runtimeUpdatesOS{
		State: runtimeUpdatesOSReported, PendingSecurityUpdates: facts.PendingSecurityUpdates,
		RebootRequired: facts.RebootRequired, LastUnattendedRun: facts.LastUnattendedRun,
	}
}

// allInventoryServerRows pages through the scoped server collection.
func allInventoryServerRows(ctx context.Context, read controlplane.InventoryReadStore, scope controlplane.InventoryReadScope) ([]controlplane.ServerRuntime, error) {
	request := controlplane.InventoryPageRequest{Limit: controlplane.MaxInventoryPageSize}
	result := make([]controlplane.ServerRuntime, 0)
	for {
		page, err := read.ListInventoryServers(ctx, scope, request)
		if err != nil {
			return nil, err
		}
		result = append(result, page.Servers...)
		if page.Next == nil {
			return result, nil
		}
		request.Watermark, request.After = page.Watermark, *page.Next
	}
}

// allInventoryServiceRows pages through the scoped service collection.
func allInventoryServiceRows(ctx context.Context, read controlplane.InventoryReadStore, scope controlplane.InventoryReadScope) ([]controlplane.ServiceRuntime, error) {
	request := controlplane.InventoryPageRequest{Limit: controlplane.MaxInventoryPageSize}
	result := make([]controlplane.ServiceRuntime, 0)
	for {
		page, err := read.ListInventoryServices(ctx, scope, "", request)
		if err != nil {
			return nil, err
		}
		result = append(result, page.Services...)
		if page.Next == nil {
			return result, nil
		}
		request.Watermark, request.After = page.Watermark, *page.Next
	}
}
