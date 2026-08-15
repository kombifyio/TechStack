package httpguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/kombifyio/techstack/pkg/runtimeconvergence"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

// Heartbeat is the existing worker heartbeat wire contract.
type Heartbeat struct {
	SourceEpoch        string                       `json:"source_epoch"`
	SourceSequence     int64                        `json:"source_sequence"`
	ObservedAt         time.Time                    `json:"observed_at"`
	CPUPercent         float64                      `json:"cpu_percent"`
	MemoryUsedBytes    int64                        `json:"memory_used_bytes"`
	MemoryTotalBytes   int64                        `json:"memory_total_bytes"`
	DiskUsedBytes      int64                        `json:"disk_used_bytes"`
	DiskTotalBytes     int64                        `json:"disk_total_bytes"`
	UptimeSeconds      float64                      `json:"uptime_seconds"`
	RuntimeConvergence *runtimeconvergence.Snapshot `json:"runtime_convergence,omitempty"`
}

// Snapshot is the existing worker inventory wire contract plus optional
// StackKit identity fields. Older control planes safely ignore the latter.
type Snapshot struct {
	SourceEpoch      string    `json:"source_epoch"`
	SourceSequence   int64     `json:"source_sequence"`
	ObservedAt       time.Time `json:"observed_at"`
	TenantID         string    `json:"tenant_id,omitempty"`
	OwnerID          string    `json:"owner_id,omitempty"`
	StackID          string    `json:"stack_id,omitempty"`
	LeaseID          string    `json:"lease_id,omitempty"`
	ServerID         string    `json:"server_id,omitempty"`
	RuntimeAgentID   string    `json:"runtime_agent_id"`
	Hostname         string    `json:"hostname,omitempty"`
	OS               string    `json:"os,omitempty"`
	Arch             string    `json:"arch,omitempty"`
	AgentVersion     string    `json:"agent_version,omitempty"`
	StackKit         string    `json:"stackkit,omitempty"`
	StackKitVersion  string    `json:"stackkit_version,omitempty"`
	StackKitMode     string    `json:"stackkit_mode,omitempty"`
	Domain           string    `json:"domain,omitempty"`
	ManifestObserved bool      `json:"manifest_observed"`
	// ManifestServiceCount is the number of services the access manifest
	// declares, before probing. It lets the control plane distinguish "the
	// manifest itself declares zero services" (legitimate prune-to-zero) from
	// "probing produced none" (no evidence — must not prune).
	ManifestServiceCount int `json:"manifest_service_count,omitempty"`
	// DiscoveryObserved reports whether at least one unmanaged-service probe
	// actually ran on this host. It lets the control plane distinguish "the
	// host runs nothing we can see" from "discovery never produced evidence",
	// which an empty service array alone cannot express.
	DiscoveryObserved bool `json:"discovery_observed"`
	// DiscoveredServiceCount is the number of unmanaged services discovery
	// enumerated, before any control-plane filtering.
	DiscoveredServiceCount int                          `json:"discovered_service_count,omitempty"`
	Host                   Host                         `json:"host"`
	Services               []Service                    `json:"services"`
	Channels               []Channel                    `json:"channels,omitempty"`
	Endpoints              []Endpoint                   `json:"endpoints,omitempty"`
	RuntimeConvergence     *runtimeconvergence.Snapshot `json:"runtime_convergence,omitempty"`
}

type Host struct {
	Hostname         string  `json:"hostname"`
	OS               string  `json:"os"`
	OSVersion        string  `json:"os_version,omitempty"`
	Arch             string  `json:"arch"`
	PublicIP         string  `json:"public_ip,omitempty"`
	PrivateIP        string  `json:"private_ip,omitempty"`
	LocalIP          string  `json:"local_ip,omitempty"`
	CPUCores         int     `json:"cpu_cores"`
	RAMMB            int     `json:"ram_mb"`
	DiskGB           int     `json:"disk_gb"`
	CPUPercent       float64 `json:"cpu_percent"`
	MemoryUsedBytes  int64   `json:"memory_used_bytes"`
	MemoryTotalBytes int64   `json:"memory_total_bytes"`
	DiskUsedBytes    int64   `json:"disk_used_bytes"`
	DiskTotalBytes   int64   `json:"disk_total_bytes"`
	UptimeSeconds    float64 `json:"uptime_seconds"`
}

