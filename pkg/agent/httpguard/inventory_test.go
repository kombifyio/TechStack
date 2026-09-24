package httpguard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	gopsutilnet "github.com/shirou/gopsutil/v4/net"
)

func TestSystemCollectorReportsOnlyBoundRuntimeListeners(t *testing.T) {
	collector := NewSystemCollector(SystemCollectorConfig{})
	collector.connections = func(context.Context, string) ([]gopsutilnet.ConnectionStat, error) {
		return []gopsutilnet.ConnectionStat{
			{Type: 1, Status: "LISTEN", Laddr: gopsutilnet.Addr{IP: "0.0.0.0", Port: 443}},
			{Type: 1, Status: "ESTABLISHED", Laddr: gopsutilnet.Addr{IP: "10.0.0.2", Port: 443}},
			{Type: 2, Laddr: gopsutilnet.Addr{IP: "::1", Port: 5353}},
			{Type: 2, Laddr: gopsutilnet.Addr{IP: "10.0.0.2", Port: 49152}, Raddr: gopsutilnet.Addr{IP: "1.1.1.1", Port: 53}},
			{Type: 1, Status: "listen", Laddr: gopsutilnet.Addr{IP: "0.0.0.0", Port: 443}},
		}, nil
	}

	ports, complete := collector.collectOpenPorts(t.Context())
	if !complete || len(ports) != 2 || ports[0] != "tcp://0.0.0.0:443" || ports[1] != "udp://[::1]:5353" {
		t.Fatalf("collectOpenPorts() = %#v complete=%t", ports, complete)
	}
}

func TestSystemCollectorReportsAuthGatewayAsReachableNotHealthy(t *testing.T) {
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer probe.Close()
	path := filepath.Join(t.TempDir(), "access.json")
	manifest := `{
		"stackkit":"basement-kit",
		"stackkitVersion":"1.2.3",
		"mode":"standalone",
		"domain":"home",
		"services":[{"key":"auth","name":"tinyauth","displayName":"TinyAuth","url":"` + probe.URL + `","desiredState":"running","allowedActions":["start","restart","logs"],"evidenceRef":"stackkit-evidence://service-control/sha256:abc"}]
	}`
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	collector := NewSystemCollector(SystemCollectorConfig{
		AccessManifestFiles: []string{path},
		ProbeTimeout:        time.Second,
		Discovery:           DiscoveryConfig{Disabled: true},
	})
	snapshot, err := collector.Collect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.StackKit != "basement-kit" || snapshot.StackKitVersion != "1.2.3" || snapshot.StackKitMode != "standalone" {
		t.Fatalf("StackKit identity = %#v", snapshot)
	}
	if !snapshot.ManifestObserved {
		t.Fatal("successfully read access manifest was not marked authoritative")
	}
	if len(snapshot.Services) != 1 {
		t.Fatalf("services = %d, want 1", len(snapshot.Services))
	}
	service := snapshot.Services[0]
	if service.Key != "auth" || service.Status != "reachable" {
		t.Fatalf("service = %#v", service)
	}
	if service.DesiredState != "running" || len(service.Actions) != 3 || service.EvidenceRef == "" {
		t.Fatalf("service control projection = %#v", service)
	}
	if service.Health["status_code"] != http.StatusUnauthorized {
		t.Fatalf("probe health = %#v", service.Health)
	}
	if _, hasHealthyVerdict := service.Health["healthy"]; hasHealthyVerdict || service.Health["auth_or_redirect_required"] != true {
		t.Fatalf("auth gateway was projected as healthy: %#v", service.Health)
	}
	if len(service.Endpoints) != 1 || service.Endpoints[0].Provenance != "stackkit-access-manifest" || service.Endpoints[0].Visibility != "local" {
		t.Fatalf("endpoints = %#v", service.Endpoints)
	}
}

func TestSystemCollectorDoesNotInventManifestServicesWithoutManifest(t *testing.T) {
	collector := NewSystemCollector(SystemCollectorConfig{
		AccessManifestFiles: []string{filepath.Join(t.TempDir(), "missing.json")},
		// Host discovery is exercised in discovery_test.go against recorded
		// probe output; this test is about the manifest half only.
		Discovery: DiscoveryConfig{Disabled: true},
	})
	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Services == nil || len(snapshot.Services) != 0 {
		t.Fatalf("services = %#v, want an observed empty inventory", snapshot.Services)
	}
	if snapshot.ManifestObserved {
		t.Fatal("missing access manifest was marked authoritative")
	}
	if snapshot.Host.Hostname == "" || snapshot.Host.OS == "" || snapshot.Host.Arch == "" {
		t.Fatalf("host inventory is incomplete: %#v", snapshot.Host)
	}
}

