package monitoring

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/parser"
)

// HTTPPromQLBackendConfig configures a PromQL-compatible HTTP query backend.
type HTTPPromQLBackendConfig struct {
	BaseURL string
	Timeout time.Duration
	Logger  *slog.Logger
}

// HTTPPromQLBackend reads metrics and labels from a remote PromQL-compatible
// API such as VictoriaMetrics while preserving the existing QueryResult shape.
type HTTPPromQLBackend struct {
	baseURL *url.URL
	client  *http.Client
	logger  *slog.Logger
}

type promAPIEnvelope struct {
	Status    string          `json:"status"`
	Data      json.RawMessage `json:"data"`
	ErrorType string          `json:"errorType"`
	Error     string          `json:"error"`
}

type promQueryData struct {
	ResultType string          `json:"resultType"`
	Result     json.RawMessage `json:"result"`
}

type promVectorSample struct {
	Metric map[string]string `json:"metric"`
	Value  []any             `json:"value"`
}

type promMatrixSeries struct {
	Metric map[string]string `json:"metric"`
	Values [][]any           `json:"values"`
}

type promStringValue struct {
	Timestamp int64
	Value     string
}

func (v promStringValue) Type() parser.ValueType { return parser.ValueTypeString }

func (v promStringValue) String() string {
	return fmt.Sprintf("%q @[%d]", v.Value, v.Timestamp)
}

// NewHTTPPromQLBackend creates a backend client for remote PromQL-compatible APIs.
func NewHTTPPromQLBackend(cfg HTTPPromQLBackendConfig) (*HTTPPromQLBackend, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, fmt.Errorf("base URL is required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	parsed, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("base URL must include scheme and host")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")

	return &HTTPPromQLBackend{
		baseURL: parsed,
		client:  &http.Client{Timeout: cfg.Timeout},
		logger:  cfg.Logger,
	}, nil
}

// RedactedBaseURL returns the backend URL without credentials.
func (b *HTTPPromQLBackend) RedactedBaseURL() string {
	redacted := *b.baseURL
	redacted.User = nil
	return redacted.String()
}

func (b *HTTPPromQLBackend) InstantQuery(ctx context.Context, query string, ts time.Time) (*QueryResult, error) {
	params := url.Values{"query": {query}}
	if !ts.IsZero() {
		params.Set("time", ts.Format(time.RFC3339Nano))
	}
	return b.query(ctx, "/api/v1/query", params)
}

func (b *HTTPPromQLBackend) RangeQuery(ctx context.Context, query string, start, end time.Time, step time.Duration) (*QueryResult, error) {
	params := url.Values{"query": {query}}
	if !start.IsZero() {
		params.Set("start", start.Format(time.RFC3339Nano))
	}
	if !end.IsZero() {
		params.Set("end", end.Format(time.RFC3339Nano))
	}
	if step > 0 {
		params.Set("step", step.String())
	}
	return b.query(ctx, "/api/v1/query_range", params)
}

func (b *HTTPPromQLBackend) LabelNames(ctx context.Context) ([]string, error) {
	data, err := b.request(ctx, "/api/v1/labels", nil)
	if err != nil {
		return nil, err
	}
	var names []string
	if err := json.Unmarshal(data, &names); err != nil {
		return nil, fmt.Errorf("decode label names: %w", err)
	}
	return names, nil
}

func (b *HTTPPromQLBackend) LabelValues(ctx context.Context, name string) ([]string, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("label name is required")
	}
	data, err := b.request(ctx, path.Join("/api/v1/label", url.PathEscape(name), "values"), nil)
	if err != nil {
		return nil, err
	}
	var values []string
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("decode label values: %w", err)
	}
	return values, nil
}

func (b *HTTPPromQLBackend) MetricNames(ctx context.Context) ([]string, error) {
	return b.LabelValues(ctx, labels.MetricName)
}

func (b *HTTPPromQLBackend) Stats(ctx context.Context) (*TSDBStats, error) {
	stats := &TSDBStats{}
	if data, err := b.request(ctx, "/api/v1/status/tsdb", nil); err == nil {
		applyRemoteStats(stats, data)
	} else {
		b.logger.Debug("remote stats endpoint unavailable", "backend", b.RedactedBaseURL(), "error", err)
	}

	names, err := b.MetricNames(ctx)
	if err != nil {
		return stats, err
	}
	counts := make([]MetricCount, 0, len(names))
	for _, name := range names {
		counts = append(counts, MetricCount{Name: name, Count: 1})
	}
	sort.Slice(counts, func(i, j int) bool { return counts[i].Name < counts[j].Name })
	if len(counts) > 20 {
		counts = counts[:20]
	}
	stats.TopMetrics = counts
	return stats, nil
}

func (b *HTTPPromQLBackend) query(ctx context.Context, endpoint string, params url.Values) (*QueryResult, error) {
	data, err := b.request(ctx, endpoint, params)
	if err != nil {
		return nil, err
	}

	var payload promQueryData
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode query data: %w", err)
	}

	value, err := decodePromValue(payload.ResultType, payload.Result)
	if err != nil {
		return nil, err
	}
	return &QueryResult{Value: value}, nil
}

func (b *HTTPPromQLBackend) request(ctx context.Context, endpoint string, params url.Values) (json.RawMessage, error) {
	reqURL := *b.baseURL
	reqURL.Path = strings.TrimRight(reqURL.Path, "/") + endpoint
	reqURL.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("request %s returned %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var envelope promAPIEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("decode response envelope: %w", err)
	}
	if envelope.Status != "success" {
		return nil, fmt.Errorf("promql backend error (%s): %s", envelope.ErrorType, envelope.Error)
	}
	return envelope.Data, nil
}

