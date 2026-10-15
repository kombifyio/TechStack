package httpguard

// Unmanaged service discovery.
//
// The Guard inventory used to be a pure projection of the StackKit access
// manifest: with no `.stackkit/access.json` on disk, CollectInventory returned
// an empty service set, the control plane faithfully projected zero services,
// and the product's Applications view stayed empty on every host that is not
// running a StackKit. This file enumerates what is actually running on the
// host, so the `observed` half of the service model (migration 074) has a data
// path whether or not a StackKit is deployed.
//
// # Honesty rules
//
// Every value reported here is measured, never assumed. A probe that is not
// installed, not permitted, or times out contributes nothing instead of a
// guess, and a runtime state the probe does not express becomes `unknown`.
// Health is only reported when the runtime itself reports health evidence: a
// container without a HEALTHCHECK carries no health key at all rather than an
// optimistic green value.
//
// # Bounds
//
// Discovery must never delay the next heartbeat. Every probe shares one
// bounded budget, reads a bounded amount of output, and the merged result is
// capped so a host with thousands of containers cannot produce an unbounded
// inventory payload.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

const (
	discoveryDefaultTimeout   = 5 * time.Second
	discoveryDefaultMax       = 200
	discoveryMaxProbeOutput   = 4 << 20
	discoveryPlatformDocker   = "docker"
	discoveryPlatformSystemd  = "systemd"
	discoveryHealthKeySource  = "source"
	discoveryHealthKeyDocker  = "docker_health"
	discoverySystemdUnitSufix = ".service"
)

// defaultSystemdUnitDirs holds the operator-owned systemd unit directory.
//
// Only unit files placed DIRECTLY in this directory are reported. That is the
// location systemd reserves for locally installed units, so it separates the
// handful of services an operator actually deployed (including the Techstack
// agent itself) from the ~200 distribution units shipped under
// /usr/lib/systemd/system. The `*.wants` subdirectories are deliberately not
// scanned: an enabled distribution unit is symlinked there and is not an
// operator-installed application.
var defaultSystemdUnitDirs = []string{"/etc/systemd/system"}

// commandRunner executes one bounded, read-only host probe and returns its
// stdout. It is the single seam that keeps discovery testable without a Docker
// daemon or a systemd instance.
type commandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

// DiscoveryConfig bounds unmanaged service discovery. The zero value enables
// discovery with the documented defaults; set Disabled to turn it off.
type DiscoveryConfig struct {
	Disabled       bool
	Budget         time.Duration
	MaxServices    int
	SystemdUnitDir []string
	// Probe runs the container probes for an unprivileged Guard through the
	// privileged executor. The docker socket is root-equivalent, so the daemon
	// never gets access to it; nil runs the probe locally.
	Probe func(ctx context.Context, name string, args ...string) ([]byte, error)
	// run and readUnitDir are unexported test seams. Production always uses the
	// real host probes.
	run         commandRunner
	readUnitDir func(string) ([]os.DirEntry, error)
}

type hostDiscovery struct {
	cfg DiscoveryConfig
}

// discoveryOutcome separates "discovery ran and found nothing" from "discovery
// never produced evidence". Without that distinction an empty service list is
// unreadable, which is exactly the failure this file exists to fix.
type discoveryOutcome struct {
	Services      []Service
	DockerVersion string
	Probed        bool
	Complete      bool
}

func newHostDiscovery(cfg DiscoveryConfig) *hostDiscovery {
	if cfg.Budget <= 0 {
		cfg.Budget = discoveryDefaultTimeout
	}
	if cfg.MaxServices <= 0 {
		cfg.MaxServices = discoveryDefaultMax
	}
	if len(cfg.SystemdUnitDir) == 0 {
		cfg.SystemdUnitDir = defaultSystemdUnitDirs
	}
	if cfg.run == nil {
		cfg.run = execCommandRunner
		if probe := cfg.Probe; probe != nil {
			cfg.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name == discoveryPlatformDocker {
					return probe(ctx, name, args...)
				}
				return execCommandRunner(ctx, name, args...)
			}
		}
	}
	if cfg.readUnitDir == nil {
		cfg.readUnitDir = os.ReadDir
	}
	return &hostDiscovery{cfg: cfg}
}

