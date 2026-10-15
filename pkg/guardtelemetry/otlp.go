package guardtelemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	collectormetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
)

// Default loopback ports of the Guard's OTLP sink. They differ from the
// collector's own receivers (4317, 4318) so both can run on one host.
const (
	DefaultOTLPGRPCPort = 14317
	DefaultOTLPHTTPPort = 14318
	otlpBodyMax         = 4 << 20
)

// OTLPConfig configures the loopback OTLP sink.
type OTLPConfig struct {
	// Bind must be a loopback address; anything else is refused, so the sink can
	// never be reached from another machine.
	Bind     string
	GRPCPort int
	HTTPPort int
	// Submit receives the samples of every accepted export.
	Submit func([]Sample)
	Logger *slog.Logger
}

// OTLPSink is the Guard's loopback OTLP receiver. The StackKits collector
// exports to it over loopback, and the Guard forwards the contract's data
// through its own authenticated channel: the Guard is the only egress.
type OTLPSink struct {
	cfg  OTLPConfig
	log  *slog.Logger
	grpc *grpc.Server
	http *http.Server
}

// ErrNotLoopback is returned when the sink is asked to listen beyond loopback.
var ErrNotLoopback = errors.New("guardtelemetry: the OTLP sink listens on loopback only")

func loopbackIP(bind string) (net.IP, error) {
	bind = strings.TrimSpace(bind)
	if bind == "" || strings.EqualFold(bind, "localhost") {
		return net.IPv4(127, 0, 0, 1), nil
	}
	ip := net.ParseIP(bind)
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("%w: %q", ErrNotLoopback, bind)
	}
	return ip, nil
}

// StartOTLP binds the sink. A port of zero disables that protocol.
func StartOTLP(cfg OTLPConfig) (*OTLPSink, error) {
	ip, err := loopbackIP(cfg.Bind)
	if err != nil {
		return nil, err
	}
	if cfg.Submit == nil {
		return nil, errors.New("guardtelemetry: the OTLP sink needs somewhere to send samples")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	sink := &OTLPSink{cfg: cfg, log: logger.With("component", "guard-otlp-sink")}
	if cfg.GRPCPort > 0 {
		listener, err := net.Listen("tcp", net.JoinHostPort(ip.String(), strconv.Itoa(cfg.GRPCPort)))
		if err != nil {
			return nil, err
		}
		sink.grpc = grpc.NewServer(grpc.MaxRecvMsgSize(otlpBodyMax))
		collectormetrics.RegisterMetricsServiceServer(sink.grpc, grpcHandler{sink: sink})
		go func() { _ = sink.grpc.Serve(listener) }()
	}
	if cfg.HTTPPort > 0 {
		listener, err := net.Listen("tcp", net.JoinHostPort(ip.String(), strconv.Itoa(cfg.HTTPPort)))
		if err != nil {
			sink.Stop()
			return nil, err
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/v1/metrics", sink.serveHTTP)
		sink.http = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second}
		go func() { _ = sink.http.Serve(listener) }()
	}
	return sink, nil
}

// Stop shuts the sink down.
func (s *OTLPSink) Stop() {
	if s.grpc != nil {
		s.grpc.Stop()
	}
	if s.http != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.http.Shutdown(ctx)
	}
}

func (s *OTLPSink) accept(request *collectormetrics.ExportMetricsServiceRequest) *collectormetrics.ExportMetricsServiceResponse {
	samples, dropped := ConvertOTLP(request)
	if len(samples) > 0 {
		s.cfg.Submit(samples)
	}
	response := &collectormetrics.ExportMetricsServiceResponse{}
	if dropped > 0 {
		response.PartialSuccess = &collectormetrics.ExportMetricsPartialSuccess{RejectedDataPoints: int64(dropped), ErrorMessage: "data points outside the kombify Guard telemetry contract were not kept"}
	}
	return response
}

type grpcHandler struct {
	collectormetrics.UnimplementedMetricsServiceServer
	sink *OTLPSink
}

func (h grpcHandler) Export(_ context.Context, request *collectormetrics.ExportMetricsServiceRequest) (*collectormetrics.ExportMetricsServiceResponse, error) {
	return h.sink.accept(request), nil
}

func (s *OTLPSink) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, otlpBodyMax))
	if err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	request := &collectormetrics.ExportMetricsServiceRequest{}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	switch contentType {
	case "application/x-protobuf":
		err = proto.Unmarshal(body, request)
	case "application/json":
		err = protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(body, request)
	default:
		http.Error(w, "unsupported content type", http.StatusUnsupportedMediaType)
		return
	}
	if err != nil {
		http.Error(w, "invalid OTLP metrics request", http.StatusBadRequest)
		return
	}
	response := s.accept(request)
	if contentType == "application/json" {
		raw, _ := protojson.Marshal(response)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
		return
	}
	raw, _ := proto.Marshal(response)
	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write(raw)
}

func attrs(values []*commonpb.KeyValue) map[string]string {
	out := make(map[string]string, len(values))
	for _, kv := range values {
		if kv != nil && kv.Value != nil {
			if s, ok := kv.Value.Value.(*commonpb.AnyValue_StringValue); ok {
				out[kv.Key] = s.StringValue
			}
		}
	}
	return out
}

