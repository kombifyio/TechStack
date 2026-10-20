package routes

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/agent/httpguard"
	"github.com/kombifyio/techstack/pkg/availability"
	"github.com/kombifyio/techstack/pkg/controlplane"
	kscore "github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

// evidenceNow is the clock of every test below.
var evidenceNow = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

// guardWorkerRecord is a worker record as a Guard inventory leaves it: the
// registration facts, and the host report with the machine-level hardware.
func guardWorkerRecord(lastSeen time.Time) controlplane.Worker {
	return controlplane.Worker{
		ID: "worker-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1",
		Hostname: "mini-pc", OS: "linux", Arch: "amd64", Status: "online", Type: "main", Provider: "local",
		CPUCores: 8, RAMMB: 16384, DiskGB: 500, DockerVersion: "27.0.0", GPU: "RTX 3060", LastSeenAt: &lastSeen,
		Resources: map[string]any{workerInventoryHostKey: map[string]any{
			"cpu_percent": 25.0, "memory_used_bytes": 4294967296.0, "memory_total_bytes": 17179869184.0,
			"disk_used_bytes": 107374182400.0, "disk_total_bytes": 536870912000.0,
			workerHardwareKey: map[string]any{
				"observed_at": "2026-10-19T11:30:00Z", "amd64_level": 3.0,
				"gpu":         map[string]any{"vendor": "nvidia", "driver": "nvidia", "driver_major": 550.0},
				"media":       map[string]any{"class": "hardware-transcode", "source": "guard-probe:sysfs"},
				"disk_class":  map[string]any{"class": "nvme", "source": "guard-probe:sysfs"},
				"power_class": map[string]any{"class": "low", "source": "estimate:dmi-chassis"},
			},
		}},
	}
}

func TestUnifierWorkerCarriesTheGuardsMachineFacts(t *testing.T) {
	worker := unifierWorkerFromRecordAt(guardWorkerRecord(evidenceNow.Add(-time.Minute)), evidenceNow)

	encoded, err := json.Marshal(worker)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("mapped worker: %s", encoded)

	caps := worker.Capabilities
	if caps.AMD64MicroarchitectureLevel != 3 {
		t.Errorf("amd64 level = %d, want 3", caps.AMD64MicroarchitectureLevel)
	}
	if gpu := caps.GPU; gpu == nil || gpu.Vendor != "nvidia" || gpu.DriverMajor != 550 || gpu.Model != "RTX 3060" {
		t.Errorf("gpu = %+v, want nvidia, driver 550, the registered model", gpu)
	}
	if caps.Media == nil || caps.Media.Class != "hardware-transcode" || !caps.HasHWTranscode {
		t.Errorf("media = %+v hwTranscode = %v, want the probe's hardware transcode", caps.Media, caps.HasHWTranscode)
	}
	if caps.DiskClass == nil || caps.DiskClass.Class != "nvme" || !caps.HasNVMe {
		t.Errorf("disk class = %+v nvme = %v", caps.DiskClass, caps.HasNVMe)
	}
	if caps.PowerClass == nil || caps.PowerClass.Source != "estimate:dmi-chassis" {
		t.Errorf("power class = %+v, want an estimate labelled as such", caps.PowerClass)
	}
	// Percentages times capacity, rounded up: 25 % of 8 cores, 16 GiB and 500 GB.
	if !worker.UsageObserved || worker.Usage.CPU != 2 || worker.Usage.RAM != 4096 || worker.Usage.Disk != 100 {
		t.Errorf("usage = %+v observed = %v, want 2 cores / 4096 MB / 100 GB observed", worker.Usage, worker.UsageObserved)
	}
}

// What the Guard did not report has to read as unknown: an empty vendor, level 0
// and no observation. Zero usage on a node that reported nothing would read as
// an idle node, and the planner would fill it.
func TestUnifierWorkerLeavesUnreportedFactsUnknown(t *testing.T) {
	legacy := controlplane.Worker{
		ID: "worker-2", OwnerSubjectID: "owner-1", Hostname: "old-box", Arch: "amd64", Status: "online",
		CPUCores: 4, RAMMB: 8192, DiskGB: 100, LastSeenAt: &evidenceNow,
	}
	worker := unifierWorkerFromRecordAt(legacy, evidenceNow)
	caps := worker.Capabilities
	if caps.AMD64MicroarchitectureLevel != 0 || caps.GPU != nil || caps.Media != nil || caps.DiskClass != nil || caps.PowerClass != nil {
		t.Errorf("a record with no probe grew facts: %+v", caps)
	}
	if worker.UsageObserved || worker.Usage.CPU != 0 || worker.Usage.RAM != 0 {
		t.Errorf("usage = %+v observed = %v, want none", worker.Usage, worker.UsageObserved)
	}

	// A host report from a node that has been silent for an hour is history.
	stale := guardWorkerRecord(evidenceNow.Add(-time.Hour))
	if worker := unifierWorkerFromRecordAt(stale, evidenceNow); worker.UsageObserved || worker.Usage.RAM != 0 {
		t.Errorf("a stale host report counted as the node's load: %+v", worker.Usage)
	}

	// A hand-edited row with a class outside the vocabulary never reaches the planner.
	forged := guardWorkerRecord(evidenceNow)
	forged.Resources[workerInventoryHostKey].(map[string]any)[workerHardwareKey].(map[string]any)["disk_class"] = map[string]any{"class": "quantum", "source": "guard-probe:sysfs"}
	if worker := unifierWorkerFromRecordAt(forged, evidenceNow); worker.Capabilities.DiskClass != nil {
		t.Errorf("a class outside the vocabulary was accepted: %+v", worker.Capabilities.DiskClass)
	}
}

