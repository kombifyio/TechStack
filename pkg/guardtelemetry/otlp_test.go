package guardtelemetry

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	collectormetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
)

func str(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: key, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}}}
}

func gauge(name string, value float64, attributes ...*commonpb.KeyValue) *metricspb.Metric {
	return &metricspb.Metric{Name: name, Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{
		{Attributes: attributes, Value: &metricspb.NumberDataPoint_AsDouble{AsDouble: value}},
	}}}}
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port
}

// The Guard's OTLP sink listens on loopback only and keeps only what the
// contract lists: the collector's network, disk I/O, process and application
// metrics are dropped and counted, and a container is named only by a valid
// container name.
func TestOTLPSinkListensOnLoopbackAndKeepsOnlyTheContract(t *testing.T) {
	for _, bind := range []string{"0.0.0.0", "::", "192.168.1.10", "example.com"} {
		if _, err := StartOTLP(OTLPConfig{Bind: bind, HTTPPort: 1, Submit: func([]Sample) {}}); !errors.Is(err, ErrNotLoopback) {
			t.Fatalf("a sink bound to %q: %v", bind, err)
		}
	}
	var mu sync.Mutex
	var got []Sample
	port := freePort(t)
	sink, err := StartOTLP(OTLPConfig{Bind: "127.0.0.1", HTTPPort: port, Submit: func(samples []Sample) {
		mu.Lock()
		got = append(got, samples...)
		mu.Unlock()
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Stop()

	request := &collectormetrics.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{
		{
			Resource: &resourcepb.Resource{},
			ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{
				gauge("system.cpu.load_average.1m", 0.5),
				gauge("system.memory.utilization", 0.4, str("state", "used")),
				gauge("system.filesystem.utilization", 0.25, str("mountpoint", "/hostfs/var"), str("device", "sda1")),
				gauge("system.network.io", 99, str("device", "eth0")),
				gauge("system.disk.io", 99),
				gauge("process.cpu.time", 1),
				gauge("http.server.duration", 3),
				gauge("system.cpu.utilization", 0.75, str("cpu", "cpu0"), str("state", "idle")),
				gauge("system.cpu.utilization", 0.25, str("cpu", "cpu0"), str("state", "user")),
			}}},
		},
		{
			Resource:     &resourcepb.Resource{Attributes: []*commonpb.KeyValue{str("container.name", "traefik")}},
			ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{gauge("container.cpu.utilization", 7), gauge("container.network.io.usage.rx_bytes", 1)}}},
		},
		{
			Resource:     &resourcepb.Resource{Attributes: []*commonpb.KeyValue{str("container.name", "bad name; drop")}},
			ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{gauge("container.cpu.utilization", 7)}}},
		},
	}}
	body, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var response *http.Response
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, err = http.Post("http://127.0.0.1:"+strconv.Itoa(port)+"/v1/metrics", "application/x-protobuf", bytes.NewReader(body))
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("export: %v %v", response, err)
	}
	_ = response.Body.Close()

	mu.Lock()
	defer mu.Unlock()
	names := map[string]int{}
	for _, sample := range got {
		if !Admit(sample) || sample.Labels[labelOrigin] != OriginOTLP {
			t.Fatalf("the sink handed over %+v", sample)
		}
		names[sample.Name]++
	}
	for _, want := range []string{"node_load_1", "node_memory_usage_percent", "node_disk_usage_percent", "node_cpu_usage_percent", "container_cpu_usage_percent"} {
		if names[want] == 0 {
			t.Errorf("%s was not kept: %v", want, names)
		}
	}
	if len(got) != 6 { // load, memory, disk, cpu0, cpu total, traefik
		t.Fatalf("kept %d samples: %+v", len(got), got)
	}
	for _, sample := range got {
		if sample.Name == "node_disk_usage_percent" && sample.Labels[labelMount] != "/var" {
			t.Fatalf("the collector's host filesystem prefix leaked: %v", sample.Labels)
		}
	}
}
