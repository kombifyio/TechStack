package guardtelemetry

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// The overhead of one collection cycle, measured: go test -bench=Cycle -benchmem.
// At the default 30 second cadence, the CPU time per cycle divided by 30 s is the
// Guard's average CPU share for telemetry.
func BenchmarkCycleHostSample(b *testing.B) {
	sampler := &HostSampler{}
	ctx := context.Background()
	_, _ = sampler.Sample(ctx)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := sampler.Sample(ctx); err != nil {
			b.Skip(err)
		}
	}
}

func BenchmarkCycleFilterSpoolAndEncode(b *testing.B) {
	samples, err := (&HostSampler{}).Sample(context.Background())
	if err != nil {
		b.Skip(err)
	}
	for i := 0; i < 10; i++ { // a typical container fleet
		name := "container-" + string(rune('a'+i))
		for _, metric := range []string{"container_running", "container_cpu_usage_percent", "container_memory_usage_bytes", "container_memory_limit_bytes", "container_memory_usage_percent"} {
			samples = append(samples, labelled(metric, 1, OriginGuard, labelContainerName, name))
		}
	}
	spool, err := OpenSpool(SpoolConfig{Dir: b.TempDir()})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(len(samples)), "samples/cycle")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		kept, _ := Filter(samples)
		record, err := spool.Append(time.Now(), kept)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := json.Marshal(Batch{SchemaVersion: BatchSchema, SourceEpoch: spool.Epoch(), Records: []Record{record}}); err != nil {
			b.Fatal(err)
		}
		if err := spool.Ack(record.Sequence); err != nil {
			b.Fatal(err)
		}
	}
}
