package guardtelemetry

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
)

// Sampler produces the contract's samples of one collection cycle.
type Sampler interface {
	Sample(ctx context.Context) ([]Sample, error)
}

func labelled(name string, value float64, origin string, pairs ...string) Sample {
	labels := map[string]string{labelOrigin: origin}
	for i := 0; i+1 < len(pairs); i += 2 {
		labels[pairs[i]] = pairs[i+1]
	}
	return Sample{Name: name, Value: value, Labels: labels}
}

// HostSampler reads the host resources of the contract: CPU, memory, swap,
// load, disk per mount and uptime. It reads only those, so a listener, a
// network interface or a process never enters a sample to be filtered out later.
type HostSampler struct{ primed bool }

// Sample reads one cycle. A reading that fails is left out; the sampler fails
// only when it read nothing at all.
func (h *HostSampler) Sample(ctx context.Context) ([]Sample, error) {
	var out []Sample
	if !h.primed {
		// The first reading has nothing to compare with.
		_, _ = cpu.PercentWithContext(ctx, 0, true)
		h.primed = true
	}
	if perCPU, err := cpu.PercentWithContext(ctx, 0, true); err == nil && len(perCPU) > 0 {
		var total float64
		for i, pct := range perCPU {
			out = append(out, labelled("node_cpu_usage_percent", pct, OriginGuard, labelCore, strconv.Itoa(i)))
			total += pct
		}
		out = append(out, labelled("node_cpu_usage_percent", total/float64(len(perCPU)), OriginGuard, labelCore, "total"))
	}
	if avg, err := load.AvgWithContext(ctx); err == nil {
		out = append(out,
			labelled("node_load_1", avg.Load1, OriginGuard), labelled("node_load_5", avg.Load5, OriginGuard), labelled("node_load_15", avg.Load15, OriginGuard))
	}
	if v, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		out = append(out,
			labelled("node_memory_total_bytes", float64(v.Total), OriginGuard), labelled("node_memory_used_bytes", float64(v.Used), OriginGuard),
			labelled("node_memory_available_bytes", float64(v.Available), OriginGuard), labelled("node_memory_cached_bytes", float64(v.Cached), OriginGuard),
			labelled("node_memory_buffers_bytes", float64(v.Buffers), OriginGuard), labelled("node_memory_usage_percent", v.UsedPercent, OriginGuard))
	}
	if sw, err := mem.SwapMemoryWithContext(ctx); err == nil {
		out = append(out,
			labelled("node_swap_total_bytes", float64(sw.Total), OriginGuard), labelled("node_swap_used_bytes", float64(sw.Used), OriginGuard),
			labelled("node_swap_usage_percent", sw.UsedPercent, OriginGuard))
	}
	if partitions, err := disk.PartitionsWithContext(ctx, false); err == nil {
		seen := map[string]bool{}
		for _, partition := range partitions {
			if seen[partition.Mountpoint] || !validLabel(labelMount, partition.Mountpoint) {
				continue
			}
			seen[partition.Mountpoint] = true
			usage, err := disk.UsageWithContext(ctx, partition.Mountpoint)
			if err != nil {
				continue
			}
			out = append(out,
				labelled("node_disk_total_bytes", float64(usage.Total), OriginGuard, labelMount, partition.Mountpoint),
				labelled("node_disk_used_bytes", float64(usage.Used), OriginGuard, labelMount, partition.Mountpoint),
				labelled("node_disk_free_bytes", float64(usage.Free), OriginGuard, labelMount, partition.Mountpoint),
				labelled("node_disk_usage_percent", usage.UsedPercent, OriginGuard, labelMount, partition.Mountpoint))
		}
	}
	if uptime, err := host.UptimeWithContext(ctx); err == nil {
		out = append(out, labelled("node_uptime_seconds", float64(uptime), OriginGuard))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the host could not be read")
	}
	return out, nil
}

// DockerProbe runs one fixed read-only container probe. In the split layout it
// is the executor's probe; the daemon never holds the docker socket.
type DockerProbe func(ctx context.Context, kind string) ([]byte, error)

// Docker probe kinds, the executor's closed set.
const (
	ProbeContainers = "containers"
	ProbeStats      = "stats"
)

