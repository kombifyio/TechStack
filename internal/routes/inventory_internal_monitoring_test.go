package routes

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/kombifyio/techstack/pkg/controlplane"
)

// The history read answers only the signed owner's own servers, and a figure
// the Guard never reported stays absent from the point.
func TestInternalRuntimeHistoryIsOwnerScopedFromSignedCloudIdentity(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	store := seedRuntimeSummaryStore(t, now).(*controlplane.MemoryStore)
	cpu, memory := 30.0, 55.5
	for serverID, owned := range map[string]bool{"server-owner": true, "server-foreign": false} {
		sample := controlplane.MetricSample{CPU: &cpu}
		if owned {
			sample.Memory = &memory
		}
		if err := store.RecordServerMetricSample(t.Context(), "tenant-1", serverID, now.Add(-5*time.Minute), sample); err != nil {
			t.Fatal(err)
		}
	}
	h := newTestInventoryHandlers(store, func() time.Time { return now })
	h.serviceAuthSecret = runtimeSummaryTestSecret

	token := runtimeSummaryCloudToken(t, &servicecall.OnBehalfOf{Sub: "auth0|owner", OrgID: "tenant-1"})
	e, rec := runtimeSummaryTestEvent("/api/v1/internal/runtime/history?range=1h", token)
	if err := h.httpInternalRuntimeHistory(e); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "server-foreign") {
		t.Fatalf("history leaked another owner's server: %s", rec.Body.String())
	}
	var history struct {
		Range       string `json:"range"`
		StepSeconds int    `json:"step_seconds"`
		Servers     []struct {
			ServerID string `json:"server_id"`
			Points   []struct {
				CPU    *float64 `json:"cpu"`
				Memory *float64 `json:"memory"`
				Disk   *float64 `json:"disk"`
			} `json:"points"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if history.Range != "1h" || history.StepSeconds != 60 || len(history.Servers) != 1 || history.Servers[0].ServerID != "server-owner" || len(history.Servers[0].Points) != 1 {
		t.Fatalf("history = %s", rec.Body.String())
	}
	point := history.Servers[0].Points[0]
	if point.CPU == nil || *point.CPU != 30 || point.Memory == nil || *point.Memory != 55.5 || point.Disk != nil {
		t.Fatalf("point = %#v, want cpu 30, memory 55.5 and no disk figure", point)
	}
}

// The monitoring reads share the summary's fail-closed boundary: scope cannot be
// smuggled through the read options, a bad option is refused, and a store that
// cannot serve the read answers 503 rather than an empty success.
func TestInternalRuntimeMonitoringFailsClosed(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	store := seedRuntimeSummaryStore(t, now)
	h := newTestInventoryHandlers(store, func() time.Time { return now })
	h.serviceAuthSecret = runtimeSummaryTestSecret
	token := runtimeSummaryCloudToken(t, &servicecall.OnBehalfOf{Sub: "auth0|owner", OrgID: "tenant-1"})

	cases := map[string]struct {
		target string
		want   int
	}{
		"scope in query":        {target: "/api/v1/internal/runtime/history?range=1h&tenant_id=tenant-2", want: http.StatusForbidden},
		"repeated option":       {target: "/api/v1/internal/runtime/history?range=1h&range=7d", want: http.StatusForbidden},
		"unknown range":         {target: "/api/v1/internal/runtime/history?range=90d", want: http.StatusBadRequest},
		"missing range":         {target: "/api/v1/internal/runtime/history", want: http.StatusBadRequest},
		"events limit too high": {target: "/api/v1/internal/runtime/events?limit=100000", want: http.StatusBadRequest},
		"events scope in query": {target: "/api/v1/internal/runtime/events?owner=auth0|other", want: http.StatusForbidden},
		"summary takes no read": {target: "/api/v1/internal/runtime/summary?range=1h", want: http.StatusForbidden},
	}
	for name, tc := range cases {
		e, rec := runtimeSummaryTestEvent(tc.target, token)
		var err error
		switch {
		case strings.Contains(tc.target, "/history"):
			err = h.httpInternalRuntimeHistory(e)
		case strings.Contains(tc.target, "/events"):
			err = h.httpInternalRuntimeEvents(e)
		default:
			err = h.httpInternalRuntimeSummary(e)
		}
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if rec.Code != tc.want {
			t.Fatalf("%s: expected %d, got %d: %s", name, tc.want, rec.Code, rec.Body.String())
		}
	}

	// A read store without the monitoring seam is unavailable, never empty.
	h.app.read = struct {
		controlplane.InventoryReadStore
	}{store}
	e, rec := runtimeSummaryTestEvent("/api/v1/internal/runtime/history?range=1h", token)
	if err := h.httpInternalRuntimeHistory(e); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), `"servers"`) {
		t.Fatalf("expected 503 without a monitoring store, got %d: %s", rec.Code, rec.Body.String())
	}
}