type Service struct {
	ID        string `json:"id,omitempty"`
	ServiceID string `json:"service_id,omitempty"`
	Key       string `json:"key,omitempty"`
	Name      string `json:"name,omitempty"`
	Status    string `json:"status"`
	// Source is the provenance of this observation on the closed
	// pkg/serviceregistry vocabulary. `stackkits-inventory` is a StackKit
	// service that has a declared contract; `observed` is a service discovered
	// on the host without one. An absent value is read as
	// `stackkits-inventory` by the control plane, which is what every agent
	// predating unmanaged discovery could ever report.
	Source          string         `json:"source,omitempty"`
	URL             string         `json:"url,omitempty"`
	OwnerStack      string         `json:"owner_stack,omitempty"`
	TargetServer    string         `json:"target_server,omitempty"`
	ContainerID     string         `json:"container_id,omitempty"`
	PlatformID      string         `json:"platform_id,omitempty"`
	PlatformType    string         `json:"platform_type,omitempty"`
	Image           string         `json:"image,omitempty"`
	Description     string         `json:"description,omitempty"`
	Instance        string         `json:"instance,omitempty"`
	StackKitVersion string         `json:"stackkit_version,omitempty"`
	Actions         []string       `json:"actions,omitempty"`
	DesiredState    string         `json:"desired_state,omitempty"`
	EvidenceRef     string         `json:"evidence_ref,omitempty"`
	Health          map[string]any `json:"health,omitempty"`
	Endpoints       []Endpoint     `json:"endpoints,omitempty"`
}

type Endpoint struct {
	URL              string `json:"url"`
	Address          string `json:"address,omitempty"`
	Visibility       string `json:"visibility,omitempty"`
	Provenance       string `json:"provenance,omitempty"`
	Kind             string `json:"kind,omitempty"`
	Health           string `json:"health,omitempty"`
	TargetType       string `json:"target_type,omitempty"`
	AccessProfileRef string `json:"access_profile_ref,omitempty"`
}

type Channel struct {
	Kind       string `json:"kind"`
	URL        string `json:"url,omitempty"`
	Status     string `json:"status,omitempty"`
	Provenance string `json:"provenance,omitempty"`
}

// SystemCollectorConfig configures honest host and StackKit observations.
type SystemCollectorConfig struct {
	AccessManifestFiles []string
	PublicIP            string
	ProbeTimeout        time.Duration
	ProbeBudget         time.Duration
	ProbeConcurrency    int
	ProbeClient         *http.Client
	// Discovery bounds unmanaged service discovery. The zero value enables it
	// with the documented defaults.
	Discovery DiscoveryConfig
}

// SystemCollector reads the host, the services actually running on it, and,
// when present, the StackKit access manifest. Service status is derived from a
// live HTTP probe or a measured runtime state, never copied from the manifest's
// desired/running placeholder.
type SystemCollector struct {
	cfg         SystemCollectorConfig
	probeClient *http.Client
	discovery   *hostDiscovery
}

