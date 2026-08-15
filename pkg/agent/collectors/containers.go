package collectors

import (
	"context"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// ContainerCollector gathers Docker container metrics via the Docker Engine API.
type ContainerCollector struct {
	interval time.Duration
	cli      *client.Client
}

func NewContainerCollector(interval time.Duration) (*ContainerCollector, error) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}
	return &ContainerCollector{interval: interval, cli: cli}, nil
}

func (c *ContainerCollector) Name() string            { return "containers" }
func (c *ContainerCollector) Interval() time.Duration { return c.interval }

func (c *ContainerCollector) Collect(ctx context.Context) ([]Sample, error) {
	now := time.Now()
	var samples []Sample

	containers, err := c.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}

	// Container count by state
	stateCounts := map[string]int{}
	for _, ctr := range containers {
		stateCounts[ctr.State]++
	}
	for state, count := range stateCounts {
		samples = append(samples, Sample{
			Name:      "container_state_count",
			Value:     float64(count),
			Labels:    map[string]string{"state": state},
			Type:      Gauge,
			Timestamp: now,
		})
	}

	// Per-container info + stats
	for _, ctr := range containers {
		name := ""
		if len(ctr.Names) > 0 {
			name = ctr.Names[0]
			if len(name) > 0 && name[0] == '/' {
				name = name[1:]
			}
		}
		labels := map[string]string{
			"container_id":   ctr.ID[:12],
			"container_name": name,
			"image":          ctr.Image,
			"state":          ctr.State,
		}

		// Info metric (always emitted, value=1 for running, 0 otherwise)
		running := float64(0)
		if ctr.State == "running" {
			running = 1
		}
		samples = append(samples, Sample{
			Name:      "container_running",
			Value:     running,
			Labels:    labels,
			Type:      Gauge,
			Timestamp: now,
		})

		// Only fetch stats for running containers
		if ctr.State != "running" {
			continue
		}

		stats, err := c.cli.ContainerStatsOneShot(ctx, ctr.ID)
		if err != nil {
			continue
		}

		var st container.StatsResponse
		if err := decodeContainerStats(stats.Body, &st); err != nil {
			stats.Body.Close()
			continue
		}
		stats.Body.Close()

		statsLabels := map[string]string{
			"container_id":   ctr.ID[:12],
			"container_name": name,
			"image":          ctr.Image,
		}

		// CPU usage percentage
		cpuPct := calculateCPUPercent(&st)
		samples = append(samples, Sample{
			Name:      "container_cpu_usage_percent",
			Value:     cpuPct,
			Labels:    statsLabels,
			Type:      Gauge,
			Timestamp: now,
		})

		// Memory
		memUsage := float64(st.MemoryStats.Usage - st.MemoryStats.Stats["cache"])
		memLimit := float64(st.MemoryStats.Limit)
		samples = append(samples,
			Sample{Name: "container_memory_usage_bytes", Value: memUsage, Labels: statsLabels, Type: Gauge, Timestamp: now},
			Sample{Name: "container_memory_limit_bytes", Value: memLimit, Labels: statsLabels, Type: Gauge, Timestamp: now},
		)
		if memLimit > 0 {
			samples = append(samples, Sample{
				Name:      "container_memory_usage_percent",
				Value:     (memUsage / memLimit) * 100,
				Labels:    statsLabels,
				Type:      Gauge,
				Timestamp: now,
			})
		}

		// Network I/O (aggregate across all interfaces)
		var netRx, netTx uint64
		for _, netStat := range st.Networks {
			netRx += netStat.RxBytes
			netTx += netStat.TxBytes
		}
		samples = append(samples,
			Sample{Name: "container_network_receive_bytes_total", Value: float64(netRx), Labels: statsLabels, Type: Counter, Timestamp: now},
			Sample{Name: "container_network_transmit_bytes_total", Value: float64(netTx), Labels: statsLabels, Type: Counter, Timestamp: now},
		)

		// Block I/O
		var blkRead, blkWrite uint64
		for _, bio := range st.BlkioStats.IoServiceBytesRecursive {
			switch bio.Op {
			case "read", "Read":
				blkRead += bio.Value
			case "write", "Write":
				blkWrite += bio.Value
			}
		}
		samples = append(samples,
			Sample{Name: "container_block_read_bytes_total", Value: float64(blkRead), Labels: statsLabels, Type: Counter, Timestamp: now},
			Sample{Name: "container_block_write_bytes_total", Value: float64(blkWrite), Labels: statsLabels, Type: Counter, Timestamp: now},
		)

		// PIDs
		samples = append(samples, Sample{
			Name:      "container_pids_current",
			Value:     float64(st.PidsStats.Current),
			Labels:    statsLabels,
			Type:      Gauge,
			Timestamp: now,
		})
	}

	return samples, nil
}

// Close releases the Docker client resources.
func (c *ContainerCollector) Close() error {
	return c.cli.Close()
}

func calculateCPUPercent(st *container.StatsResponse) float64 {
	cpuDelta := float64(st.CPUStats.CPUUsage.TotalUsage - st.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(st.CPUStats.SystemUsage - st.PreCPUStats.SystemUsage)
	if systemDelta <= 0 || cpuDelta < 0 {
		return 0
	}
	onlineCPUs := float64(st.CPUStats.OnlineCPUs)
	if onlineCPUs == 0 {
		onlineCPUs = float64(len(st.CPUStats.CPUUsage.PercpuUsage))
	}
	if onlineCPUs == 0 {
		onlineCPUs = 1
	}
	return (cpuDelta / systemDelta) * onlineCPUs * 100.0
}
