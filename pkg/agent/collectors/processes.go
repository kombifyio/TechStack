package collectors

import (
	"context"
	"regexp"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// ProcessPattern defines a process to watch by name or command-line regex.
type ProcessPattern struct {
	Name    string         // Human-readable identifier for this pattern
	Pattern *regexp.Regexp // Regex matched against process name or cmdline
}

// ProcessCollector monitors specific processes by configurable patterns.
type ProcessCollector struct {
	interval time.Duration
	patterns []ProcessPattern
}

func NewProcessCollector(interval time.Duration, patterns []ProcessPattern) *ProcessCollector {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &ProcessCollector{interval: interval, patterns: patterns}
}

func (c *ProcessCollector) Name() string            { return "processes" }
func (c *ProcessCollector) Interval() time.Duration { return c.interval }

func (c *ProcessCollector) Collect(ctx context.Context) ([]Sample, error) {
	now := time.Now()
	var samples []Sample

	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, err
	}

	// If no patterns configured, emit only summary metrics
	if len(c.patterns) == 0 {
		samples = append(samples, Sample{
			Name:      "node_processes_total",
			Value:     float64(len(procs)),
			Type:      Gauge,
			Timestamp: now,
		})

		var running, sleeping, stopped, zombie int
		for _, p := range procs {
			statuses, err := p.StatusWithContext(ctx)
			if err != nil || len(statuses) == 0 {
				continue
			}
			switch statuses[0] {
			case process.Running:
				running++
			case process.Sleep:
				sleeping++
			case process.Stop:
				stopped++
			case process.Zombie:
				zombie++
			}
		}
		samples = append(samples,
			Sample{Name: "node_processes_running", Value: float64(running), Type: Gauge, Timestamp: now},
			Sample{Name: "node_processes_sleeping", Value: float64(sleeping), Type: Gauge, Timestamp: now},
			Sample{Name: "node_processes_stopped", Value: float64(stopped), Type: Gauge, Timestamp: now},
			Sample{Name: "node_processes_zombie", Value: float64(zombie), Type: Gauge, Timestamp: now},
		)

		return samples, nil
	}

	// Track which patterns have at least one match
	matched := make(map[string]bool, len(c.patterns))

	for _, p := range procs {
		name, _ := p.NameWithContext(ctx)
		cmdline, _ := p.CmdlineWithContext(ctx)
		matchStr := name + " " + cmdline

		for _, pat := range c.patterns {
			if !pat.Pattern.MatchString(matchStr) {
				continue
			}
			matched[pat.Name] = true

			labels := map[string]string{
				"pattern": pat.Name,
				"name":    name,
				"pid":     itoa(int(p.Pid)),
			}

			cpuPct, err := p.CPUPercentWithContext(ctx)
			if err == nil {
				samples = append(samples, Sample{
					Name: "process_cpu_usage_percent", Value: cpuPct,
					Labels: labels, Type: Gauge, Timestamp: now,
				})
			}

			memInfo, err := p.MemoryInfoWithContext(ctx)
			if err == nil && memInfo != nil {
				samples = append(samples,
					Sample{Name: "process_memory_rss_bytes", Value: float64(memInfo.RSS), Labels: labels, Type: Gauge, Timestamp: now},
					Sample{Name: "process_memory_vms_bytes", Value: float64(memInfo.VMS), Labels: labels, Type: Gauge, Timestamp: now},
				)
			}

			numThreads, err := p.NumThreadsWithContext(ctx)
			if err == nil {
				samples = append(samples, Sample{
					Name: "process_threads", Value: float64(numThreads),
					Labels: labels, Type: Gauge, Timestamp: now,
				})
			}

			numFDs, err := p.NumFDsWithContext(ctx)
			if err == nil {
				samples = append(samples, Sample{
					Name: "process_open_fds", Value: float64(numFDs),
					Labels: labels, Type: Gauge, Timestamp: now,
				})
			}

			statuses, err := p.StatusWithContext(ctx)
			if err == nil && len(statuses) > 0 {
				statusVal := float64(0)
				if statuses[0] == process.Running {
					statusVal = 1
				}
				samples = append(samples, Sample{
					Name: "process_running", Value: statusVal,
					Labels: labels, Type: Gauge, Timestamp: now,
				})
			}

			createTime, err := p.CreateTimeWithContext(ctx)
			if err == nil {
				uptime := now.Sub(time.UnixMilli(createTime)).Seconds()
				samples = append(samples, Sample{
					Name: "process_uptime_seconds", Value: uptime,
					Labels: labels, Type: Gauge, Timestamp: now,
				})
			}
		}
	}

	// Emit a presence metric per pattern (1 = at least one process found, 0 = missing)
	for _, pat := range c.patterns {
		val := float64(0)
		if matched[pat.Name] {
			val = 1
		}
		samples = append(samples, Sample{
			Name:      "process_pattern_up",
			Value:     val,
			Labels:    map[string]string{"pattern": pat.Name},
			Type:      Gauge,
			Timestamp: now,
		})
	}

	return samples, nil
}

func itoa(i int) string {
	// Avoid importing strconv just for this -- simple int-to-string
	if i == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