func numberValue(point *metricspb.NumberDataPoint) float64 {
	switch v := point.Value.(type) {
	case *metricspb.NumberDataPoint_AsDouble:
		return v.AsDouble
	case *metricspb.NumberDataPoint_AsInt:
		return float64(v.AsInt)
	}
	return 0
}

// ConvertOTLP maps the host and container metrics the StackKits collector's
// hostmetrics and docker_stats receivers produce onto the contract. Every
// other metric (network, disk I/O, processes, application metrics) is dropped
// and counted. Nothing is forwarded as it came: a sample is built by name from
// the mapping, so an attribute that is not part of the contract cannot travel.
func ConvertOTLP(request *collectormetrics.ExportMetricsServiceRequest) (samples []Sample, dropped int) {
	idle := map[string]float64{}
	for _, resourceMetrics := range request.GetResourceMetrics() {
		resource := attrs(resourceMetrics.GetResource().GetAttributes())
		for _, scope := range resourceMetrics.GetScopeMetrics() {
			for _, metric := range scope.GetMetrics() {
				var points []*metricspb.NumberDataPoint
				switch data := metric.Data.(type) {
				case *metricspb.Metric_Gauge:
					points = data.Gauge.GetDataPoints()
				case *metricspb.Metric_Sum:
					points = data.Sum.GetDataPoints()
				default:
					dropped++
					continue
				}
				for _, point := range points {
					attributes := attrs(point.GetAttributes())
					value := numberValue(point)
					if metric.Name == "system.cpu.utilization" {
						if attributes["state"] == "idle" {
							idle[attributes["cpu"]] = value
						}
						continue
					}
					if sample, ok := mapOTLP(metric.Name, attributes, resource, value); ok {
						samples = append(samples, sample)
					} else {
						dropped++
					}
				}
			}
		}
	}
	if len(idle) > 0 {
		var total float64
		for cpuName, fraction := range idle {
			busy := (1 - fraction) * 100
			total += busy
			if core := strings.TrimPrefix(cpuName, "cpu"); coreValue.MatchString(core) && core != "" {
				samples = append(samples, labelled("node_cpu_usage_percent", busy, OriginOTLP, labelCore, core))
			}
		}
		samples = append(samples, labelled("node_cpu_usage_percent", total/float64(len(idle)), OriginOTLP, labelCore, "total"))
	}
	return samples, dropped
}

// mapOTLP maps one data point. ok is false for anything off the mapping.
func mapOTLP(name string, attributes, resource map[string]string, value float64) (Sample, bool) {
	switch name {
	case "system.cpu.load_average.1m":
		return labelled("node_load_1", value, OriginOTLP), true
	case "system.cpu.load_average.5m":
		return labelled("node_load_5", value, OriginOTLP), true
	case "system.cpu.load_average.15m":
		return labelled("node_load_15", value, OriginOTLP), true
	case "system.uptime":
		return labelled("node_uptime_seconds", value, OriginOTLP), true
	case "system.memory.utilization":
		if attributes["state"] == "used" {
			return labelled("node_memory_usage_percent", value*100, OriginOTLP), true
		}
	case "system.memory.usage":
		switch attributes["state"] {
		case "used":
			return labelled("node_memory_used_bytes", value, OriginOTLP), true
		case "cached":
			return labelled("node_memory_cached_bytes", value, OriginOTLP), true
		case "buffered":
			return labelled("node_memory_buffers_bytes", value, OriginOTLP), true
		}
	case "system.paging.utilization":
		if attributes["state"] == "used" {
			return labelled("node_swap_usage_percent", value*100, OriginOTLP), true
		}
	case "system.paging.usage":
		if attributes["state"] == "used" {
			return labelled("node_swap_used_bytes", value, OriginOTLP), true
		}
	case "system.filesystem.utilization":
		if mount, ok := hostMount(attributes["mountpoint"]); ok {
			return labelled("node_disk_usage_percent", value*100, OriginOTLP, labelMount, mount), true
		}
	case "system.filesystem.usage":
		if mount, ok := hostMount(attributes["mountpoint"]); ok {
			switch attributes["state"] {
			case "used":
				return labelled("node_disk_used_bytes", value, OriginOTLP, labelMount, mount), true
			case "free":
				return labelled("node_disk_free_bytes", value, OriginOTLP, labelMount, mount), true
			}
		}
	case "container.cpu.utilization":
		return containerSample("container_cpu_usage_percent", resource, value)
	case "container.memory.percent":
		return containerSample("container_memory_usage_percent", resource, value)
	case "container.memory.usage.total":
		return containerSample("container_memory_usage_bytes", resource, value)
	case "container.memory.usage.limit":
		return containerSample("container_memory_limit_bytes", resource, value)
	}
	return Sample{}, false
}

// hostMount strips the collector's host filesystem prefix.
func hostMount(mountpoint string) (string, bool) {
	mountpoint = strings.TrimPrefix(mountpoint, "/hostfs")
	if mountpoint == "" {
		mountpoint = "/"
	}
	return mountpoint, validLabel(labelMount, mountpoint)
}

func containerSample(name string, resource map[string]string, value float64) (Sample, bool) {
	container := strings.TrimPrefix(resource["container.name"], "/")
	if !validLabel(labelContainerName, container) {
		return Sample{}, false
	}
	return labelled(name, value, OriginOTLP, labelContainerName, container), true
}