// The bug this collector was built around: a host without a StackKit reported
// zero services forever, so the whole unmanaged half of the model had no data
// path. A missing manifest must silence the manifest half only.
func TestSystemCollectorDiscoversUnmanagedServicesWithoutAManifest(t *testing.T) {
	collector := NewSystemCollector(SystemCollectorConfig{
		AccessManifestFiles: []string{filepath.Join(t.TempDir(), "missing.json")},
		Discovery: DiscoveryConfig{
			SystemdUnitDir: []string{t.TempDir()},
			run: (&fakeHostProbes{
				docker:        `{"ID":"c0ffee","Names":"vaultwarden","Image":"vaultwarden/server","State":"running","Status":"Up 3 days (healthy)"}`,
				dockerVersion: "27.1.1",
			}).run,
		},
	})
	snapshot, err := collector.CollectInventory(t.Context(), Snapshot{Services: []Service{}})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ManifestObserved {
		t.Fatal("missing access manifest was marked authoritative")
	}
	if !snapshot.DiscoveryObserved || snapshot.DiscoveredServiceCount != 1 {
		t.Fatalf("discovery evidence not reported: %#v", snapshot)
	}
	if snapshot.Host.DockerVersion != "27.1.1" {
		t.Fatalf("Docker runtime observation = %q, want 27.1.1", snapshot.Host.DockerVersion)
	}
	if len(snapshot.Services) != 1 {
		t.Fatalf("services = %#v, want the discovered container", snapshot.Services)
	}
	service := snapshot.Services[0]
	if service.Key != "docker/vaultwarden" || service.Source != "observed" || service.Status != "running" {
		t.Fatalf("discovered service = %#v", service)
	}
}