func NewSystemCollector(cfg SystemCollectorConfig) *SystemCollector {
	if len(cfg.AccessManifestFiles) == 0 {
		cfg.AccessManifestFiles = defaultAccessManifestFiles()
	}
	if cfg.ProbeTimeout <= 0 {
		cfg.ProbeTimeout = 5 * time.Second
	}
	if cfg.ProbeBudget <= 0 {
		cfg.ProbeBudget = 10 * time.Second
	}
	if cfg.ProbeConcurrency <= 0 {
		cfg.ProbeConcurrency = 8
	}
	if cfg.ProbeConcurrency > 32 {
		cfg.ProbeConcurrency = 32
	}
	client := cfg.ProbeClient
	if client == nil {
		client = &http.Client{
			Timeout: cfg.ProbeTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &SystemCollector{cfg: cfg, probeClient: client, discovery: newHostDiscovery(cfg.Discovery)}
}

func (c *SystemCollector) Collect(ctx context.Context) (Snapshot, error) {
	snapshot, err := c.CollectHostSnapshot(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	return c.CollectInventory(ctx, snapshot)
}

// CollectHostSnapshot is intentionally independent from access-manifest IO and
// service probes so the client can publish server liveness first.
func (c *SystemCollector) CollectHostSnapshot(ctx context.Context) (Snapshot, error) {
	hostInventory, err := collectHost(ctx, c.cfg.PublicIP)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{
		Hostname: hostInventory.Hostname,
		OS:       hostInventory.OS,
		Arch:     hostInventory.Arch,
		Host:     hostInventory,
		Services: []Service{},
	}
	return snapshot, nil
}

// CollectInventory enriches a previously collected host snapshot with the two
// halves of the service model: the managed half declared by a StackKit access
// manifest, and the unmanaged half discovered on the host itself.
//
// Discovery runs whether or not a manifest exists. Gating the whole service set
// on the manifest is what made every non-StackKit host report zero services
// while the control plane faithfully projected that empty set.
func (c *SystemCollector) CollectInventory(ctx context.Context, snapshot Snapshot) (Snapshot, error) {
	snapshot, err := c.collectManifestServices(ctx, snapshot)
	if err != nil {
		return Snapshot{}, err
	}
	return c.appendDiscoveredServices(ctx, snapshot), nil
}

// appendDiscoveredServices adds the services running on the host that no
// StackKit declared. A discovered service never displaces a manifest service:
// the manifest carries a declared contract, discovery only carries evidence.
func (c *SystemCollector) appendDiscoveredServices(ctx context.Context, snapshot Snapshot) Snapshot {
	outcome := c.discovery.discover(ctx)
	snapshot.DiscoveryObserved = outcome.Probed
	declared := make(map[string]struct{}, len(snapshot.Services))
	for _, service := range snapshot.Services {
		declared[strings.ToLower(strings.TrimSpace(service.Key))] = struct{}{}
	}
	for _, service := range outcome.Services {
		if _, alreadyDeclared := declared[strings.ToLower(strings.TrimSpace(service.Key))]; alreadyDeclared {
			continue
		}
		snapshot.Services = append(snapshot.Services, service)
		snapshot.DiscoveredServiceCount++
	}
	return snapshot
}

// collectManifestServices projects the StackKit access manifest, when one is
// present. Service probes share one bounded budget and run with bounded
// concurrency, keeping a broken endpoint set from delaying the next heartbeat
// indefinitely.
func (c *SystemCollector) collectManifestServices(ctx context.Context, snapshot Snapshot) (Snapshot, error) {
	snapshot.ManifestObserved = false
	manifest, err := readFirstAccessManifest(c.cfg.AccessManifestFiles)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return snapshot, nil
		}
		return Snapshot{}, err
	}
	snapshot.ManifestObserved = true
	snapshot.ManifestServiceCount = len(manifest.Services)
	snapshot.StackKit = strings.TrimSpace(manifest.StackKit)
	snapshot.StackKitVersion = firstNonEmpty(manifest.StackKitVersion, manifest.Version)
	snapshot.StackKitMode = strings.TrimSpace(manifest.Mode)
	snapshot.Domain = strings.TrimSpace(manifest.Domain)
	if len(manifest.Services) == 0 {
		return snapshot, nil
	}

	probeCtx, cancel := context.WithTimeout(ctx, c.cfg.ProbeBudget)
	defer cancel()
	type probeResult struct {
		service Service
		ok      bool
	}
	results := make([]probeResult, len(manifest.Services))
	jobs := make(chan int)
	workers := min(c.cfg.ProbeConcurrency, len(manifest.Services))
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			for index := range jobs {
				service, ok := c.probeService(probeCtx, manifest.Services[index], snapshot.StackKitVersion)
				results[index] = probeResult{service: service, ok: ok}
			}
		}()
	}
	for index := range manifest.Services {
		jobs <- index
	}
	close(jobs)
	group.Wait()
	for _, result := range results {
		if result.ok {
			snapshot.Services = append(snapshot.Services, result.service)
		}
	}
	return snapshot, nil
}