// discover enumerates the services running on this host. It never returns an
// error: a host probe that is absent or refused is missing evidence, not a
// reason to drop the heartbeat that carries server liveness.
func (d *hostDiscovery) discover(ctx context.Context) discoveryOutcome {
	if d == nil || d.cfg.Disabled {
		return discoveryOutcome{}
	}
	budgetCtx, cancel := context.WithTimeout(ctx, d.cfg.Budget)
	defer cancel()

	outcome := discoveryOutcome{Complete: true}
	collected := make([]Service, 0, d.cfg.MaxServices)
	for _, probe := range []func(context.Context) ([]Service, bool, bool){
		d.discoverDockerContainersCoverage,
		d.discoverSystemdUnitsCoverage,
	} {
		services, probed, complete := probe(budgetCtx)
		outcome.Complete = outcome.Complete && complete
		if probed {
			outcome.Probed = true
		}
		collected = append(collected, services...)
	}
	unique := boundedUniqueServices(collected, len(collected)+1)
	outcome.Complete = outcome.Complete && len(unique) <= d.cfg.MaxServices && budgetCtx.Err() == nil
	outcome.Services = boundedUniqueServices(unique, d.cfg.MaxServices)
	outcome.DockerVersion = d.observeDockerVersion(budgetCtx)
	return outcome
}

func (d *hostDiscovery) observeDockerVersion(ctx context.Context) string {
	output, err := d.cfg.run(ctx, discoveryPlatformDocker,
		"version", "--format", "{{.Server.Version}}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// dockerContainer is the subset of `docker ps --format {{json .}}` this probe
// reads. Docker's CLI JSON keys are stable across versions.
type dockerContainer struct {
	ID     string `json:"ID"`
	Names  string `json:"Names"`
	Image  string `json:"Image"`
	State  string `json:"State"`
	Status string `json:"Status"`
	Labels string `json:"Labels"`
	// Ports is the human port summary, e.g. "0.0.0.0:8080->80/tcp, 5432/tcp".
	Ports string `json:"Ports"`
}

func (d *hostDiscovery) discoverDockerContainers(ctx context.Context) ([]Service, bool) {
	services, probed, _ := d.discoverDockerContainersCoverage(ctx)
	return services, probed
}

func (d *hostDiscovery) discoverDockerContainersCoverage(ctx context.Context) ([]Service, bool, bool) {
	output, err := d.cfg.run(ctx, discoveryPlatformDocker,
		"ps", "--all", "--no-trunc", "--format", "{{json .}}")
	if err != nil {
		// A verified missing executable is not applicable. A denied daemon,
		// timeout or failed command remains missing evidence.
		return nil, false, errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)
	}
	complete := true
	services := make([]Service, 0, 8)
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var container dockerContainer
		if err := json.Unmarshal([]byte(line), &container); err != nil {
			complete = false
			continue
		}
		service, ok := dockerContainerService(container)
		if !ok {
			complete = false
			continue
		}
		services = append(services, service)
	}
	return services, true, complete
}

