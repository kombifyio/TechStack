package httpguard

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeHostProbes replays recorded probe output so discovery is testable without
// a Docker daemon or a systemd instance, on every OS the agent builds for.
type fakeHostProbes struct {
	docker    string
	dockerErr error
	systemctl string
	calls     []string
}

func (f *fakeHostProbes) run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
	switch name {
	case "docker":
		if f.dockerErr != nil {
			return nil, f.dockerErr
		}
		return []byte(f.docker), nil
	case "systemctl":
		return []byte(f.systemctl), nil
	default:
		return nil, fmt.Errorf("unexpected probe %q", name)
	}
}

func discoveryWith(t *testing.T, probes *fakeHostProbes, units ...string) *hostDiscovery {
	t.Helper()
	dir := t.TempDir()
	for _, unit := range units {
		if err := os.WriteFile(filepath.Join(dir, unit), []byte("[Unit]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return newHostDiscovery(DiscoveryConfig{
		SystemdUnitDir: []string{dir},
		run:            probes.run,
	})
}

func serviceByKey(t *testing.T, services []Service, key string) Service {
	t.Helper()
	for _, service := range services {
		if service.Key == key {
			return service
		}
	}
	t.Fatalf("service %q not discovered: %#v", key, services)
	return Service{}
}

// The regression this file exists for: a plain host with no StackKit manifest
// must still report the services it is actually running, as `observed` rows
// with vocabulary-legal states.
func TestDiscoveryEnumeratesRunningContainerAsObservedService(t *testing.T) {
	probes := &fakeHostProbes{docker: strings.Join([]string{
		`{"ID":"c0ffee","Names":"vaultwarden","Image":"vaultwarden/server:1.30","State":"running","Status":"Up 3 days (healthy)"}`,
		`{"ID":"deadbe","Names":"old-backup","Image":"restic:latest","State":"exited","Status":"Exited (0) 2 days ago"}`,
	}, "\n")}
	outcome := discoveryWith(t, probes).discover(t.Context())
	if !outcome.Probed {
		t.Fatal("a successful docker probe was not recorded as evidence")
	}
	if len(outcome.Services) != 2 {
		t.Fatalf("services = %#v, want 2", outcome.Services)
	}

	running := serviceByKey(t, outcome.Services, "docker/vaultwarden")
	if running.Source != "observed" {
		t.Fatalf("discovered service source = %q, want observed: %#v", running.Source, running)
	}
	if running.Status != "running" {
		t.Fatalf("observed state = %q, want running", running.Status)
	}
	if running.PlatformType != "docker" || running.ContainerID != "c0ffee" || running.Image != "vaultwarden/server:1.30" {
		t.Fatalf("container identity not projected: %#v", running)
	}
	if running.Health["docker_health"] != "healthy" {
		t.Fatalf("declared container health was dropped: %#v", running.Health)
	}
	// A discovered service has no declared contract, so it must not carry one.
	if running.DesiredState != "" || len(running.Actions) != 0 || running.EvidenceRef != "" {
		t.Fatalf("discovered service invented a declared contract: %#v", running)
	}

	stopped := serviceByKey(t, outcome.Services, "docker/old-backup")
	if stopped.Status != "stopped" {
		t.Fatalf("exited container observed state = %q, want stopped", stopped.Status)
	}
	if _, claimed := stopped.Health["docker_health"]; claimed {
		t.Fatalf("a container without health evidence reported health: %#v", stopped.Health)
	}
}

// Every state the collector can emit must be inside the migration 073
// vocabularies, and anything the runtime does not express must be `unknown`
// rather than a guess.
func TestDiscoveryStatesStayInsideTheConstrainedVocabulary(t *testing.T) {
	observedVocabulary := map[string]bool{
		"running": true, "stopped": true, "starting": true, "error": true, "unknown": true,
	}
	for _, test := range []struct{ state, want string }{
		{state: "running", want: "running"},
		{state: "created", want: "starting"},
		{state: "restarting", want: "starting"},
		{state: "paused", want: "stopped"},
		{state: "removing", want: "stopped"},
		{state: "exited", want: "stopped"},
		{state: "dead", want: "error"},
		{state: "something-docker-invented", want: "unknown"},
		{state: "", want: "unknown"},
	} {
		if got := dockerObservedState(test.state); got != test.want || !observedVocabulary[got] {
			t.Fatalf("dockerObservedState(%q) = %q, want %q inside the vocabulary", test.state, got, test.want)
		}
	}
	for _, test := range []struct{ active, sub, want string }{
		{active: "active", sub: "running", want: "running"},
		{active: "active", sub: "exited", want: "stopped"},
		{active: "activating", sub: "start-pre", want: "starting"},
		{active: "reloading", sub: "reload", want: "starting"},
		{active: "inactive", sub: "dead", want: "stopped"},
		{active: "deactivating", sub: "stop", want: "stopped"},
		{active: "failed", sub: "failed", want: "error"},
		{active: "maintenance", sub: "", want: "unknown"},
	} {
		if got := systemdObservedState(test.active, test.sub); got != test.want || !observedVocabulary[got] {
			t.Fatalf("systemdObservedState(%q,%q) = %q, want %q inside the vocabulary", test.active, test.sub, got, test.want)
		}
	}
	healthVocabulary := map[string]bool{
		"healthy": true, "unhealthy": true, "starting": true, "not-required": true, "unknown": true,
	}
	for _, test := range []struct{ status, want string }{
		{status: "Up 3 days (healthy)", want: "healthy"},
		{status: "Up 2 minutes (unhealthy)", want: "unhealthy"},
		{status: "Up 5 seconds (health: starting)", want: "starting"},
		{status: "Up 9 days", want: ""},
		{status: "Exited (137) 1 hour ago", want: ""},
	} {
		got := dockerReportedHealth(test.status)
		if got != test.want || (got != "" && !healthVocabulary[got]) {
			t.Fatalf("dockerReportedHealth(%q) = %q, want %q", test.status, got, test.want)
		}
	}
}

// The Techstack agent itself is a locally installed unit, so a host that runs
// no containers at all still has something honest to report.
func TestDiscoveryReportsOperatorInstalledSystemdUnitsOnly(t *testing.T) {
	probes := &fakeHostProbes{
		dockerErr: errors.New("docker: command not found"),
		systemctl: `[
			{"unit":"techstack-agent.service","active":"active","sub":"running","description":"Kombify Techstack Agent"},
			{"unit":"ssh.service","active":"active","sub":"running","description":"OpenBSD Secure Shell server"},
			{"unit":"broken-app.service","active":"failed","sub":"failed","description":"Broken App"}
		]`,
	}
	// ssh.service is a distribution unit and is deliberately absent from the
	// operator-owned directory, so it must not appear as a deployed application.
	outcome := discoveryWith(t, probes, "techstack-agent.service", "broken-app.service").discover(t.Context())
	if !outcome.Probed {
		t.Fatal("a successful systemctl probe was not recorded as evidence")
	}
	if len(outcome.Services) != 2 {
		t.Fatalf("services = %#v, want the two operator-installed units", outcome.Services)
	}
	agent := serviceByKey(t, outcome.Services, "systemd/techstack-agent")
	if agent.Source != "observed" || agent.Status != "running" || agent.PlatformType != "systemd" {
		t.Fatalf("operator unit not projected as an observed service: %#v", agent)
	}
	if agent.PlatformID != "techstack-agent.service" || agent.Description != "Kombify Techstack Agent" {
		t.Fatalf("unit identity not projected: %#v", agent)
	}
	if broken := serviceByKey(t, outcome.Services, "systemd/broken-app"); broken.Status != "error" {
		t.Fatalf("failed unit observed state = %q, want error", broken.Status)
	}
	for _, service := range outcome.Services {
		if strings.Contains(service.Key, "ssh") {
			t.Fatalf("a distribution unit was reported as a deployed application: %#v", service)
		}
	}
}

// A probe that is not installed or not permitted is missing evidence. It must
// not be reported as a service, and it must not claim discovery ran.
func TestDiscoveryReportsNoEvidenceWhenEveryProbeIsUnavailable(t *testing.T) {
	probes := &fakeHostProbes{dockerErr: errors.New("permission denied while trying to connect to the Docker daemon socket")}
	outcome := discoveryWith(t, probes).discover(t.Context())
	if outcome.Probed {
		t.Fatal("a refused probe was recorded as discovery evidence")
	}
	if len(outcome.Services) != 0 {
		t.Fatalf("services = %#v, want none", outcome.Services)
	}
	// With no operator-installed units there is nothing to enumerate, so the
	// systemd probe must not even be executed.
	for _, call := range probes.calls {
		if strings.HasPrefix(call, "systemctl") {
			t.Fatalf("systemctl was probed with no operator-installed units: %#v", probes.calls)
		}
	}
}

func TestDiscoveryIsDisabledAndBoundedOnDemand(t *testing.T) {
	probes := &fakeHostProbes{docker: `{"ID":"a","Names":"one","State":"running","Status":"Up"}`}
	disabled := newHostDiscovery(DiscoveryConfig{Disabled: true, run: probes.run})
	if outcome := disabled.discover(t.Context()); outcome.Probed || len(outcome.Services) != 0 {
		t.Fatalf("disabled discovery still probed the host: %#v", outcome)
	}
	if len(probes.calls) != 0 {
		t.Fatalf("disabled discovery executed probes: %#v", probes.calls)
	}

	lines := make([]string, 0, 5)
	for index := range 5 {
		lines = append(lines, fmt.Sprintf(`{"ID":"id-%d","Names":"app-%d","State":"running","Status":"Up"}`, index, index))
	}
	bounded := newHostDiscovery(DiscoveryConfig{
		MaxServices: 2, run: (&fakeHostProbes{docker: strings.Join(lines, "\n")}).run,
	})
	outcome := bounded.discover(t.Context())
	if len(outcome.Services) != 2 {
		t.Fatalf("services = %d, want the configured cap of 2", len(outcome.Services))
	}
	if outcome.Services[0].Key >= outcome.Services[1].Key {
		t.Fatalf("discovered services are not in a stable order: %#v", outcome.Services)
	}
}

// A template unit cannot run without an instance, and only unit files placed
// directly in the operator-owned directory count: an enabled distribution unit
// is symlinked into a *.wants subdirectory and is not a deployed application.
func TestOperatorInstalledUnitsSkipTemplatesAndSubdirectories(t *testing.T) {
	dir := t.TempDir()
	for _, unit := range []string{"real-app.service", "worker@.service", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, unit), []byte("[Unit]\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	wants := filepath.Join(dir, "multi-user.target.wants")
	if err := os.Mkdir(wants, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wants, "distro.service"), []byte("[Unit]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	units := newHostDiscovery(DiscoveryConfig{SystemdUnitDir: []string{dir}}).operatorInstalledUnits()
	if _, present := units["real-app.service"]; !present {
		t.Fatalf("operator unit missing: %#v", units)
	}
	for _, excluded := range []string{"worker@.service", "notes.txt", "distro.service", "multi-user.target.wants"} {
		if _, present := units[excluded]; present {
			t.Fatalf("%s must not be reported as a deployed application: %#v", excluded, units)
		}
	}
}

// A masked unit is a symlink to /dev/null: a decision to never run the
// service, not a deployed application.
func TestOperatorInstalledUnitsSkipMaskedUnits(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real-app.service"), []byte("[Unit]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(os.DevNull, filepath.Join(dir, "masked-app.service")); err != nil {
		t.Skipf("symlinks unavailable on this host: %v", err)
	}
	units := newHostDiscovery(DiscoveryConfig{SystemdUnitDir: []string{dir}}).operatorInstalledUnits()
	if _, present := units["real-app.service"]; !present {
		t.Fatalf("operator unit missing: %#v", units)
	}
	if _, present := units["masked-app.service"]; present {
		t.Fatalf("a masked unit was reported as a deployed application: %#v", units)
	}
}

func TestOperatorInstalledUnitsToleratesAnUnreadableDirectory(t *testing.T) {
	discovery := newHostDiscovery(DiscoveryConfig{
		SystemdUnitDir: []string{"/etc/systemd/system", ""},
		readUnitDir:    func(string) ([]os.DirEntry, error) { return nil, fs.ErrPermission },
	})
	if units := discovery.operatorInstalledUnits(); len(units) != 0 {
		t.Fatalf("units = %#v, want none", units)
	}
}

// The production runner must be structurally incapable of executing anything
// other than the two host probes, not merely trusted not to.
func TestExecCommandRunnerRefusesNonAllowlistedBinaries(t *testing.T) {
	for _, name := range []string{"sh", "cmd", "rm", "curl", ""} {
		if _, err := execCommandRunner(t.Context(), name, "-c", "echo pwned"); err == nil ||
			!strings.Contains(err.Error(), "not allowlisted") {
			t.Fatalf("execCommandRunner(%q) error = %v, want an allowlist refusal", name, err)
		}
	}
	for name := range discoveryProbeBinaries {
		if _, allowed := map[string]bool{"docker": true, "systemctl": true}[name]; !allowed {
			t.Fatalf("unexpected allowlisted discovery probe %q", name)
		}
	}
}

func TestDiscoveryBudgetIsApplied(t *testing.T) {
	blocked := make(chan struct{})
	defer close(blocked)
	discovery := newHostDiscovery(DiscoveryConfig{
		Budget: 20 * time.Millisecond,
		run: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-blocked:
				return nil, nil
			}
		},
	})
	start := time.Now()
	outcome := discovery.discover(t.Context())
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("discovery ignored its budget: %s", elapsed)
	}
	if outcome.Probed || len(outcome.Services) != 0 {
		t.Fatalf("a timed-out probe produced evidence: %#v", outcome)
	}
}
