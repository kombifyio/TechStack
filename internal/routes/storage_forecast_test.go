package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/monitoring"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/parser"
)

// The external backend fixture models selector authorization and timestamp's
// source-vs-evaluation semantics without creating or opening a database.
type storageRouteBackend struct {
	monitoring.MetricsQueryBackend
	at       time.Time
	requests int
}

func (b *storageRouteBackend) RangeQuery(_ context.Context, query string, _, end time.Time, _ time.Duration) (*monitoring.QueryResult, error) {
	b.requests++
	expr, err := parser.NewParser(parser.Options{}).ParseExpr(query)
	if err != nil {
		return nil, err
	}
	selectors := []*parser.VectorSelector{}
	timestampMode, directTimestamp := false, true
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		if call, ok := node.(*parser.Call); ok && call.Func.Name == "timestamp" {
			timestampMode = true
			if _, direct := call.Args[0].(*parser.VectorSelector); !direct {
				directTimestamp = false
			}
		}
		if selector, ok := node.(*parser.VectorSelector); ok {
			selectors = append(selectors, selector)
		}
		return nil
	})
	matrix := promql.Matrix{}
	for _, tenant := range []string{"tenant-one", "tenant-two"} {
		for _, metric := range []string{"node_disk_used_bytes", "node_disk_total_bytes"} {
			data := map[string]string{"__name__": metric, "tenant_id": tenant, "node_id": "shared-node-name", "mount": "/data"}
			selected := false
			for _, selector := range selectors {
				matches := true
				for _, matcher := range selector.LabelMatchers {
					if !matcher.Matches(data[matcher.Name]) {
						matches = false
					}
				}
				selected = selected || matches
			}
			if !selected {
				continue
			}
			value := 40.0
			if metric == "node_disk_total_bytes" {
				value = 100
			}
			if tenant == "tenant-two" {
				value *= 100
			}
			if timestampMode {
				value = float64(b.at.UnixMilli()) / 1000
				if !directTimestamp {
					value = float64(end.UnixMilli()) / 1000
				}
				delete(data, "__name__")
			}
			data[storageForecastMetricLabel] = metric
			matrix = append(matrix, promql.Series{Metric: labels.FromMap(data), Floats: []promql.FPoint{{T: end.UnixMilli(), F: value}}})
		}
	}
	return &monitoring.QueryResult{Value: matrix}, nil
}

// Regression boundary: a storage read must not reveal a different tenant's
// same-named node or turn query evaluation time into measurement freshness.
func TestStorageForecastTenantAndMeasurementBoundary(t *testing.T) {
	tenantguard.Configure(true)
	t.Cleanup(func() { tenantguard.Configure(false) })
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	backend := &storageRouteBackend{at: now.Add(-3 * time.Minute)}
	router := httpx.NewRouter()
	RegisterMonitoringRoutes(router, backend, MonitoringStatusMetadata{Now: func() time.Time { return now }}, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/monitor/storage-forecast?node_id=shared-node-name", nil)
	anonymous := httptest.NewRecorder()
	router.BuildMux().ServeHTTP(anonymous, request)
	if anonymous.Code != http.StatusUnauthorized || backend.requests != 0 {
		t.Fatalf("unauthenticated storage query reached backend: %d, %d", anonymous.Code, backend.requests)
	}
	request = request.WithContext(identity.NewContext(request.Context(), &identity.Identity{UserID: "auth0|storage-owner", OrgID: "tenant-one"}))
	response := httptest.NewRecorder()
	router.BuildMux().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("storage query failed: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Data storageForecastResponse `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data.Resources) != 1 {
		t.Fatalf("storage query leaked a foreign tenant: %+v", body.Data.Resources)
	}
	resource := body.Data.Resources[0]
	if resource.Labels["tenant_id"] != "tenant-one" || resource.Forecast.UsedBytes == nil || *resource.Forecast.UsedBytes != 40 {
		t.Fatalf("wrong tenant storage projection: %+v", resource)
	}
	if resource.Forecast.LatestAt == nil || !resource.Forecast.LatestAt.Equal(backend.at) {
		t.Fatalf("evaluation time replaced observation freshness: %+v", resource.Forecast)
	}
}