// evidenceStore seeds one owner's worker, its canonical server and a connection
// history, and returns the API reading them.
func evidenceStore(t *testing.T, enrolledDaysAgo int, outages ...[2]time.Duration) (*controlplane.MemoryStore, *UnifierAPI) {
	t.Helper()
	store := controlplane.NewMemoryStore()
	store.SetNow(func() time.Time { return evidenceNow.Add(-time.Duration(enrolledDaysAgo) * 24 * time.Hour) })
	if _, err := store.UpsertWorkerHeartbeat(t.Context(), guardWorkerRecord(evidenceNow.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	seen := evidenceNow.Add(-time.Minute)
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: "server-1", TenantID: "tenant-1", StackID: "stack-1", OwnerSubjectID: "owner-1", WorkerID: "worker-1",
		Name: "mini-pc", LifecycleState: string(serverregistry.LifecycleActive),
		ConnectionState: string(serverregistry.ConnectionConnected), HealthState: "healthy", LastHeartbeatAt: &seen,
	}); err != nil {
		t.Fatal(err)
	}
	store.SetNow(nil)
	for _, outage := range outages {
		for _, transition := range []controlplane.ServerStateTransition{
			{FromState: "connected", ToState: "offline", ObservedAt: evidenceNow.Add(-outage[0])},
			{FromState: "offline", ToState: "connected", ObservedAt: evidenceNow.Add(-outage[1])},
		} {
			transition.TenantID, transition.ServerID, transition.Dimension, transition.Source = "tenant-1", "server-1", "connection", "sweeper"
			if _, err := store.AppendServerTransition(t.Context(), transition); err != nil {
				t.Fatal(err)
			}
		}
	}
	api, err := NewUnifierAPI(store)
	if err != nil {
		t.Fatal(err)
	}
	api.now = func() time.Time { return evidenceNow }
	return store, api
}

func inventoryWorker1(t *testing.T, api *UnifierAPI) (worker kscore.Worker) {
	t.Helper()
	inventory, err := api.fetchRecommendationInventory(t.Context(), "tenant-1", "owner-1")
	if err != nil || len(inventory.Workers) != 1 {
		t.Fatalf("inventory workers = %d err = %v", len(inventory.Workers), err)
	}
	return inventory.Workers[0]
}

func TestInventoryWorkerCarriesServerIdentityAndObservedAvailability(t *testing.T) {
	_, api := evidenceStore(t, 30)
	worker := inventoryWorker1(t, api)
	if worker.ServerID != "server-1" || worker.StackID != "stack-1" {
		t.Errorf("server = %q stack = %q, want the canonical server row bound by worker_id", worker.ServerID, worker.StackID)
	}
	got := worker.Availability
	if got == nil || got.Class != availability.NodeAlwaysOn || got.Source != "server-transitions" || got.Confirmed ||
		got.WindowDays != 14 || got.UptimeRatio == nil || *got.UptimeRatio != 1 {
		t.Fatalf("availability = %+v, want observed always-on over 14 days", got)
	}
	encoded, _ := json.Marshal(worker)
	t.Logf("inventory worker: %s", encoded)
}

func TestInventoryWorkerAvailabilityStaysUnknownWithoutEnoughHistory(t *testing.T) {
	// Enrolled two days ago: two days of observation is not enough to say.
	_, api := evidenceStore(t, 2)
	if got := inventoryWorker1(t, api).Availability; got == nil || got.Class != availability.NodeUnknown || got.Source != "server-transitions" {
		t.Fatalf("availability = %+v, want unknown from too little history", got)
	}
	// A worker with no canonical server has no availability evidence at all.
	api.servers = nil
	worker := inventoryWorker1(t, api)
	if worker.Availability != nil || worker.ServerID != "" {
		t.Fatalf("a worker without a server row grew evidence: %+v", worker)
	}
}