func decodePromValue(resultType string, raw json.RawMessage) (parser.Value, error) {
	switch resultType {
	case string(parser.ValueTypeVector):
		var vectorPayload []promVectorSample
		if err := json.Unmarshal(raw, &vectorPayload); err != nil {
			return nil, fmt.Errorf("decode vector result: %w", err)
		}
		vector := make(promql.Vector, 0, len(vectorPayload))
		for _, sample := range vectorPayload {
			timestamp, value, err := decodePromPoint(sample.Value)
			if err != nil {
				return nil, err
			}
			vector = append(vector, promql.Sample{
				T:      timestamp,
				F:      value,
				Metric: labels.FromMap(sample.Metric),
			})
		}
		return vector, nil
	case string(parser.ValueTypeMatrix):
		var matrixPayload []promMatrixSeries
		if err := json.Unmarshal(raw, &matrixPayload); err != nil {
			return nil, fmt.Errorf("decode matrix result: %w", err)
		}
		matrix := make(promql.Matrix, 0, len(matrixPayload))
		for _, series := range matrixPayload {
			floats := make([]promql.FPoint, 0, len(series.Values))
			for _, point := range series.Values {
				timestamp, value, err := decodePromPoint(point)
				if err != nil {
					return nil, err
				}
				floats = append(floats, promql.FPoint{T: timestamp, F: value})
			}
			matrix = append(matrix, promql.Series{
				Metric: labels.FromMap(series.Metric),
				Floats: floats,
			})
		}
		return matrix, nil
	case string(parser.ValueTypeScalar):
		var scalarPayload []any
		if err := json.Unmarshal(raw, &scalarPayload); err != nil {
			return nil, fmt.Errorf("decode scalar result: %w", err)
		}
		timestamp, value, err := decodePromPoint(scalarPayload)
		if err != nil {
			return nil, err
		}
		return promql.Scalar{T: timestamp, V: value}, nil
	case string(parser.ValueTypeString):
		var stringPayload []any
		if err := json.Unmarshal(raw, &stringPayload); err != nil {
			return nil, fmt.Errorf("decode string result: %w", err)
		}
		if len(stringPayload) != 2 {
			return nil, fmt.Errorf("unexpected string result shape")
		}
		timestamp, err := decodePromTimestamp(stringPayload[0])
		if err != nil {
			return nil, err
		}
		return promStringValue{Timestamp: timestamp, Value: fmt.Sprint(stringPayload[1])}, nil
	default:
		return nil, fmt.Errorf("unsupported result type %q", resultType)
	}
}

func decodePromPoint(point []any) (int64, float64, error) {
	if len(point) != 2 {
		return 0, 0, fmt.Errorf("unexpected promql point shape")
	}
	timestamp, err := decodePromTimestamp(point[0])
	if err != nil {
		return 0, 0, err
	}
	value, err := decodePromFloat(point[1])
	if err != nil {
		return 0, 0, err
	}
	return timestamp, value, nil
}

func decodePromTimestamp(value any) (int64, error) {
	switch typed := value.(type) {
	case float64:
		return int64(math.Round(typed * 1000)), nil
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		if err != nil {
			return 0, fmt.Errorf("parse timestamp %q: %w", typed, err)
		}
		return int64(math.Round(parsed * 1000)), nil
	default:
		return 0, fmt.Errorf("unsupported timestamp type %T", value)
	}
}

func decodePromFloat(value any) (float64, error) {
	switch typed := value.(type) {
	case float64:
		return typed, nil
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		if err != nil {
			return 0, fmt.Errorf("parse float %q: %w", typed, err)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("unsupported numeric value type %T", value)
	}
}

func applyRemoteStats(stats *TSDBStats, raw json.RawMessage) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}
	stats.NumSeries = lookupUint64(payload, "seriesCount", "numSeries")
	stats.MinTime = lookupInt64(payload, "minTimeMs", "minTime")
	stats.MaxTime = lookupInt64(payload, "maxTimeMs", "maxTime")
	if stats.MaxTime > stats.MinTime {
		stats.RetentionDays = float64(stats.MaxTime-stats.MinTime) / float64(24*60*60*1000)
	}
}

func lookupUint64(payload map[string]any, keys ...string) uint64 {
	for _, key := range keys {
		if value, ok := payload[key]; ok {
			switch typed := value.(type) {
			case float64:
				return nonNegativeFloatToUint64(typed)
			case int64:
				return nonNegativeInt64ToUint64(typed)
			case json.Number:
				if parsed, err := typed.Int64(); err == nil {
					return nonNegativeInt64ToUint64(parsed)
				}
			}
		}
	}
	if nested, ok := payload["headStats"].(map[string]any); ok {
		return lookupUint64(nested, keys...)
	}
	return 0
}

func nonNegativeFloatToUint64(value float64) uint64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 0
	}
	if value > float64(math.MaxUint64) {
		return math.MaxUint64
	}
	// #nosec G115 -- value is finite, non-negative, and clamped to uint64 range above.
	return uint64(value)
}

func nonNegativeInt64ToUint64(value int64) uint64 {
	if value <= 0 {
		return 0
	}
	// #nosec G115 -- value is known positive.
	return uint64(value)
}

func lookupInt64(payload map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if value, ok := payload[key]; ok {
			switch typed := value.(type) {
			case float64:
				return int64(typed)
			case int64:
				return typed
			case json.Number:
				if parsed, err := typed.Int64(); err == nil {
					return parsed
				}
			}
		}
	}
	if nested, ok := payload["headStats"].(map[string]any); ok {
		return lookupInt64(nested, keys...)
	}
	return 0
}
