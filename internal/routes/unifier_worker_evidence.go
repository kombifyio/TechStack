package routes

import (
	"context"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/agent/httpguard"
	"github.com/kombifyio/techstack/pkg/availability"
	"github.com/kombifyio/techstack/pkg/controlplane"
	kscore "github.com/kombifyio/techstack/pkg/core"
)

// Provenance of a worker's availability class.
const (
	workerAvailabilitySourceTransitions = "server-transitions"
	workerAvailabilitySourceConfirmed   = "user-confirmed"
	workerAvailabilitySourceNone        = "none"
)

// unifierUsageFreshness is how old the Guard's last host report may be before
// its usage no longer counts as an observation: a reading from a node that has
// been silent for longer is history, not the node's load.
const unifierUsageFreshness = wizardRecommendationInventoryTTL

// unifierWorkerFromRecordAt maps one registry record onto the Unifier's worker,
// reading usage freshness against now. Everything it states is something the
// record carries; what the Guard did not report stays absent (an empty vendor,
// level 0, a nil fact) because the consumers read those as "unknown" and never
// as a shortfall.
func unifierWorkerFromRecordAt(record controlplane.Worker, now time.Time) kscore.Worker {
	tags := unifierWorkerTags(record.Tags)
	if ip := strings.TrimSpace(record.IP); ip != "" {
		tags["ip"] = ip
	}
	worker := kscore.Worker{
		ID:       record.ID,
		Name:     record.Hostname,
		Type:     record.Type,
		Provider: record.Provider,
		Status:   record.Status,
		StackID:  strings.TrimSpace(record.StackID),
		Capabilities: kscore.WorkerCapabilities{
			CPU:            record.CPUCores,
			RAM:            record.RAMMB,
			Disk:           record.DiskGB,
			Arch:           record.Arch,
			OS:             record.OS,
			DockerVersion:  record.DockerVersion,
			HasNVMe:        record.HasNVME,
			HasHWTranscode: record.HasHWTranscode,
		},
		Tags: tags,
	}
	host, _ := record.Resources[workerInventoryHostKey].(map[string]any)
	hardware := hostHardwareFromMap(host)
	if gpu := strings.TrimSpace(record.GPU); gpu != "" {
		worker.Capabilities.GPU = &kscore.GPUInfo{Model: gpu}
	}
	if hardware != nil {
		applyHardwareFacts(&worker.Capabilities, hardware)
	}
	worker.Capabilities.Volumes = storageVolumesFromHost(host)
	if record.LastSeenAt != nil && now.Sub(record.LastSeenAt.UTC()) <= unifierUsageFreshness {
		worker.Usage, worker.UsageObserved = workerUsageFromHost(host, worker.Capabilities)
	}
	if worker.Type == "" {
		worker.Type = "worker"
	}
	if worker.Provider == "" {
		worker.Provider = "local"
	}
	return worker
}

// storageVolumesFromHost reads the Guard's data volumes back out of the stored
// host map. Sanitize drops anything that is not a customer data filesystem.
func storageVolumesFromHost(host map[string]any) []kscore.StorageVolume {
	if host == nil {
		return nil
	}
	raw, ok := host["volumes"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	parsed := make([]httpguard.HostVolume, 0, len(raw))
	for _, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		external, _ := row["external"].(bool)
		parsed = append(parsed, httpguard.HostVolume{
			MountPoint: stringFromAny(row["mount_point"]),
			SizeGB:     intFromAnyMap(row, "size_gb"),
			FreeGB:     intFromAnyMap(row, "free_gb"),
			Filesystem: stringFromAny(row["filesystem"]),
			Label:      stringFromAny(row["label"]),
			External:   external,
		})
	}
	clean := httpguard.SanitizeHostVolumes(parsed)
	if len(clean) == 0 {
		return nil
	}
	out := make([]kscore.StorageVolume, 0, len(clean))
	for _, volume := range clean {
		out = append(out, kscore.StorageVolume{
			MountPoint: volume.MountPoint,
			SizeGB:     volume.SizeGB,
			FreeGB:     volume.FreeGB,
			Filesystem: volume.Filesystem,
			Label:      volume.Label,
			External:   volume.External,
		})
	}
	return out
}