func dockerContainerService(container dockerContainer) (Service, bool) {
	name := firstDockerName(container.Names)
	if name == "" {
		return Service{}, false
	}
	key := discoveryServiceKey(discoveryPlatformDocker, name)
	health := map[string]any{discoveryHealthKeySource: "docker-ps"}
	// Docker only reports health for a container that declares a HEALTHCHECK.
	// Absent evidence stays absent: runtimehealth then derives a non-green
	// transitional state from the runtime state alone.
	if reported := dockerReportedHealth(container.Status); reported != "" {
		health[discoveryHealthKeyDocker] = reported
	}
	labels := dockerLabelMap(container.Labels)
	runtimeIdentity := map[string]string{"container_name": name}
	if project := labels["com.docker.compose.project"]; project != "" {
		runtimeIdentity["kind"] = "docker_compose_service"
		runtimeIdentity["project"] = project
		runtimeIdentity["service"] = labels["com.docker.compose.service"]
		runtimeIdentity["file"] = labels["com.docker.compose.project.config_files"]
	}
	return Service{
		ID:              key,
		ServiceID:       key,
		Key:             key,
		Name:            name,
		Status:          dockerObservedState(container.State),
		Source:          serviceregistry.SourceObserved,
		Instance:        "default",
		ContainerID:     strings.TrimSpace(container.ID),
		PlatformID:      strings.TrimSpace(container.ID),
		PlatformType:    discoveryPlatformDocker,
		Image:           strings.TrimSpace(container.Image),
		ImageVersion:    boundedImageVersionLabel(labels[ociImageVersionLabel]),
		Health:          health,
		RuntimeIdentity: runtimeIdentity,
		publishedPorts:  parsePublishedPorts(container.Ports),
	}, true
}

const ociImageVersionLabel = "org.opencontainers.image.version"

func boundedImageVersionLabel(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 64 || strings.ContainsFunc(value, unicode.IsSpace) {
		return ""
	}
	return value
}

func dockerLabelMap(raw string) map[string]string {
	result := map[string]string{}
	for _, entry := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if key != "" && len(key) <= 128 && len(value) <= 1024 {
			result[key] = value
		}
	}
	return result
}

// dockerObservedState projects a Docker container state onto the constrained
// observed vocabulary of migration 073. A state Docker does not express as one
// of these becomes `unknown` rather than an optimistic guess.
func dockerObservedState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "running":
		return string(serviceregistry.ObservedRunning)
	case "created", "restarting":
		return string(serviceregistry.ObservedStarting)
	// A paused or removing container is not serving, and an exited container
	// has stopped. None of them proves an error, so none of them claims one.
	case "paused", "removing", "exited":
		return string(serviceregistry.ObservedStopped)
	// `dead` is Docker's unrecoverable state: the container could not be
	// stopped or removed cleanly.
	case "dead":
		return string(serviceregistry.ObservedError)
	default:
		return string(serviceregistry.ObservedUnknown)
	}
}

// dockerReportedHealth extracts the health verdict Docker appends to the human
// status string, e.g. "Up 3 days (healthy)". An empty result means the
// container declares no HEALTHCHECK, which is missing evidence, not health.
func dockerReportedHealth(status string) string {
	lower := strings.ToLower(strings.TrimSpace(status))
	switch {
	case strings.Contains(lower, "(healthy)"):
		return string(serviceregistry.HealthHealthy)
	case strings.Contains(lower, "(unhealthy)"):
		return string(serviceregistry.HealthUnhealthy)
	case strings.Contains(lower, "(health: starting)"):
		return string(serviceregistry.HealthStarting)
	default:
		return ""
	}
}

func firstDockerName(names string) string {
	for _, name := range strings.Split(names, ",") {
		if name = strings.TrimSpace(name); name != "" {
			return name
		}
	}
	return ""
}

// systemdUnit is the subset of `systemctl list-units --output=json` this probe
// reads.
type systemdUnit struct {
	Unit        string `json:"unit"`
	Active      string `json:"active"`
	Sub         string `json:"sub"`
	Description string `json:"description"`
}

func (d *hostDiscovery) discoverSystemdUnits(ctx context.Context) ([]Service, bool) {
	services, probed, _ := d.discoverSystemdUnitsCoverage(ctx)
	return services, probed
}