// ExecDockerProbe runs the same fixed probes directly, for a Guard that still
// runs as root.
func ExecDockerProbe(ctx context.Context, kind string) ([]byte, error) {
	var args []string
	switch kind {
	case ProbeContainers:
		args = []string{"ps", "--all", "--no-trunc", "--format", "{{json .}}"}
	case ProbeStats:
		args = []string{"stats", "--no-stream", "--no-trunc", "--format", "{{json .}}"}
	default:
		return nil, fmt.Errorf("container probe %q is not on the allowlist", kind)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "docker", args...).Output() // #nosec G204 -- the argv is a constant selected by a closed kind.
}

// ContainerSampler reads container state and resource usage through a docker
// probe. A host without docker yields no samples and no error.
type ContainerSampler struct{ Probe DockerProbe }

// Sample reads one cycle.
func (c ContainerSampler) Sample(ctx context.Context) ([]Sample, error) {
	if c.Probe == nil {
		return nil, nil
	}
	listing, err := c.Probe(ctx, ProbeContainers)
	if err != nil {
		return nil, nil // docker absent or not running: there is nothing to report
	}
	running := map[string]bool{}
	var out []Sample
	scanner := bufio.NewScanner(bytes.NewReader(listing))
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		var row struct{ Names, State string }
		if json.Unmarshal(scanner.Bytes(), &row) != nil {
			continue
		}
		name := firstName(row.Names)
		if !validLabel(labelContainerName, name) {
			continue
		}
		up := 0.0
		if strings.EqualFold(row.State, "running") {
			up = 1
			running[name] = true
		}
		out = append(out, labelled("container_running", up, OriginGuard, labelContainerName, name))
	}
	if len(running) == 0 {
		return out, nil
	}
	stats, err := c.Probe(ctx, ProbeStats)
	if err != nil {
		return out, nil
	}
	scanner = bufio.NewScanner(bytes.NewReader(stats))
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		var row struct{ Name, CPUPerc, MemPerc, MemUsage string }
		if json.Unmarshal(scanner.Bytes(), &row) != nil || !running[row.Name] {
			continue
		}
		if cpuPct, ok := percent(row.CPUPerc); ok {
			out = append(out, labelled("container_cpu_usage_percent", cpuPct, OriginGuard, labelContainerName, row.Name))
		}
		if memPct, ok := percent(row.MemPerc); ok {
			out = append(out, labelled("container_memory_usage_percent", memPct, OriginGuard, labelContainerName, row.Name))
		}
		if used, limit, ok := memoryUsage(row.MemUsage); ok {
			out = append(out, labelled("container_memory_usage_bytes", used, OriginGuard, labelContainerName, row.Name))
			if limit > 0 {
				out = append(out, labelled("container_memory_limit_bytes", limit, OriginGuard, labelContainerName, row.Name))
			}
		}
	}
	return out, nil
}

func firstName(names string) string {
	name, _, _ := strings.Cut(names, ",")
	return strings.TrimPrefix(strings.TrimSpace(name), "/")
}

func percent(text string) (float64, bool) {
	value, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), "%")), 64)
	return value, err == nil
}

var byteUnits = map[string]float64{
	"b": 1, "kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12,
	"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40,
}

// memoryUsage parses docker stats' "13.74MiB / 7.667GiB".
func memoryUsage(text string) (used, limit float64, ok bool) {
	usedText, limitText, found := strings.Cut(text, "/")
	if !found {
		return 0, 0, false
	}
	used, usedOK := bytesOf(usedText)
	limit, limitOK := bytesOf(limitText)
	return used, limit, usedOK && limitOK
}

func bytesOf(text string) (float64, bool) {
	text = strings.TrimSpace(text)
	i := 0
	for i < len(text) && (text[i] == '.' || (text[i] >= '0' && text[i] <= '9')) {
		i++
	}
	number, err := strconv.ParseFloat(text[:i], 64)
	unit, known := byteUnits[strings.ToLower(strings.TrimSpace(text[i:]))]
	if err != nil || !known {
		return 0, false
	}
	return number * unit, true
}
