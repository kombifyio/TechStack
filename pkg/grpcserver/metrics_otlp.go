package grpcserver

import (
	"context"
	"encoding/base64"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/kombifyio/techstack/pkg/monitoring"
	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// Export implements the OTLP MetricsService gRPC API beside the legacy
// PushMetrics path. This keeps mixed fleets working during migration.
func (s *Server) Export(ctx context.Context, req *collectormetricspb.ExportMetricsServiceRequest) (*collectormetricspb.ExportMetricsServiceResponse, error) {
	_ = ctx
	rejected := countOTLPDataPoints(req)

	if s.monitorTSDB == nil {
		if s.monitorIngestHealth != nil {
			s.monitorIngestHealth.recordOTLPRejected(int(rejected), "monitoring not enabled")
		}
		return &collectormetricspb.ExportMetricsServiceResponse{
			PartialSuccess: &collectormetricspb.ExportMetricsPartialSuccess{
				RejectedDataPoints: rejected,
				ErrorMessage:       "monitoring not enabled",
			},
		}, nil
	}

	samples := otlpMetricsToSamples(req)
	if len(samples) == 0 {
		if s.monitorIngestHealth != nil {
			s.monitorIngestHealth.recordOTLPSuccess(0)
		}
		return &collectormetricspb.ExportMetricsServiceResponse{}, nil
	}

	if err := s.monitorTSDB.Write(samples); err != nil {
		if s.monitorIngestHealth != nil {
			s.monitorIngestHealth.recordOTLPRejected(int(rejected), err.Error())
		}
		s.log.Warn("otlp_metrics_write_error",
			"resource_metrics", len(req.GetResourceMetrics()),
			"samples", len(samples),
			"error", err.Error(),
		)
		return &collectormetricspb.ExportMetricsServiceResponse{
			PartialSuccess: &collectormetricspb.ExportMetricsPartialSuccess{
				RejectedDataPoints: rejected,
				ErrorMessage:       err.Error(),
			},
		}, nil
	}

	s.log.Debug("otlp_metrics_accepted",
		"resource_metrics", len(req.GetResourceMetrics()),
		"samples", len(samples),
	)
	if s.monitorIngestHealth != nil {
		s.monitorIngestHealth.recordOTLPSuccess(len(samples))
	}

	return &collectormetricspb.ExportMetricsServiceResponse{}, nil
}

func countOTLPDataPoints(req *collectormetricspb.ExportMetricsServiceRequest) int64 {
	if req == nil {
		return 0
	}

	var count int64
	for _, rm := range req.GetResourceMetrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, metric := range sm.GetMetrics() {
				switch data := metric.Data.(type) {
				case *metricspb.Metric_Gauge:
					count += int64(len(data.Gauge.GetDataPoints()))
				case *metricspb.Metric_Sum:
					count += int64(len(data.Sum.GetDataPoints()))
				case *metricspb.Metric_Histogram:
					count += int64(len(data.Histogram.GetDataPoints()))
				case *metricspb.Metric_Summary:
					count += int64(len(data.Summary.GetDataPoints()))
				case *metricspb.Metric_ExponentialHistogram:
					count += int64(len(data.ExponentialHistogram.GetDataPoints()))
				}
			}
		}
	}
	return count
}

func otlpMetricsToSamples(req *collectormetricspb.ExportMetricsServiceRequest) []monitoring.MetricSample {
	if req == nil {
		return nil
	}

	samples := make([]monitoring.MetricSample, 0)
	for _, rm := range req.GetResourceMetrics() {
		resourceLabels := extractOTLPLabels(map[string]string{"ingest_protocol": "otlp"}, rm.GetResource().GetAttributes())
		for _, sm := range rm.GetScopeMetrics() {
			scopeLabels := cloneLabels(resourceLabels)
			if scope := sm.GetScope(); scope != nil {
				if scope.GetName() != "" {
					scopeLabels["otel_scope_name"] = scope.GetName()
				}
				if scope.GetVersion() != "" {
					scopeLabels["otel_scope_version"] = scope.GetVersion()
				}
			}

			for _, metric := range sm.GetMetrics() {
				metricName := sanitizePromMetricName(metric.GetName())
				switch data := metric.Data.(type) {
				case *metricspb.Metric_Gauge:
					samples = appendNumberDataPoints(samples, metricName, scopeLabels, data.Gauge.GetDataPoints())
				case *metricspb.Metric_Sum:
					samples = appendNumberDataPoints(samples, metricName, scopeLabels, data.Sum.GetDataPoints())
				case *metricspb.Metric_Histogram:
					samples = appendHistogramDataPoints(samples, metricName, scopeLabels, data.Histogram.GetDataPoints())
				case *metricspb.Metric_ExponentialHistogram:
					samples = appendExponentialHistogramDataPoints(samples, metricName, scopeLabels, data.ExponentialHistogram.GetDataPoints())
				case *metricspb.Metric_Summary:
					samples = appendSummaryDataPoints(samples, metricName, scopeLabels, data.Summary.GetDataPoints())
				}
			}
		}
	}

	return samples
}

func appendNumberDataPoints(samples []monitoring.MetricSample, metricName string, baseLabels map[string]string, points []*metricspb.NumberDataPoint) []monitoring.MetricSample {
	for _, point := range points {
		labels := extractOTLPLabels(baseLabels, point.GetAttributes())
		samples = append(samples, monitoring.MetricSample{
			Name:      metricName,
			Value:     numberDataPointValue(point),
			Labels:    labels,
			Timestamp: otlpTimestamp(point.GetTimeUnixNano()),
		})
	}
	return samples
}