func TestSystemCollectorMarksReadEmptyManifestAuthoritative(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.json")
	if err := os.WriteFile(path, []byte(`{"stackkit":"basement-kit","services":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	collector := NewSystemCollector(SystemCollectorConfig{
		AccessManifestFiles: []string{path},
		Discovery:           DiscoveryConfig{Disabled: true},
	})
	snapshot, err := collector.CollectInventory(t.Context(), Snapshot{Services: []Service{}})
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.ManifestObserved || len(snapshot.Services) != 0 {
		t.Fatalf("empty manifest snapshot = %#v, want authoritative empty services", snapshot)
	}
}

// A StackKit service has a declared contract; a discovered one only has
// evidence. When both describe the same key, the contract must win.
func TestManifestServicesOutrankDiscoveredServicesOnTheSameKey(t *testing.T) {
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer probe.Close()
	path := filepath.Join(t.TempDir(), "access.json")
	manifest := `{"stackkit":"basement-kit","services":[{"key":"docker/vaultwarden","url":"` + probe.URL + `","desiredState":"running"}]}`
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	collector := NewSystemCollector(SystemCollectorConfig{
		AccessManifestFiles: []string{path},
		ProbeTimeout:        time.Second,
		Discovery: DiscoveryConfig{
			SystemdUnitDir: []string{t.TempDir()},
			run: (&fakeHostProbes{
				docker: `{"ID":"c0ffee","Names":"vaultwarden","State":"running","Status":"Up 3 days"}`,
			}).run,
		},
	})
	snapshot, err := collector.CollectInventory(t.Context(), Snapshot{Services: []Service{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Services) != 1 || snapshot.DiscoveredServiceCount != 0 {
		t.Fatalf("discovery displaced a declared service: %#v", snapshot.Services)
	}
	if snapshot.Services[0].Source != "stackkits-inventory" || snapshot.Services[0].DesiredState != "running" {
		t.Fatalf("manifest service lost its contract: %#v", snapshot.Services[0])
	}
}

func TestAccessManifestV3BindsMeasuredRuntimeComponentWithoutDuplicateDiscovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.json")
	manifest := `{
		"schemaVersion":"stackkit.access-manifest/v3",
		"stackkit":"cloud-kit",
		"services":[{"key":"coolify","displayName":"Coolify"}],
		"runtime_services":[{
			"service_key":"coolify","application_key":"coolify","display_name":"Coolify",
			"role":"application","lifecycle":"daemon","operational_impact":"critical",
			"runtime_identity":{"kind":"docker_compose_service","project":"stackkit-cloud-core","file":".stackkit/runtime/cloud-core/compose.yaml","service":"coolify"},
			"internal_address":"http://coolify:8000"
		}]
	}`
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	collector := NewSystemCollector(SystemCollectorConfig{
		AccessManifestFiles: []string{path},
		Discovery: DiscoveryConfig{SystemdUnitDir: []string{t.TempDir()}, run: (&fakeHostProbes{
			docker: `{"ID":"c0ffee","Names":"stackkit-cloud-core-coolify-1","State":"running","Status":"Up 2 hours (healthy)","Labels":"com.docker.compose.project=stackkit-cloud-core,com.docker.compose.service=coolify,com.docker.compose.project.config_files=/opt/kombify/.stackkit/runtime/cloud-core/compose.yaml"}`,
		}).run},
	})
	snapshot, err := collector.CollectInventory(t.Context(), Snapshot{Services: []Service{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Services) != 1 {
		t.Fatalf("v3 runtime binding produced duplicate rows: %#v", snapshot.Services)
	}
	service := snapshot.Services[0]
	if service.ApplicationKey != "coolify" || service.ApplicationDisplayName != "Coolify" || service.Status != "running" || service.OperationalImpact != "critical" {
		t.Fatalf("v3 runtime projection = %#v", service)
	}
}

func TestSystemCollectorProbesServicesWithinSharedConcurrentBudget(t *testing.T) {
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer probe.Close()
	path := filepath.Join(t.TempDir(), "access.json")
	manifest := `{
		"stackkit":"basement-kit",
		"services":[
			{"key":"one","url":"` + probe.URL + `/one"},
			{"key":"two","url":"` + probe.URL + `/two"},
			{"key":"three","url":"` + probe.URL + `/three"}
		]
	}`
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	collector := NewSystemCollector(SystemCollectorConfig{
		AccessManifestFiles: []string{path},
		ProbeTimeout:        time.Second,
		ProbeBudget:         time.Second,
		ProbeConcurrency:    3,
		Discovery:           DiscoveryConfig{Disabled: true},
	})
	started := time.Now()
	snapshot, err := collector.CollectInventory(t.Context(), Snapshot{Services: []Service{}})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed >= 350*time.Millisecond {
		t.Fatalf("service probes took %s; expected bounded concurrent collection", elapsed)
	}
	if len(snapshot.Services) != 3 {
		t.Fatalf("services = %d, want 3", len(snapshot.Services))
	}
}

func TestProbeServiceReportsServerFailureAsUnhealthy(t *testing.T) {
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "broken", http.StatusBadGateway)
	}))
	defer probe.Close()
	collector := NewSystemCollector(SystemCollectorConfig{ProbeTimeout: time.Second})
	service, ok := collector.probeService(t.Context(), accessManifestService{Key: "base", URL: probe.URL}, "")
	if !ok || service.Status != "unhealthy" || service.Endpoints[0].Health != "unhealthy" {
		t.Fatalf("service = %#v, ok = %t", service, ok)
	}
}

func TestProbeServiceDoesNotTreatMissingRouteAsHealthy(t *testing.T) {
	probe := httptest.NewServer(http.NotFoundHandler())
	defer probe.Close()
	collector := NewSystemCollector(SystemCollectorConfig{ProbeTimeout: time.Second})
	service, ok := collector.probeService(t.Context(), accessManifestService{Key: "missing", URL: probe.URL}, "")
	if !ok || service.Status != "unhealthy" {
		t.Fatalf("service = %#v, ok = %t", service, ok)
	}
}

func TestSafeIntClampsOverflow(t *testing.T) {
	const maxInt = int(^uint(0) >> 1)
	if got := safeInt(uint64(maxInt)); got != maxInt {
		t.Fatalf("safeInt(max) = %d, want %d", got, maxInt)
	}
	if got := safeInt(uint64(maxInt) + 1); got != maxInt {
		t.Fatalf("safeInt(max+1) = %d, want clamped %d", got, maxInt)
	}
}
