package routes

import (
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/stackrouting"
)

func RegisterDomainMonitoringRoutes(router *httpx.Router, monitor *stackrouting.DomainMonitor) {
	router.GET("/api/v1/monitor/domains", func(e *httpx.Event) error {
		owner, err := requireAuth(e)
		if err != nil {
			return err
		}
		tenant, err := tenantguard.TenantScope(requestExplicitTenantID(e), owner, "techstack.monitor.domains.read")
		if err != nil {
			return err
		}
		e.Response.Header().Set("Cache-Control", "no-store")
		for _, filter := range []string{"server_id", "service_id"} {
			if len(e.Request.URL.Query().Get(filter)) > 128 {
				return httpx.BadRequest(e, "Domain monitoring filter exceeds 128 characters")
			}
		}
		if monitor == nil {
			return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Domain observations are unavailable", nil)
		}
		report, err := monitor.Report(e.Request.Context(), stackrouting.Principal{TenantID: tenant, OwnerSubjectID: owner}, strings.TrimSpace(e.Request.URL.Query().Get("server_id")), strings.TrimSpace(e.Request.URL.Query().Get("service_id")), time.Now().UTC())
		if err != nil {
			return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Domain observations could not be read", map[string]any{"reason_code": "domain_monitoring_unavailable"})
		}
		return httpx.Success(e, http.StatusOK, report)
	})
}