func collectHost(ctx context.Context, configuredPublicIP string) (Host, error) {
	info, err := host.InfoWithContext(ctx)
	if err != nil {
		return Host{}, fmt.Errorf("host info: %w", err)
	}
	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return Host{}, fmt.Errorf("memory info: %w", err)
	}
	root := "/"
	if runtime.GOOS == "windows" {
		root = filepath.VolumeName(os.Getenv("SystemRoot")) + `\`
	}
	du, err := disk.UsageWithContext(ctx, root)
	if err != nil {
		return Host{}, fmt.Errorf("disk info: %w", err)
	}
	cores, err := cpu.CountsWithContext(ctx, true)
	if err != nil {
		cores = runtime.NumCPU()
	}
	cpuPercent := 0.0
	if values, percentErr := cpu.PercentWithContext(ctx, 200*time.Millisecond, false); percentErr == nil && len(values) > 0 {
		cpuPercent = values[0]
	}
	publicIP, privateIP, localIP := observedHostIPs()
	if configured := net.ParseIP(strings.TrimSpace(configuredPublicIP)); configured != nil && configured.IsGlobalUnicast() && !configured.IsPrivate() {
		publicIP = configured.String()
	}
	return Host{
		Hostname:         strings.TrimSpace(info.Hostname),
		OS:               firstNonEmpty(info.Platform, info.OS, runtime.GOOS),
		OSVersion:        strings.TrimSpace(info.PlatformVersion),
		Arch:             runtime.GOARCH,
		PublicIP:         publicIP,
		PrivateIP:        privateIP,
		LocalIP:          localIP,
		CPUCores:         cores,
		RAMMB:            safeInt(vm.Total / (1024 * 1024)),
		DiskGB:           safeInt(du.Total / (1024 * 1024 * 1024)),
		CPUPercent:       cpuPercent,
		MemoryUsedBytes:  safeInt64(vm.Used),
		MemoryTotalBytes: safeInt64(vm.Total),
		DiskUsedBytes:    safeInt64(du.Used),
		DiskTotalBytes:   safeInt64(du.Total),
		UptimeSeconds:    float64(info.Uptime),
	}, nil
}

func observedHostIPs() (publicIP, privateIP, localIP string) {
	publicIP, privateIP, localIP = classifyPrimaryIP(routeSelectedIP())
	public, private := observedInterfaceIPs()
	publicIP = firstCandidate(publicIP, public)
	privateIP = firstCandidate(privateIP, private)
	localIP = firstNonEmpty(localIP, privateIP, publicIP)
	return publicIP, privateIP, localIP
}

func classifyPrimaryIP(primary net.IP) (publicIP, privateIP, localIP string) {
	if primary == nil {
		return "", "", ""
	}
	localIP = primary.String()
	if primary.IsPrivate() {
		return "", localIP, localIP
	}
	return localIP, "", localIP
}

func observedInterfaceIPs() (public, private []string) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, nil
	}
	for _, iface := range interfaces {
		if !isObservedInterface(iface) {
			continue
		}
		addresses, addrErr := iface.Addrs()
		if addrErr != nil {
			continue
		}
		for _, address := range addresses {
			ip := observedAddressIP(address)
			if ip == nil {
				continue
			}
			if ip.IsPrivate() {
				private = append(private, ip.String())
			} else {
				public = append(public, ip.String())
			}
		}
	}
	slices.Sort(public)
	slices.Sort(private)
	return public, private
}

func isObservedInterface(iface net.Interface) bool {
	if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
		return false
	}
	name := strings.ToLower(iface.Name)
	return !strings.HasPrefix(name, "docker") &&
		!strings.HasPrefix(name, "br-") &&
		!strings.HasPrefix(name, "veth")
}

func observedAddressIP(address net.Addr) net.IP {
	raw := address.String()
	if slash := strings.IndexByte(raw, '/'); slash >= 0 {
		raw = raw[:slash]
	}
	ip := net.ParseIP(raw)
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsLinkLocalUnicast() {
		return nil
	}
	return ip
}

func firstCandidate(current string, candidates []string) string {
	if current != "" || len(candidates) == 0 {
		return current
	}
	return candidates[0]
}

func routeSelectedIP() net.IP {
	connection, err := net.DialTimeout("udp", "1.1.1.1:53", 250*time.Millisecond)
	if err != nil {
		return nil
	}
	defer func() { _ = connection.Close() }()
	address, ok := connection.LocalAddr().(*net.UDPAddr)
	if !ok || address.IP == nil || !address.IP.IsGlobalUnicast() {
		return nil
	}
	return address.IP
}

type accessManifest struct {
	StackKit        string                  `json:"stackkit"`
	StackKitVersion string                  `json:"stackkitVersion"`
	Version         string                  `json:"version"`
	Mode            string                  `json:"mode"`
	Domain          string                  `json:"domain"`
	Services        []accessManifestService `json:"services"`
}

type accessManifestService struct {
	Key            string   `json:"key"`
	Name           string   `json:"name"`
	DisplayName    string   `json:"displayName"`
	URL            string   `json:"url"`
	DesiredState   string   `json:"desiredState"`
	AllowedActions []string `json:"allowedActions"`
	EvidenceRef    string   `json:"evidenceRef"`
}

func readFirstAccessManifest(paths []string) (accessManifest, error) {
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return accessManifest{}, fmt.Errorf("read access manifest: %w", err)
		}
		var manifest accessManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return accessManifest{}, fmt.Errorf("parse access manifest: %w", err)
		}
		return manifest, nil
	}
	return accessManifest{}, os.ErrNotExist
}

func (c *SystemCollector) probeService(ctx context.Context, observed accessManifestService, stackKitVersion string) (Service, bool) {
	rawURL := strings.TrimSpace(observed.URL)
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return Service{}, false
	}
	status := "unhealthy"
	health := map[string]any{"source": "http-probe"}
	reqCtx, cancel := context.WithTimeout(ctx, c.cfg.ProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, parsed.String(), nil)
	if err == nil {
		resp, probeErr := c.probeClient.Do(req)
		if probeErr == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
			_ = resp.Body.Close()
			health["status_code"] = resp.StatusCode
			if healthyProbeStatus(resp.StatusCode) {
				status = "healthy"
				health["healthy"] = true
			} else if reachableProbeStatus(resp.StatusCode) {
				status = "reachable"
				health["auth_or_redirect_required"] = true
			} else {
				health["healthy"] = false
			}
		} else {
			health["error_class"] = "transport"
			health["healthy"] = false
		}
	} else {
		health["healthy"] = false
	}
	health["status"] = status
	key := firstNonEmpty(observed.Key, observed.Name)
	name := firstNonEmpty(observed.DisplayName, observed.Name, key)
	endpoint := Endpoint{
		URL:        parsed.String(),
		Visibility: endpointVisibility(parsed.Hostname()),
		Provenance: "stackkit-access-manifest",
		Kind:       "web",
		Health:     status,
		TargetType: "direct",
	}
	return Service{
		ID:              key,
		ServiceID:       key,
		Key:             key,
		Name:            name,
		Status:          status,
		Source:          serviceregistry.SourceStackKitsInventory,
		URL:             parsed.String(),
		Instance:        "default",
		StackKitVersion: stackKitVersion,
		DesiredState:    strings.ToLower(strings.TrimSpace(observed.DesiredState)),
		Actions:         append([]string(nil), observed.AllowedActions...),
		EvidenceRef:     strings.TrimSpace(observed.EvidenceRef),
		Health:          health,
		Endpoints:       []Endpoint{endpoint},
	}, key != ""
}

func healthyProbeStatus(status int) bool {
	return status >= http.StatusOK && status < http.StatusMultipleChoices
}

func reachableProbeStatus(status int) bool {
	return (status >= http.StatusMultipleChoices && status < http.StatusBadRequest) || status == http.StatusUnauthorized || status == http.StatusForbidden
}

func endpointVisibility(hostname string) string {
	hostname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostname), "."))
	ip := net.ParseIP(hostname)
	if hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") || (ip != nil && (ip.IsLoopback() || ip.IsPrivate())) {
		return "local"
	}
	return "public"
}

func defaultAccessManifestFiles() []string {
	paths := []string{
		"/opt/stackkit/.stackkit/access.json",
		"/root/my-homelab/.stackkit/access.json",
		"/var/lib/stackkit/.stackkit/access.json",
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		paths = append(paths, filepath.Join(home, "my-homelab", ".stackkit", "access.json"))
	}
	return paths
}

func safeInt64(value uint64) int64 {
	const maxInt64 = uint64(^uint64(0) >> 1)
	if value > maxInt64 {
		return int64(maxInt64)
	}
	return int64(value)
}

func safeInt(value uint64) int {
	const maxInt = int(^uint(0) >> 1)
	if value > uint64(maxInt) {
		return maxInt
	}
	return int(value)
}
