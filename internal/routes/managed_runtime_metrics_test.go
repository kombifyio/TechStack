package routes

import (
	"strings"
	"testing"
)

func managedRuntimeMetricsHasNote(notes []string, substr string) bool {
	for _, note := range notes {
		if strings.Contains(note, substr) {
			return true
		}
	}
	return false
}

// TestOverlayManagedRuntimeMetricsSurfacesMeasuredHealth proves that once the
// runtime worker reports node_*{agent_id} samples, a managed projection stops
// showing the "unknown"/"provisioned" placeholder and reflects the real values —
// closing the "fake health" gap where a managed node read healthy/unknown.
func TestOverlayManagedRuntimeMetricsSurfacesMeasuredHealth(t *testing.T) {
	server := stackServerFromManagedRuntime(managedRuntimeInventoryItem{
		ID:            "managed-runtime-lease-abc123",
		Status:        managedRuntimeStatusProvisioned,
		Approved:      true,
		CPUPercent:    metricUnknown("%"),
		MemoryPercent: metricUnknown("%"),
		DiskPercent:   metricUnknown("%"),
		UptimeSeconds: metricUnknown("s"),
	})

	// Precondition: no telemetry -> honest unknown behind the "provisioned" note.
	if server.Health.CPUPercent.Status != "unknown" {
		t.Fatalf("precondition: expected unknown cpu, got %q", server.Health.CPUPercent.Status)
	}
	if !managedRuntimeMetricsHasNote(server.Health.Notes, "no live telemetry") {
		t.Fatalf("precondition: expected no-telemetry note, got %v", server.Health.Notes)
	}

	overlayManagedRuntimeMetrics(&server, func(query string) (float64, bool) {
		switch {
		case strings.Contains(query, "node_cpu_usage_percent"):
			return 12.5, true
		case strings.Contains(query, "node_memory_usage_percent"):
			return 41, true
		case strings.Contains(query, "node_disk_usage_percent"):
			return 63, true
		case strings.Contains(query, "node_uptime_seconds"):
			return 7200, true
		}
		return 0, false
	})

	if server.Health.CPUPercent.Status != "ok" || server.Health.CPUPercent.Value == nil || *server.Health.CPUPercent.Value != 12.5 {
		t.Fatalf("expected measured cpu 12.5, got %+v", server.Health.CPUPercent)
	}
	if server.Health.DiskPercent.Value == nil || *server.Health.DiskPercent.Value != 63 {
		t.Fatalf("expected measured disk 63, got %+v", server.Health.DiskPercent)
	}
	if server.Health.Source != "promql" {
		t.Fatalf("expected source promql, got %q", server.Health.Source)
	}
	if server.Status != "healthy" || server.Health.State != "healthy" {
		t.Fatalf("expected measured telemetry to promote status healthy, got status=%q state=%q", server.Status, server.Health.State)
	}
	if managedRuntimeMetricsHasNote(server.Health.Notes, "no live telemetry") {
		t.Fatalf("expected no-telemetry note dropped after measurement, got %v", server.Health.Notes)
	}
}

// TestOverlayManagedRuntimeMetricsWithoutSamplesKeepsHonestProjection proves the
// overlay never fabricates health: with no samples the honest lease-state
// projection ("provisioned" + unknown metrics + note) is left untouched.
func TestOverlayManagedRuntimeMetricsWithoutSamplesKeepsHonestProjection(t *testing.T) {
	server := stackServerFromManagedRuntime(managedRuntimeInventoryItem{
		ID:            "managed-runtime-lease-def456",
		Status:        managedRuntimeStatusProvisioned,
		Approved:      true,
		CPUPercent:    metricUnknown("%"),
		MemoryPercent: metricUnknown("%"),
		DiskPercent:   metricUnknown("%"),
		UptimeSeconds: metricUnknown("s"),
	})

	overlayManagedRuntimeMetrics(&server, func(string) (float64, bool) { return 0, false })

	if server.Status != managedRuntimeStatusProvisioned {
		t.Fatalf("expected status to stay provisioned without telemetry, got %q", server.Status)
	}
	if server.Health.CPUPercent.Status != "unknown" {
		t.Fatalf("expected cpu to stay unknown, got %q", server.Health.CPUPercent.Status)
	}
	if !managedRuntimeMetricsHasNote(server.Health.Notes, "no live telemetry") {
		t.Fatalf("expected no-telemetry note retained, got %v", server.Health.Notes)
	}
}
