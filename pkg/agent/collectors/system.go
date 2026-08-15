package collectors

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

// SystemCollector gathers host-level metrics: CPU, memory, disk, network, load.
type SystemCollector struct {
	interval time.Duration
}

func NewSystemCollector(interval time.Duration) *SystemCollector {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	return &SystemCollector{interval: interval}
}

func (c *SystemCollector) Name() string            { return "system" }
func (c *SystemCollector) Interval() time.Duration { return c.interval }

func (c *SystemCollector) Collect(ctx context.Context) ([]Sample, error) {
	now := time.Now()
	var samples []Sample

	// CPU per-core usage
	perCPU, err := cpu.PercentWithContext(ctx, 0, true)
	if err == nil {
		var total float64
		for i, pct := range perCPU {
			samples = append(samples, Sample{
				Name:      "node_cpu_usage_percent",
				Value:     pct,
				Labels:    map[string]string{"core": strconv.Itoa(i)},
				Type:      Gauge,
				Timestamp: now,
			})
			total += pct
		}
		if len(perCPU) > 0 {
			samples = append(samples, Sample{
				Name:      "node_cpu_usage_percent",
				Value:     total / float64(len(perCPU)),
				Labels:    map[string]string{"core": "total"},
				Type:      Gauge,
				Timestamp: now,
			})
		}
	}

	// CPU count
	samples = append(samples, Sample{
		Name:      "node_cpu_count",
		Value:     float64(runtime.NumCPU()),
		Labels:    nil,
		Type:      Gauge,
		Timestamp: now,
	})

	// Load average (Linux/macOS only, returns 0 on Windows)
	if avg, err := load.AvgWithContext(ctx); err == nil {
		samples = append(samples,
			Sample{Name: "node_load_1", Value: avg.Load1, Type: Gauge, Timestamp: now},
			Sample{Name: "node_load_5", Value: avg.Load5, Type: Gauge, Timestamp: now},
			Sample{Name: "node_load_15", Value: avg.Load15, Type: Gauge, Timestamp: now},
		)
	}

	// Memory
	if v, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		samples = append(samples,
			Sample{Name: "node_memory_total_bytes", Value: float64(v.Total), Type: Gauge, Timestamp: now},
			Sample{Name: "node_memory_used_bytes", Value: float64(v.Used), Type: Gauge, Timestamp: now},
			Sample{Name: "node_memory_available_bytes", Value: float64(v.Available), Type: Gauge, Timestamp: now},
			Sample{Name: "node_memory_cached_bytes", Value: float64(v.Cached), Type: Gauge, Timestamp: now},
			Sample{Name: "node_memory_buffers_bytes", Value: float64(v.Buffers), Type: Gauge, Timestamp: now},
			Sample{Name: "node_memory_usage_percent", Value: v.UsedPercent, Type: Gauge, Timestamp: now},
		)
	}

	// Swap
	if sw, err := mem.SwapMemoryWithContext(ctx); err == nil {
		samples = append(samples,
			Sample{Name: "node_swap_total_bytes", Value: float64(sw.Total), Type: Gauge, Timestamp: now},
			Sample{Name: "node_swap_used_bytes", Value: float64(sw.Used), Type: Gauge, Timestamp: now},
			Sample{Name: "node_swap_usage_percent", Value: sw.UsedPercent, Type: Gauge, Timestamp: now},
		)
	}

	// Disk usage per mount
	partitions, err := disk.PartitionsWithContext(ctx, false)
	if err == nil {
		for _, p := range partitions {
			usage, err := disk.UsageWithContext(ctx, p.Mountpoint)
			if err != nil {
				continue
			}
			labels := map[string]string{
				"mount":  p.Mountpoint,
				"device": p.Device,
				"fstype": p.Fstype,
			}
			samples = append(samples,
				Sample{Name: "node_disk_total_bytes", Value: float64(usage.Total), Labels: labels, Type: Gauge, Timestamp: now},
				Sample{Name: "node_disk_used_bytes", Value: float64(usage.Used), Labels: labels, Type: Gauge, Timestamp: now},
				Sample{Name: "node_disk_free_bytes", Value: float64(usage.Free), Labels: labels, Type: Gauge, Timestamp: now},
				Sample{Name: "node_disk_usage_percent", Value: usage.UsedPercent, Labels: labels, Type: Gauge, Timestamp: now},
			)
		}
	}

	// Disk I/O counters
	ioCounters, err := disk.IOCountersWithContext(ctx)
	if err == nil {
		for device, io := range ioCounters {
			labels := map[string]string{"device": device}
			samples = append(samples,
				Sample{Name: "node_disk_read_bytes_total", Value: float64(io.ReadBytes), Labels: labels, Type: Counter, Timestamp: now},
				Sample{Name: "node_disk_write_bytes_total", Value: float64(io.WriteBytes), Labels: labels, Type: Counter, Timestamp: now},
				Sample{Name: "node_disk_reads_total", Value: float64(io.ReadCount), Labels: labels, Type: Counter, Timestamp: now},
				Sample{Name: "node_disk_writes_total", Value: float64(io.WriteCount), Labels: labels, Type: Counter, Timestamp: now},
			)
		}
	}

	// Network I/O per interface
	netIO, err := net.IOCountersWithContext(ctx, true)
	if err == nil {
		for _, iface := range netIO {
			labels := map[string]string{"interface": iface.Name}
			samples = append(samples,
				Sample{Name: "node_network_receive_bytes_total", Value: float64(iface.BytesRecv), Labels: labels, Type: Counter, Timestamp: now},
				Sample{Name: "node_network_transmit_bytes_total", Value: float64(iface.BytesSent), Labels: labels, Type: Counter, Timestamp: now},
				Sample{Name: "node_network_receive_packets_total", Value: float64(iface.PacketsRecv), Labels: labels, Type: Counter, Timestamp: now},
				Sample{Name: "node_network_transmit_packets_total", Value: float64(iface.PacketsSent), Labels: labels, Type: Counter, Timestamp: now},
				Sample{Name: "node_network_receive_errors_total", Value: float64(iface.Errin), Labels: labels, Type: Counter, Timestamp: now},
				Sample{Name: "node_network_transmit_errors_total", Value: float64(iface.Errout), Labels: labels, Type: Counter, Timestamp: now},
				Sample{Name: "node_network_receive_drops_total", Value: float64(iface.Dropin), Labels: labels, Type: Counter, Timestamp: now},
				Sample{Name: "node_network_transmit_drops_total", Value: float64(iface.Dropout), Labels: labels, Type: Counter, Timestamp: now},
			)
		}
	}

	// Host info (uptime, OS)
	if info, err := host.InfoWithContext(ctx); err == nil {
		samples = append(samples,
			Sample{Name: "node_uptime_seconds", Value: float64(info.Uptime), Type: Gauge, Timestamp: now},
			Sample{
				Name:      "node_info",
				Value:     1,
				Labels:    map[string]string{"os": info.OS, "platform": info.Platform, "kernel": info.KernelVersion, "hostname": info.Hostname, "arch": fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)},
				Type:      Gauge,
				Timestamp: now,
			},
		)
	}

	return samples, nil
}
