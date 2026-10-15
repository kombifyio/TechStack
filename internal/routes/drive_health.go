package routes

import (
	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	"net/http"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/monitoring"
)

// RegisterDriveHealthRoutes exposes tenant-scoped evidence separately from
// filesystem capacity forecasting. Missing hardware evidence stays explicit.
func RegisterDriveHealthRoutes(r *httpx.Router, service *monitoring.DriveHealthService, selfHostTenantID ...string) {
	handler := func(alertsOnly bool) func(*httpx.Event) error {
		return monitoringRequireAuth(func(e *httpx.Event) error {
			tenant, err := monitoringTenantID(e)
			if err != nil {
				return err
			}
			if !tenantguard.Active() && len(selfHostTenantID) > 0 {
				tenant = selfHostTenantID[0]
			}
			if service == nil || service.Store == nil {
				return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeInternal, "drive health persistence is unavailable", map[string]any{"reason_code": "drive_health_persistence_unavailable", "retryable": true})
			}
			server := strings.TrimSpace(e.Request.URL.Query().Get("server_id"))
			if len(server) > 128 || strings.ContainsAny(server, "\x00\n\r") {
				return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "invalid server identity", nil)
			}
			ctx, cancel := defaultMonitoringQueryPolicy().context(e.Request.Context())
			defer cancel()
			response, err := service.Read(ctx, tenant, server)
			if err != nil {
				return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeInternal, "drive health evidence unavailable", nil)
			}
			if alertsOnly {
				alerts := []monitoring.DriveAlert{}
				for _, s := range response.Servers {
					alerts = append(alerts, s.Alerts...)
					for _, d := range s.Drives {
						alerts = append(alerts, d.Alerts...)
					}
				}
				return httpx.Success(e, http.StatusOK, struct {
					GeneratedAt time.Time               `json:"generated_at"`
					Alerts      []monitoring.DriveAlert `json:"alerts"`
				}{response.GeneratedAt, alerts})
			}
			return httpx.Success(e, http.StatusOK, response)
		})
	}
	r.GET("/api/v1/monitor/drive-health", handler(false))
	r.GET("/api/v1/monitor/drive-health/alerts", handler(true))
}
