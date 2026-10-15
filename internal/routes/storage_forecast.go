package routes

import (
	"crypto/sha256"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/monitoring"
	"github.com/kombifyio/techstack/pkg/storageforecast"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
)

const (
	storageForecastWindow      = 14 * 24 * time.Hour
	storageForecastStep        = time.Hour
	storageForecastMaxSeries   = 384
	storageForecastMaxPoints   = 130000
	storageForecastMetricLabel = "storage_metric"
)

type storageForecastResource struct {
	ID       string                   `json:"id"`
	Labels   map[string]string        `json:"labels"`
	Forecast storageforecast.Forecast `json:"forecast"`
}

type storageForecastResponse struct {
	GeneratedAt time.Time                 `json:"generated_at"`
	WindowStart time.Time                 `json:"window_start"`
	WindowEnd   time.Time                 `json:"window_end"`
	Resources   []storageForecastResource `json:"resources"`
}

// storageForecast reads the existing filesystem gauges. The paired timestamp
// query distinguishes a source observation from PromQL's evaluation grid; an
// unchanged value or repeated lookback sample is never an extra observation.
func (h monitoringRouteHandlers) storageForecast(e *httpx.Event) error {
	tenantID, err := monitoringTenantID(e)
	if err != nil {
		return err
	}
	selector := `{__name__=~"node_disk_(used|total|free)_bytes",mount!=""`
	for _, key := range []string{"node_id", "server_id", "agent_id"} {
		value := strings.TrimSpace(e.Request.URL.Query().Get(key))
		if len(value) > 128 {
			return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "storage identity is too long", nil)
		}
		if value != "" {
			selector += "," + key + "=" + strconv.Quote(value)
		}
	}
	selector += "}"
	selector, err = scopeMonitoringQuery(selector, tenantID)
	if err != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, err.Error(), nil)
	}
	query := fmt.Sprintf(`label_replace(%s,"%s","$1","__name__","(.+)")`, selector, storageForecastMetricLabel)
	// timestamp must wrap a direct selector: wrapping label_replace would
	// expose evaluation times instead of the original observation times.
	timeQueries := make([]string, 0, 3)
	for _, metric := range []string{"node_disk_used_bytes", "node_disk_total_bytes", "node_disk_free_bytes"} {
		metricSelector := strings.Replace(selector, `__name__=~"node_disk_(used|total|free)_bytes"`, `__name__="`+metric+`"`, 1)
		timeQueries = append(timeQueries, fmt.Sprintf(`label_replace(timestamp(%s),"%s","%s","","")`, metricSelector, storageForecastMetricLabel, metric))
	}
	now := time.Now().UTC()
	if h.metadata.Now != nil {
		now = h.metadata.Now().UTC()
	}
	start := now.Add(-storageForecastWindow)
	ctx, cancel := h.policy.context(e.Request.Context())
	defer cancel()
	values, err := h.backend.RangeQuery(ctx, query, start, now, storageForecastStep)
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeInternal, "storage measurements unavailable", nil)
	}
	if _, err := storageForecastMatrix(values); err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeInternal, err.Error(), nil)
	}
	sourceTimes, err := h.backend.RangeQuery(ctx, strings.Join(timeQueries, " or "), start, now, storageForecastStep)
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeInternal, "storage measurement timestamps unavailable", nil)
	}
	resources, err := projectStorageForecast(values, sourceTimes, now)
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeInternal, err.Error(), nil)
	}
	return httpx.Success(e, http.StatusOK, storageForecastResponse{GeneratedAt: now, WindowStart: start, WindowEnd: now, Resources: resources})
}

type storageMeasurementGroup struct {
	labels   map[string]string
	metrics  map[string]map[int64]float64
	unpaired bool
	conflict bool
}

func storageForecastMatrix(result *monitoring.QueryResult) (promql.Matrix, error) {
	if result == nil || result.Value == nil {
		return nil, fmt.Errorf("storage query returned no result")
	}
	matrix, ok := result.Value.(promql.Matrix)
	if !ok {
		return nil, fmt.Errorf("storage query returned an unsupported result")
	}
	if len(matrix) > storageForecastMaxSeries {
		return nil, fmt.Errorf("storage query exceeds the filesystem limit; select a node")
	}
	points := 0
	for _, series := range matrix {
		points += len(series.Floats)
		if len(series.Histograms) > 0 {
			return nil, fmt.Errorf("storage gauges contain unsupported histograms")
		}
		if points > storageForecastMaxPoints {
			return nil, fmt.Errorf("storage query exceeds the measurement limit; select a node")
		}
	}
	return matrix, nil
}

func storageSeriesLabels(series labels.Labels) (map[string]string, string, string) {
	stable := map[string]string{}
	metric := series.Get(storageForecastMetricLabel)
	series.Range(func(label labels.Label) {
		if label.Name != "__name__" && label.Name != storageForecastMetricLabel {
			stable[label.Name] = label.Value
		}
	})
	return stable, labels.FromMap(stable).String(), metric
}