// applyHardwareFacts adds the Guard probe's machine facts to the capabilities.
// A GPU keeps the model the registration named and gains the vendor and the
// driver major the probe read; a driver major of 0 stays 0, which every reader
// treats as unknown.
func applyHardwareFacts(capabilities *kscore.WorkerCapabilities, hardware *httpguard.HostHardware) {
	capabilities.AMD64MicroarchitectureLevel = hardware.AMD64Level
	if gpu := hardware.GPU; gpu != nil {
		info := kscore.GPUInfo{Vendor: gpu.Vendor, DriverMajor: gpu.DriverMajor}
		if capabilities.GPU != nil {
			info.Model = capabilities.GPU.Model
		}
		capabilities.GPU = &info
	}
	if fact := hardware.Media; fact != nil {
		capabilities.Media = &kscore.HardwareFact{Class: fact.Class, Source: fact.Source}
		capabilities.HasHWTranscode = capabilities.HasHWTranscode || fact.Class == "hardware-transcode"
	}
	if fact := hardware.DiskClass; fact != nil {
		capabilities.DiskClass = &kscore.HardwareFact{Class: fact.Class, Source: fact.Source}
		capabilities.HasNVMe = capabilities.HasNVMe || fact.Class == "nvme"
	}
	if fact := hardware.PowerClass; fact != nil {
		capabilities.PowerClass = &kscore.HardwareFact{Class: fact.Class, Source: fact.Source}
	}
}

// workerUsageFromHost turns the Guard's last host report into absolute usage:
// each reported percentage times the node's capacity, rounded up so a reading
// never overstates the free room. A figure the Guard did not report stays 0,
// and a report with no usable figure at all is no observation.
func workerUsageFromHost(host map[string]any, capacity kscore.WorkerCapabilities) (kscore.WorkerUsage, bool) {
	sample, ok := controlplane.HostMetricSample(host)
	if !ok {
		return kscore.WorkerUsage{}, false
	}
	var usage kscore.WorkerUsage
	observed := false
	absolute := func(percent *float64, total int) int {
		if percent == nil || total <= 0 {
			return 0
		}
		observed = true
		return int(math.Ceil(*percent / 100 * float64(total)))
	}
	usage.CPU = absolute(sample.CPU, capacity.CPU)
	usage.RAM = absolute(sample.Memory, capacity.RAM)
	usage.Disk = absolute(sample.Disk, capacity.Disk)
	return usage, observed
}

// unifierServerEvidence is the canonical server rows and connection history of
// one tenant, read once per inventory so every worker is enriched from the same
// observation.
type unifierServerEvidence struct {
	now      time.Time
	byWorker map[string]controlplane.ServerRuntime
	byID     map[string]controlplane.ServerRuntime
	// historyRead is true only when the transition history was read. Without
	// it "no transitions" would claim the current state held for the whole
	// window, which is evidence the read never produced.
	historyRead bool
	transitions map[string][]controlplane.ServerStateTransition
}

// serverEvidence reads the tenant's servers and their connection transitions
// over the planning window. It is best effort by design: a failed read leaves
// the evidence out, which the planner reads as "no evidence", and never fails
// the recommendation that asked for the inventory.
func (api *UnifierAPI) serverEvidence(ctx context.Context, tenantID string, now time.Time) unifierServerEvidence {
	evidence := unifierServerEvidence{now: now}
	if api.servers == nil {
		return evidence
	}
	servers, err := api.servers.ListServerRuntimesByTenant(ctx, tenantID, "")
	if err != nil {
		slog.Warn("unifier_server_evidence_unavailable", "error", err.Error())
		return evidence
	}
	evidence.byWorker = make(map[string]controlplane.ServerRuntime, len(servers))
	evidence.byID = make(map[string]controlplane.ServerRuntime, len(servers))
	for _, server := range servers {
		if serverRuntimeHiddenFromCurrentInventory(server) {
			continue
		}
		evidence.byID[server.ID] = server
		if workerID := strings.TrimSpace(server.WorkerID); workerID != "" {
			if current, taken := evidence.byWorker[workerID]; !taken || server.UpdatedAt.After(current.UpdatedAt) {
				evidence.byWorker[workerID] = server
			}
		}
	}
	if api.transitions == nil {
		return evidence
	}
	window := availability.NodeEvidenceWindow(now)
	rows, err := api.transitions.ListServerTransitionsSince(ctx, tenantID, "connection", window.Start, 0)
	if err != nil {
		slog.Warn("unifier_availability_evidence_unavailable", "error", err.Error())
		return evidence
	}
	evidence.historyRead = true
	evidence.transitions = make(map[string][]controlplane.ServerStateTransition)
	for _, row := range rows {
		evidence.transitions[row.ServerID] = append(evidence.transitions[row.ServerID], row)
	}
	return evidence
}