func appendHistogramDataPoints(samples []monitoring.MetricSample, metricName string, baseLabels map[string]string, points []*metricspb.HistogramDataPoint) []monitoring.MetricSample {
	for _, point := range points {
		labels := extractOTLPLabels(baseLabels, point.GetAttributes())
		ts := otlpTimestamp(point.GetTimeUnixNano())
		samples = append(samples,
			monitoring.MetricSample{Name: metricName + "_count", Value: float64(point.GetCount()), Labels: cloneLabels(labels), Timestamp: ts},
			monitoring.MetricSample{Name: metricName + "_sum", Value: point.GetSum(), Labels: cloneLabels(labels), Timestamp: ts},
		)
	}
	return samples
}

func appendExponentialHistogramDataPoints(samples []monitoring.MetricSample, metricName string, baseLabels map[string]string, points []*metricspb.ExponentialHistogramDataPoint) []monitoring.MetricSample {
	for _, point := range points {
		labels := extractOTLPLabels(baseLabels, point.GetAttributes())
		ts := otlpTimestamp(point.GetTimeUnixNano())
		samples = append(samples,
			monitoring.MetricSample{Name: metricName + "_count", Value: float64(point.GetCount()), Labels: cloneLabels(labels), Timestamp: ts},
			monitoring.MetricSample{Name: metricName + "_sum", Value: point.GetSum(), Labels: cloneLabels(labels), Timestamp: ts},
		)
	}
	return samples
}

func appendSummaryDataPoints(samples []monitoring.MetricSample, metricName string, baseLabels map[string]string, points []*metricspb.SummaryDataPoint) []monitoring.MetricSample {
	for _, point := range points {
		labels := extractOTLPLabels(baseLabels, point.GetAttributes())
		ts := otlpTimestamp(point.GetTimeUnixNano())
		samples = append(samples,
			monitoring.MetricSample{Name: metricName + "_count", Value: float64(point.GetCount()), Labels: cloneLabels(labels), Timestamp: ts},
			monitoring.MetricSample{Name: metricName + "_sum", Value: point.GetSum(), Labels: cloneLabels(labels), Timestamp: ts},
		)
		for _, quantile := range point.GetQuantileValues() {
			quantileLabels := cloneLabels(labels)
			quantileLabels["quantile"] = strconv.FormatFloat(quantile.GetQuantile(), 'f', -1, 64)
			samples = append(samples, monitoring.MetricSample{
				Name:      metricName,
				Value:     quantile.GetValue(),
				Labels:    quantileLabels,
				Timestamp: ts,
			})
		}
	}
	return samples
}

func numberDataPointValue(point *metricspb.NumberDataPoint) float64 {
	switch value := point.Value.(type) {
	case *metricspb.NumberDataPoint_AsDouble:
		return value.AsDouble
	case *metricspb.NumberDataPoint_AsInt:
		return float64(value.AsInt)
	default:
		return 0
	}
}

func extractOTLPLabels(base map[string]string, attrs []*commonpb.KeyValue) map[string]string {
	labels := cloneLabels(base)
	for _, attr := range attrs {
		if attr == nil {
			continue
		}
		key := sanitizePromLabelName(attr.GetKey())
		value := otlpAnyValueString(attr.GetValue())
		if key == "" || value == "" {
			continue
		}
		labels[key] = value
	}
	return labels
}

func otlpAnyValueString(value *commonpb.AnyValue) string {
	if value == nil {
		return ""
	}

	switch v := value.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return v.StringValue
	case *commonpb.AnyValue_BoolValue:
		return strconv.FormatBool(v.BoolValue)
	case *commonpb.AnyValue_IntValue:
		return strconv.FormatInt(v.IntValue, 10)
	case *commonpb.AnyValue_DoubleValue:
		return strconv.FormatFloat(v.DoubleValue, 'f', -1, 64)
	case *commonpb.AnyValue_BytesValue:
		return base64.StdEncoding.EncodeToString(v.BytesValue)
	case *commonpb.AnyValue_ArrayValue:
		parts := make([]string, 0, len(v.ArrayValue.GetValues()))
		for _, item := range v.ArrayValue.GetValues() {
			if s := otlpAnyValueString(item); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ",")
	case *commonpb.AnyValue_KvlistValue:
		parts := make([]string, 0, len(v.KvlistValue.GetValues()))
		for _, item := range v.KvlistValue.GetValues() {
			if item == nil {
				continue
			}
			parts = append(parts, item.GetKey()+"="+otlpAnyValueString(item.GetValue()))
		}
		return strings.Join(parts, ",")
	default:
		return ""
	}
}

func otlpTimestamp(unixNano uint64) time.Time {
	if unixNano == 0 {
		return time.Now().UTC()
	}
	return time.Unix(0, unixNanoToInt64(unixNano)).UTC()
}

func cloneLabels(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func sanitizePromMetricName(name string) string {
	return sanitizePromName(name, "otlp_metric", true)
}

func sanitizePromLabelName(name string) string {
	return sanitizePromName(name, "otlp_label", false)
}

func sanitizePromName(name, fallback string, allowColon bool) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return fallback
	}

	var builder strings.Builder
	for index, r := range name {
		valid := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
		if allowColon && r == ':' {
			valid = true
		}
		if index == 0 && unicode.IsDigit(r) {
			valid = false
		}
		if valid {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('_')
		}
	}

	result := builder.String()
	result = strings.Trim(result, "_")
	if result == "" {
		return fallback
	}
	if unicode.IsDigit(rune(result[0])) {
		return fallback + "_" + result
	}
	return result
}