func projectStorageForecast(values, sourceTimes *monitoring.QueryResult, now time.Time) ([]storageForecastResource, error) {
	matrix, err := storageForecastMatrix(values)
	if err != nil {
		return nil, err
	}
	timestamps, err := storageForecastMatrix(sourceTimes)
	if err != nil {
		return nil, err
	}
	times := map[string]map[int64]int64{}
	for _, series := range timestamps {
		_, key, metric := storageSeriesLabels(series.Metric)
		index := key + "\x00" + metric
		if times[index] != nil {
			return nil, fmt.Errorf("storage timestamps contain ambiguous series")
		}
		times[index] = map[int64]int64{}
		for _, point := range series.Floats {
			if math.IsNaN(point.F) || math.IsInf(point.F, 0) || point.F <= 0 || point.F > float64(now.Unix())+1 {
				continue
			}
			times[index][point.T] = int64(math.Round(point.F * 1000))
		}
	}
	groups := map[string]*storageMeasurementGroup{}
	for _, series := range matrix {
		stable, key, metric := storageSeriesLabels(series.Metric)
		if stable["mount"] == "" {
			continue
		}
		if metric != "node_disk_used_bytes" && metric != "node_disk_total_bytes" && metric != "node_disk_free_bytes" {
			continue
		}
		group := groups[key]
		if group == nil {
			group = &storageMeasurementGroup{labels: stable, metrics: map[string]map[int64]float64{}}
			groups[key] = group
		}
		if group.metrics[metric] != nil {
			return nil, fmt.Errorf("storage measurements contain ambiguous series")
		}
		group.metrics[metric] = map[int64]float64{}
		for _, point := range series.Floats {
			at, paired := times[key+"\x00"+metric][point.T]
			if !paired {
				group.unpaired = true
				continue
			}
			if at < now.Add(-storageForecastWindow).UnixMilli() {
				continue
			}
			if previous, exists := group.metrics[metric][at]; exists && previous != point.F {
				group.conflict = true
			}
			group.metrics[metric][at] = point.F
		}
	}
	keys := make([]string, 0, len(groups))
	streamCounts := map[string]int{}
	for key := range groups {
		keys = append(keys, key)
		streamCounts[storageFilesystemIdentity(groups[key].labels)]++
	}
	sort.Strings(keys)
	resources := make([]storageForecastResource, 0, len(keys))
	for _, key := range keys {
		group := groups[key]
		samples := make([]storageforecast.Sample, 0, len(group.metrics["node_disk_used_bytes"]))
		missingCapacity, effectiveCapacity := false, false
		for at, used := range group.metrics["node_disk_used_bytes"] {
			sample := storageforecast.Sample{At: time.UnixMilli(at).UTC(), UsedBytes: used}
			free, hasFree := group.metrics["node_disk_free_bytes"][at]
			if hasFree {
				sample.FreeBytes = &free
			}
			capacity, hasCapacity := group.metrics["node_disk_total_bytes"][at]
			if hasCapacity {
				sample.CapacityBytes = capacity
			} else if hasFree && free >= 0 && used >= 0 {
				sample.CapacityBytes = used + free
				effectiveCapacity = true
			} else {
				missingCapacity = true
			}
			samples = append(samples, sample)
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i].At.Before(samples[j].At) })
		forecast := storageforecast.Estimate(samples, now)
		forecast.Notes = append(forecast.Notes, "hourly_sampled")
		if streamCounts[storageFilesystemIdentity(group.labels)] > 1 {
			forecast.Notes = append(forecast.Notes, "duplicate_streams")
		}
		if group.unpaired {
			forecast.Notes = append(forecast.Notes, "measurement_timestamp_unpaired")
			forecast.State, forecast.Reason, forecast.Exhaustion = "insufficient_data", "invalid_measurements", nil
		}
		if group.conflict {
			forecast.Notes = append(forecast.Notes, "conflicting_measurements")
			forecast.State, forecast.Reason, forecast.Exhaustion = "insufficient_data", "conflicting_measurements", nil
		}
		if missingCapacity {
			forecast.Notes = append(forecast.Notes, "capacity_measurement_unpaired")
		}
		if effectiveCapacity {
			forecast.Notes = append(forecast.Notes, "effective_capacity")
		}
		if group.labels["origin"] == "otlp" && group.labels["source"] == "guard-telemetry" {
			forecast.Notes = append(forecast.Notes, "collection_timestamps")
		}
		digest := sha256.Sum256([]byte(key))
		resources = append(resources, storageForecastResource{ID: fmt.Sprintf("filesystem-%x", digest[:12]), Labels: group.labels, Forecast: forecast})
	}
	return resources, nil
}

func storageFilesystemIdentity(stable map[string]string) string {
	identity := make(map[string]string, len(stable))
	for key, value := range stable {
		switch key {
		case "origin", "source", "ingest_protocol", "otel_scope_name", "otel_scope_version":
			continue
		}
		identity[key] = value
	}
	return labels.FromMap(identity).String()
}