// apply adds the canonical server identity and the availability evidence to a
// worker mapped from its registry record. The server is the row bound to the
// worker (servers.worker_id); a worker whose runtime id is only named by its
// own capabilities falls back to that row.
func (e unifierServerEvidence) apply(worker *kscore.Worker, record controlplane.Worker) {
	server, found := e.byWorker[record.ID]
	if !found {
		server, found = e.byID[stringFromAny(record.Capabilities[workerFieldServerID])]
	}
	if !found {
		return
	}
	worker.ServerID = server.ID
	if stack := strings.TrimSpace(server.StackID); stack != "" {
		worker.StackID = stack
	}
	if !e.historyRead {
		// The owner's word needs no history; an observation does.
		if confirmation, confirmed := controlplane.ServerAvailabilityConfirmationOf(server); confirmed {
			worker.Availability = &kscore.WorkerAvailability{
				Class: confirmation.Class, Source: workerAvailabilitySourceConfirmed, Confirmed: true,
				WindowDays: availability.NodeEvidenceWindowDays,
			}
		}
		return
	}
	worker.Availability = workerAvailabilityFor(server, e.transitions[server.ID], e.now)
}

// workerAvailabilityFor folds one server's connection history over the planning
// window into the class the Layout Planner reads. Observed evidence stays on
// the result even when the owner confirmed a class, so a surface can show the
// owner what the machine did next to what they said. The class itself is the
// owner's word once confirmed; before that it is only ever what the history
// supports, and "unknown" in every case it does not settle.
func workerAvailabilityFor(server controlplane.ServerRuntime, transitions []controlplane.ServerStateTransition, now time.Time) *kscore.WorkerAvailability {
	return workerAvailabilityFrom(server, nodeAvailabilityReport(server, transitions, now))
}

// nodeAvailabilityReport folds a server's connection transitions over the
// planning window; the server was enrolled when its row was created.
func nodeAvailabilityReport(server controlplane.ServerRuntime, transitions []controlplane.ServerStateTransition, now time.Time) availability.ServerReport {
	return availability.BuildServerReport(availability.NodeEvidenceWindow(now), availability.Input{
		ServerID: server.ID, EnrolledAt: server.CreatedAt, CurrentState: server.ConnectionState, Transitions: transitions,
	})
}

// workerAvailabilityFrom states the class for a folded report and the owner's
// confirmation, if any.
func workerAvailabilityFrom(server controlplane.ServerRuntime, report availability.ServerReport) *kscore.WorkerAvailability {
	result := &kscore.WorkerAvailability{
		Class:           availability.ClassifyNode(report),
		Source:          workerAvailabilitySourceTransitions,
		UptimeRatio:     report.UptimeRatio,
		ObservedSeconds: report.ObservedSeconds,
		WindowDays:      availability.NodeEvidenceWindowDays,
	}
	if report.ObservedSeconds == 0 {
		result.Source = workerAvailabilitySourceNone
	}
	if confirmation, confirmed := controlplane.ServerAvailabilityConfirmationOf(server); confirmed {
		result.Class, result.Source, result.Confirmed = confirmation.Class, workerAvailabilitySourceConfirmed, true
	}
	return result
}

// unifierWorkers maps the owner's registry records, enriched from the tenant's
// canonical servers. records must already be filtered to the owner.
func (api *UnifierAPI) unifierWorkers(ctx context.Context, tenantID string, records []controlplane.Worker, now time.Time) []kscore.Worker {
	evidence := api.serverEvidence(ctx, tenantID, now)
	workers := make([]kscore.Worker, 0, len(records))
	for _, record := range records {
		worker := unifierWorkerFromRecordAt(record, now)
		evidence.apply(&worker, record)
		workers = append(workers, worker)
	}
	return workers
}