func (d *hostDiscovery) discoverSystemdUnitsCoverage(ctx context.Context) ([]Service, bool, bool) {
	installed, complete := d.operatorInstalledUnitsCoverage()
	if len(installed) == 0 {
		// Nothing an operator installed locally: there is nothing to enumerate,
		// and listing the distribution's own units would be noise, not evidence.
		return nil, false, complete
	}
	output, err := d.cfg.run(ctx, "systemctl",
		"list-units", "--type=service", "--all", "--output=json", "--no-pager")
	if err != nil {
		return nil, false, false
	}
	var units []systemdUnit
	if err := json.Unmarshal(output, &units); err != nil {
		return nil, false, false
	}
	services := make([]Service, 0, len(installed))
	seen := map[string]bool{}
	for _, unit := range units {
		name := strings.TrimSpace(unit.Unit)
		key := strings.ToLower(name)
		_, operatorInstalled := installed[key]
		_, installedTemplate := installed[systemdTemplateName(key)]
		if !operatorInstalled && !installedTemplate {
			continue
		}
		if key == systemdTemplateName(key) && strings.Contains(key, "@") {
			continue
		}
		services = append(services, systemdUnitService(unit, name))
		seen[key] = true
	}
	for name := range installed {
		// A bare template is not runnable; only its enumerated instances run.
		if strings.Contains(name, "@.service") {
			continue
		}
		if !seen[name] {
			complete = false
		}
	}
	return services, true, complete
}

func systemdTemplateName(name string) string {
	if at := strings.Index(name, "@"); at >= 0 && strings.HasSuffix(name, discoverySystemdUnitSufix) {
		return name[:at+1] + discoverySystemdUnitSufix
	}
	return ""
}

func systemdUnitService(unit systemdUnit, name string) Service {
	key := discoveryServiceKey(discoveryPlatformSystemd, strings.TrimSuffix(name, discoverySystemdUnitSufix))
	return Service{
		ID:              key,
		ServiceID:       key,
		Key:             key,
		Name:            name,
		Status:          systemdObservedState(unit.Active, unit.Sub),
		Source:          serviceregistry.SourceObserved,
		Instance:        "default",
		PlatformID:      name,
		PlatformType:    discoveryPlatformSystemd,
		Description:     strings.TrimSpace(unit.Description),
		Health:          map[string]any{discoveryHealthKeySource: "systemctl-list-units"},
		RuntimeIdentity: map[string]string{"kind": "systemd_unit", "unit": name},
	}
}

// systemdObservedState projects a unit's ActiveState/SubState pair onto the
// constrained observed vocabulary. `active (exited)` is a oneshot unit that
// completed: no process is running, so it reports stopped rather than running.
func systemdObservedState(active, sub string) string {
	switch strings.ToLower(strings.TrimSpace(active)) {
	case "active":
		if strings.EqualFold(strings.TrimSpace(sub), "running") {
			return string(serviceregistry.ObservedRunning)
		}
		return string(serviceregistry.ObservedStopped)
	case "activating", "reloading":
		return string(serviceregistry.ObservedStarting)
	case "deactivating", "inactive":
		return string(serviceregistry.ObservedStopped)
	case "failed":
		return string(serviceregistry.ObservedError)
	default:
		return string(serviceregistry.ObservedUnknown)
	}
}

// operatorInstalledUnits lists the service units an operator installed on this
// host. A masked unit (a symlink to /dev/null) is excluded: it is a decision to
// never run the service, not a deployed application. Templates and enabled
// instance links are retained so loaded instances can be observed too.
func (d *hostDiscovery) operatorInstalledUnits() map[string]struct{} {
	installed, _ := d.operatorInstalledUnitsCoverage()
	return installed
}