func TestInventoryWorkerPartTimeFromNightlyOutages(t *testing.T) {
	var nights [][2]time.Duration
	for day := 1; day <= 10; day++ {
		start := time.Duration(day)*24*time.Hour + 2*time.Hour
		nights = append(nights, [2]time.Duration{start, start - 8*time.Hour})
	}
	_, api := evidenceStore(t, 30, nights...)
	if got := inventoryWorker1(t, api).Availability; got == nil || got.Class != availability.NodePartTime {
		t.Fatalf("availability = %+v, want part-time", got)
	}
}

func TestOwnerConfirmationBecomesTheWorkersAvailability(t *testing.T) {
	store, api := evidenceStore(t, 2)
	handlers := serverRuntimeHandlers{store: store, availabilityReader: store, availabilityConfirmer: store, now: func() time.Time { return evidenceNow }}

	call := func(owner, method, body string) (int, serverAvailabilityResponse) {
		event, recorder := registryRouteStoreTestEvent(method, "/api/v1/servers/server-1/availability", owner, "tenant-1", nil)
		event.Request.SetPathValue("serverId", "server-1")
		if body != "" {
			event.Request.Body = io.NopCloser(strings.NewReader(body))
		}
		handler := handlers.availabilityRead
		if method == http.MethodPut {
			handler = handlers.availabilityConfirm
		}
		if err := handler(event); err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Data serverAvailabilityResponse `json:"data"`
		}
		_ = json.Unmarshal(recorder.Body.Bytes(), &envelope)
		return recorder.Code, envelope.Data
	}

	if code, view := call("owner-1", http.MethodGet, ""); code != http.StatusOK || view.Class != "unknown" || view.Source != "server-transitions" || view.Confirmed {
		t.Fatalf("before confirmation: %d %+v", code, view)
	}
	// Anyone but the owner learns nothing, not even that the server exists.
	if code, _ := call("intruder", http.MethodGet, ""); code != http.StatusNotFound {
		t.Fatalf("another owner read the availability: %d", code)
	}
	if code, _ := call("intruder", http.MethodPut, `{"class":"always-on"}`); code != http.StatusNotFound {
		t.Fatalf("another owner confirmed an availability: %d", code)
	}
	for _, body := range []string{`{}`, `{"class":"sometimes"}`, `{"class":"always-on","x":1}`, `{"class":7}`} {
		if code, _ := call("owner-1", http.MethodPut, body); code != http.StatusBadRequest {
			t.Fatalf("PUT %s = %d, want 400", body, code)
		}
	}

	code, view := call("owner-1", http.MethodPut, `{"class":"always-on"}`)
	if code != http.StatusOK || view.Class != "always-on" || view.Source != "user-confirmed" || !view.Confirmed ||
		view.ObservedClass != "unknown" || view.ConfirmedAt == nil {
		t.Fatalf("after confirmation: %d %+v, want the owner's word next to the observed class", code, view)
	}
	worker := inventoryWorker1(t, api)
	if got := worker.Availability; got == nil || got.Class != "always-on" || got.Source != "user-confirmed" || !got.Confirmed {
		t.Fatalf("worker availability = %+v, want the confirmation", got)
	}

	if code, view := call("owner-1", http.MethodPut, `{"class":null}`); code != http.StatusOK || view.Confirmed || view.Source != "server-transitions" {
		t.Fatalf("after withdrawing: %d %+v", code, view)
	}
}

// The Guard is authenticated but its report is still input: a class outside the
// closed vocabularies, an unnamed GPU vendor or a source that is not a plain
// token never reaches the stored host map.
func TestInventoryHostMapStoresOnlySanitizedHardware(t *testing.T) {
	host := workerInventoryHost{Hardware: &httpguard.HostHardware{
		ObservedAt: evidenceNow, AMD64Level: 9,
		GPU:        &httpguard.HostGPU{Vendor: "aspeed", Driver: "ast"},
		Media:      &httpguard.HostHardwareFact{Class: "hardware-transcode", Source: "guard-probe:sysfs"},
		DiskClass:  &httpguard.HostHardwareFact{Class: "tape", Source: "guard-probe:sysfs"},
		PowerClass: &httpguard.HostHardwareFact{Class: "low", Source: "estimate dmi chassis; DROP TABLE"},
	}}
	stored := inventoryHostMap(host)
	hardware := hostHardwareFromMap(stored)
	if hardware == nil || hardware.Media == nil {
		t.Fatalf("the valid member was dropped: %+v", hardware)
	}
	if hardware.AMD64Level != 0 || hardware.GPU != nil || hardware.DiskClass != nil || hardware.PowerClass != nil {
		t.Fatalf("members outside the vocabularies were stored: %+v", hardware)
	}

	if _, present := inventoryHostMap(workerInventoryHost{Hardware: &httpguard.HostHardware{AMD64Level: 9}})[workerHardwareKey]; present {
		t.Fatal("a report with nothing usable still stored a hardware member")
	}
}
