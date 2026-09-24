package routes

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/availability"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

// serverTransitionWindowReader is the narrow read the availability projection
// needs: one dimension, one window, oldest first.
type serverTransitionWindowReader interface {
	ListServerTransitionsSince(
		ctx context.Context,
		tenantID, dimension string,
		since time.Time,
		limit int,
	) ([]controlplane.ServerStateTransition, error)
}

// AvailabilityRouteConfig wires the read-only availability projection.
type AvailabilityRouteConfig struct {
	Store       controlplane.ServerRuntimeStore
	Transitions serverTransitionWindowReader
	Now         func() time.Time
}

type availabilityHandlers struct {
	store       controlplane.ServerRuntimeStore
	transitions serverTransitionWindowReader
	now         func() time.Time
}

// availabilityWindows is the closed set of windows the surface offers. It is a
// closed set rather than a free duration because every window is a full table
// scan of the timeline, and an unbounded one is a denial-of-service knob.
var availabilityWindows = map[string]time.Duration{
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
	"90d": 90 * 24 * time.Hour,
}

// RegisterAvailabilityRoutes exposes the derived availability read model.
func RegisterAvailabilityRoutes(r *httpx.Router, cfg AvailabilityRouteConfig) {
	if cfg.Store == nil || cfg.Transitions == nil {
		return
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	h := availabilityHandlers{store: cfg.Store, transitions: cfg.Transitions, now: cfg.Now}
	r.GET("/api/v1/monitor/availability", h.report)
}

func (h availabilityHandlers) report(e *httpx.Event) error {
	ownerID, isAdmin, ok := authenticatedUser(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.monitor.availability")
	if tenantErr != nil {
		return tenantErr
	}

	windowKey := strings.TrimSpace(e.Request.URL.Query().Get("window"))
	if windowKey == "" {
		windowKey = "30d"
	}
	span, known := availabilityWindows[windowKey]
	if !known {
		return httpx.BadRequest(e, "Window must be one of 24h, 7d, 30d, 90d", nil)
	}
	serverFilter := strings.TrimSpace(e.Request.URL.Query().Get("server_id"))

	end := h.now().UTC()
	window := availability.Window{Start: end.Add(-span), End: end}

	servers, err := h.store.ListServerRuntimesByTenant(e.Request.Context(), tenantID, "")
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Server inventory is unavailable", nil)
	}

	// The projection reads the connection dimension only; asking the store for
	// one dimension keeps the scan proportional to what is actually folded.
	transitions, err := h.transitions.ListServerTransitionsSince(
		e.Request.Context(), tenantID, "connection", window.Start, 0)
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Availability history is unavailable", nil)
	}
	byServer := map[string][]controlplane.ServerStateTransition{}
	for _, transition := range transitions {
		byServer[transition.ServerID] = append(byServer[transition.ServerID], transition)
	}

	inputs := make([]availability.Input, 0, len(servers))
	qualifiers := make([]string, 0, len(servers))
	for i := range servers {
		server := servers[i]
		if !serverRuntimeOwnedBy(server, ownerID) && !isAdmin {
			continue
		}
		if serverFilter != "" && server.ID != serverFilter {
			continue
		}
		if serverFilter == "" && serverRuntimeHiddenFromCurrentInventory(server) {
			continue
		}
		target := serverregistry.NormalizeRuntimeTarget(server.RuntimeTarget)
		inputs = append(inputs, availability.Input{
			ServerID:     server.ID,
			Name:         serverRuntimeDisplayName(server, target),
			EnrolledAt:   server.CreatedAt,
			CurrentState: server.ConnectionState,
			Transitions:  byServer[server.ID],
		})
		qualifiers = append(qualifiers, serverregistry.DisplayQualifier(target.ProviderID, string(target.Offering), server.ID))
	}
	qualifyAvailabilityInputNames(inputs, qualifiers)
	if serverFilter != "" && len(inputs) == 0 {
		return httpx.NotFound(e, "Server not found")
	}

	report := availability.BuildFleetReport(window, inputs)
	return httpx.Success(e, http.StatusOK, availabilityResponse{
		Window:      windowKey,
		FleetReport: report,
		// The vocabulary is published with the numbers so a reader never has
		// to guess how a state was accounted for.
		Accounting: availabilityAccounting{
			Up:         []string{string(serverregistry.ConnectionConnected), string(serverregistry.ConnectionDegraded)},
			Down:       []string{string(serverregistry.ConnectionStale), string(serverregistry.ConnectionOffline), string(serverregistry.ConnectionRevoked)},
			Unobserved: []string{string(serverregistry.ConnectionPending), string(serverregistry.ConnectionConnecting)},
		},
	})
}

func qualifyAvailabilityInputNames(inputs []availability.Input, qualifiers []string) {
	names := make([]string, len(inputs))
	for i := range inputs {
		names[i] = inputs[i].Name
	}
	for i, name := range qualifyCollidingServerNames(names, qualifiers) {
		inputs[i].Name = name
	}
}

type availabilityAccounting struct {
	Up         []string `json:"up"`
	Down       []string `json:"down"`
	Unobserved []string `json:"unobserved"`
}

type availabilityResponse struct {
	Window string `json:"window"`
	availability.FleetReport
	Accounting availabilityAccounting `json:"accounting"`
}