func (d *hostDiscovery) operatorInstalledUnitsCoverage() (map[string]struct{}, bool) {
	installed := map[string]struct{}{}
	complete := len(d.cfg.SystemdUnitDir) > 0
	dirs := append([]string(nil), d.cfg.SystemdUnitDir...)
	for index := 0; index < len(dirs); index++ {
		dir := dirs[index]
		if strings.TrimSpace(dir) == "" {
			continue
		}
		entries, err := d.cfg.readUnitDir(dir)
		if err != nil {
			if !os.IsNotExist(err) {
				complete = false
			}
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasSuffix(name, ".wants") || strings.HasSuffix(name, ".requires") {
				if index < len(d.cfg.SystemdUnitDir) {
					dirs = append(dirs, filepath.Join(dir, name))
				} else {
					complete = false
				}
				continue
			}
			if entry.IsDir() {
				continue
			}
			if !strings.HasSuffix(name, discoverySystemdUnitSufix) {
				continue
			}
			if maskedSystemdUnit(filepath.Join(dir, name)) {
				continue
			}
			installed[strings.ToLower(name)] = struct{}{}
		}
	}
	return installed, complete
}

func maskedSystemdUnit(path string) bool {
	target, err := os.Readlink(path)
	if err != nil {
		return false
	}
	return strings.TrimSpace(target) == os.DevNull
}

// discoveryServiceKey namespaces a discovered service by the runtime that
// reported it, so a container and a unit that happen to share a name stay two
// distinct services instead of overwriting each other's identity.
func discoveryServiceKey(platform, name string) string {
	return strings.ToLower(strings.TrimSpace(platform)) + "/" + strings.ToLower(strings.TrimSpace(name))
}

// boundedUniqueServices sorts by key for a stable payload, drops duplicates,
// and caps the result so one host cannot publish an unbounded inventory.
func boundedUniqueServices(services []Service, limit int) []Service {
	sort.SliceStable(services, func(i, j int) bool { return services[i].Key < services[j].Key })
	seen := make(map[string]struct{}, len(services))
	out := make([]Service, 0, min(len(services), limit))
	for _, service := range services {
		if service.Key == "" {
			continue
		}
		if _, duplicate := seen[service.Key]; duplicate {
			continue
		}
		seen[service.Key] = struct{}{}
		out = append(out, service)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// discoveryProbeBinaries is the complete set of programs discovery may execute.
// The allowlist is enforced rather than assumed, so no future caller can turn
// this seam into an arbitrary-command path on an agent host.
var discoveryProbeBinaries = map[string]struct{}{
	discoveryPlatformDocker: {}, "systemctl": {},
}

// execCommandRunner is the production probe. It reads a bounded amount of
// stdout, discards stderr so a diagnostic message can never reach an
// observation payload, and pins the locale so parsing does not depend on the
// host's language settings.
func execCommandRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	if _, allowed := discoveryProbeBinaries[name]; !allowed {
		return nil, fmt.Errorf("discovery probe %q is not allowlisted", name)
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("discovery probe %q unavailable: %w", name, err)
	}
	// #nosec G204 -- name is checked against discoveryProbeBinaries above and
	// resolved through PATH; args are compile-time constants in this file.
	command := exec.CommandContext(ctx, path, args...)
	command.Env = append(os.Environ(), "LC_ALL=C")
	var stdout bytes.Buffer
	writer := &boundedWriter{buffer: &stdout, limit: discoveryMaxProbeOutput}
	command.Stdout = writer
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("discovery probe %q failed: %w", name, err)
	}
	if writer.truncated {
		return nil, fmt.Errorf("discovery probe %q output truncated", name)
	}
	return stdout.Bytes(), nil
}

type boundedWriter struct {
	buffer    *bytes.Buffer
	limit     int
	truncated bool
}

// Write keeps the io.Writer contract while discarding everything past the
// limit: the caller is always told the whole payload was accepted, so a probe
// that floods stdout is truncated instead of failing.
func (w *boundedWriter) Write(payload []byte) (int, error) {
	accepted := len(payload)
	remaining := w.limit - w.buffer.Len()
	if remaining <= 0 {
		w.truncated = w.truncated || len(payload) > 0
		return accepted, nil
	}
	if len(payload) > remaining {
		w.truncated = true
		payload = payload[:remaining]
	}
	if written, err := w.buffer.Write(payload); err != nil {
		return written, err
	}
	return accepted, nil
}
