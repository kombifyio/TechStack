package httpguard

// HTTP fingerprint probe: recognition layer 3.
//
// Labels, image and name (layers 1 and 2) say what a container claims to be.
// This step asks the application itself: it fetches a few pages of the
// container's own published TCP ports and hands the responses to
// pkg/appcatalog, which matches them against the generated fingerprint set. The
// same requests prove two facts the published port mapping alone cannot: which
// port really serves a web UI, and whether it speaks http or https.
//
// # Where it probes
//
// Only the host itself. A wildcard or loopback bind is dialed on 127.0.0.1, a
// specific bind on that address when it is loopback or on the local network
// (RFC 1918, CGNAT 100.64/10, IPv6 ULA, link-local), and a port bound to a
// public address is never probed. The dialer enforces this rule on every
// connection, so neither a redirect, a Host header nor a proxy setting can aim
// a request at another host or port. No request carries credentials, cookies
// or authorization, and nothing a response says is kept beyond the recognized
// application id.
//
// # Honesty rules
//
// A port is a web port only when it answered HTTP or HTTPS. A silent port, a
// refused connection and a non-HTTP protocol prove no web UI, and the address
// then falls back to publishedAddress unchanged. The application id comes only
// from a response, never from a port number.
//
// # Bounds
//
// The whole step shares one budget (default 4 s), probes at most four ports
// per container and 24 per cycle, runs four at a time and gives every request
// 1.5 s. Results are cached per container and host port so steady-state
// heartbeats send no requests; a result cut short by the budget is never cached.

import (
	"context"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kombifyio/techstack/pkg/appcatalog"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

const (
	fingerprintDefaultBudget   = 4 * time.Second
	fingerprintRequestTimeout  = 1500 * time.Millisecond
	fingerprintConcurrency     = 4
	fingerprintPortsPerService = 4
	fingerprintPortsPerCycle   = 24

	// A port that answered HTTP is remembered for fingerprintCacheTTL. A port
	// that did not is retried on a backoff that starts at fingerprintMissBackoff
	// and doubles up to the same TTL: a container that is still starting often
	// refuses or resets its first connections, and must not stay unrecognized
	// for half an hour because the probe came early.
	fingerprintCacheTTL     = 30 * time.Minute
	fingerprintMissBackoff  = time.Minute
	fingerprintMaxMissShift = 5
	fingerprintCacheLimit   = 512

	catalogCategoryDatabase = "database"
)

// probeTarget is one published TCP port of the host itself.
type probeTarget struct {
	// ip is the address to dial: loopback or on the local network.
	ip   string
	port int
}

// portProbe is what one port proved.
type portProbe struct {
	// web is true when the port answered HTTP or HTTPS.
	web bool
	// scheme is "http" or "https", set with web.
	scheme string
	// app is the application id the responses fingerprinted, or "".
	app string
}

// portProber probes one port. Production uses probeOnePort; it is a seam so a
// test can answer without a socket.
type portProber func(ctx context.Context, target probeTarget) portProbe

// fingerprintProber probes the running containers of one host and remembers
// what each port proved.
type fingerprintProber struct {
	budget time.Duration
	probe  portProber
	cache  *fingerprintCache
}

// newFingerprintProber returns nil when discovery or the probe is disabled.
func newFingerprintProber(cfg DiscoveryConfig) *fingerprintProber {
	if cfg.Disabled || cfg.DisableFingerprint {
		return nil
	}
	prober := &fingerprintProber{
		budget: cfg.FingerprintBudget, probe: cfg.probePort,
		cache: &fingerprintCache{now: time.Now, entries: map[fingerprintKey]fingerprintEntry{}},
	}
	if prober.budget <= 0 {
		prober.budget = fingerprintDefaultBudget
	}
	if prober.probe == nil {
		prober.probe = probeOnePort
	}
	return prober
}

// plannedPort is a published port with the address the probe may dial for it.
type plannedPort struct {
	port publishedPort
	ip   string
}

// probeJob is one container to probe.
type probeJob struct {
	index     int
	container string
	ports     []plannedPort
	// catalogPorts are the container ports the catalog knows the web UI on,
	// preferred first; labelPort is the one a Traefik label forwards to.
	catalogPorts []int
	labelPort    int
}

// primary reports whether a container port is where the container's own
// metadata says the web UI is.
func (j probeJob) primary(containerPort int) bool {
	return containerPort != 0 && (containerPort == j.labelPort || slices.Contains(j.catalogPorts, containerPort))
}

// observedPort is a probed port and what it proved.
type observedPort struct {
	port  publishedPort
	probe portProbe
}

// recognize probes the running containers among services and records what the
// responses prove on them in place. Cached ports send no request.
func (p *fingerprintProber) recognize(ctx context.Context, services []Service, host Host) {
	if p == nil {
		return
	}
	jobs := planFingerprintJobs(services)
	if len(jobs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, p.budget)
	defer cancel()

	observed := make([][]observedPort, len(jobs))
	var allowance atomic.Int32
	allowance.Store(fingerprintPortsPerCycle)
	queue := make(chan int)
	workers := min(fingerprintConcurrency, len(jobs))
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			for index := range queue {
				observed[index] = p.probeJob(ctx, jobs[index], &allowance)
			}
		}()
	}
	for index := range jobs {
		queue <- index
	}
	close(queue)
	group.Wait()
	for index, job := range jobs {
		applyFingerprint(&services[job.index], job, observed[index], host)
	}
}

// planFingerprintJobs selects the containers worth probing: running docker
// containers with a published TCP port. A recognized database without a web UI
// is skipped: its port speaks its own protocol and a stray HTTP request only
// fills its log.
func planFingerprintJobs(services []Service) []probeJob {
	var jobs []probeJob
	for index := range services {
		service := &services[index]
		if service.PlatformType != discoveryPlatformDocker || len(service.publishedPorts) == 0 ||
			service.Status != string(serviceregistry.ObservedRunning) {
			continue
		}
		entry := appcatalog.Match(serviceSubject(*service))
		catalogPorts := entry.WebPorts()
		if entry != nil && entry.Category == catalogCategoryDatabase && len(catalogPorts) == 0 {
			continue
		}
		labelPort, _, _ := appcatalog.LabelWebPort(service.Labels)
		ports := plannedPorts(service.publishedPorts, catalogPorts, labelPort)
		if len(ports) == 0 {
			continue
		}
		jobs = append(jobs, probeJob{
			index: index, container: firstNonEmpty(service.ContainerID, service.Key),
			ports: ports, catalogPorts: catalogPorts, labelPort: labelPort,
		})
	}
	return jobs
}

// plannedPorts orders a container's published ports by how likely they serve
// its web UI and keeps the first few that may be probed at all: the catalog's
// web ports, then the Traefik label port, then conventional web and TLS ports,
// then the rest.
func plannedPorts(published []publishedPort, catalogPorts []int, labelPort int) []plannedPort {
	type target struct {
		ip   string
		port int
	}
	seen := map[target]bool{}
	candidates := make([]plannedPort, 0, len(published))
	for _, port := range published {
		ip := probeAddress(port.bind)
		if ip == "" || seen[target{ip, port.hostPort}] {
			continue
		}
		seen[target{ip, port.hostPort}] = true
		candidates = append(candidates, plannedPort{port: port, ip: ip})
	}
	rank := func(containerPort int) int {
		switch {
		case slices.Contains(catalogPorts, containerPort):
			return slices.Index(catalogPorts, containerPort)
		case labelPort != 0 && containerPort == labelPort:
			return len(catalogPorts)
		case webSchemeForContainerPort(containerPort) != "":
			return len(catalogPorts) + 1
		}
		return len(catalogPorts) + 2
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return rank(candidates[i].port.containerPort) < rank(candidates[j].port.containerPort)
	})
	if len(candidates) > fingerprintPortsPerService {
		candidates = candidates[:fingerprintPortsPerService]
	}
	return candidates
}

// probeJob walks one container's ports in order and stops at the first port
// that proves where the application is: one that fingerprinted it, or the web
// port its own metadata names. A port the budget or the per-cycle allowance
// cut off stays unknown and is neither cached nor reported.
func (p *fingerprintProber) probeJob(ctx context.Context, job probeJob, allowance *atomic.Int32) []observedPort {
	observed := make([]observedPort, 0, len(job.ports))
	for _, planned := range job.ports {
		key := fingerprintKey{container: job.container, hostPort: planned.port.hostPort}
		result, cached := p.cache.get(key)
		if !cached {
			if ctx.Err() != nil || allowance.Add(-1) < 0 {
				break
			}
			result = p.probe(ctx, probeTarget{ip: planned.ip, port: planned.port.hostPort})
			if ctx.Err() != nil {
				break
			}
			p.cache.put(key, result)
		}
		observed = append(observed, observedPort{port: planned.port, probe: result})
		if result.web && (result.app != "" || job.primary(planned.port.containerPort)) {
			break
		}
	}
	return observed
}

// applyFingerprint records the proof: the application the best web port
// fingerprinted, and that port as the address, with the scheme it answered on.
// The host part is the address the user can reach (hostForPublishedPort), not
// the loopback address the probe dialed. Without a proven web port the service
// is left for publishedAddress.
func applyFingerprint(service *Service, job probeJob, observed []observedPort, host Host) {
	best, ok := chooseWebPort(job, observed)
	if !ok {
		return
	}
	service.Fingerprint = best.probe.app
	if address := hostForPublishedPort(best.port.bind, host); address != "" {
		service.InternalAddress = best.probe.scheme + "://" +
			net.JoinHostPort(address, strconv.Itoa(best.port.hostPort))
	}
}

// chooseWebPort picks among the proven web ports: the one that fingerprinted
// the application, then the catalog's web port, then the Traefik label port,
// then the lowest host port.
func chooseWebPort(job probeJob, observed []observedPort) (observedPort, bool) {
	var best observedPort
	bestRank, found := 0, false
	for _, candidate := range observed {
		if !candidate.probe.web {
			continue
		}
		rank := 1_000_000 + candidate.port.hostPort
		switch {
		case candidate.probe.app != "":
			rank = 0
		case slices.Contains(job.catalogPorts, candidate.port.containerPort):
			rank = 1 + slices.Index(job.catalogPorts, candidate.port.containerPort)
		case job.labelPort != 0 && candidate.port.containerPort == job.labelPort:
			rank = 1 + len(job.catalogPorts)
		}
		if !found || rank < bestRank {
			best, bestRank, found = candidate, rank, true
		}
	}
	return best, found
}

// probeAddress is the address the probe may dial for a published port's bind
// address, or "" when the port must not be probed. The host's own loopback
// reaches every wildcard bind; a specific bind is probed only when it is
// loopback or on the local network, never when it is public.
func probeAddress(bind string) string {
	bind = strings.TrimSpace(bind)
	if bind == "" {
		return "127.0.0.1"
	}
	ip := net.ParseIP(bind)
	switch {
	case ip == nil:
		return ""
	case ip.IsUnspecified():
		return "127.0.0.1"
	case probeIPAllowed(ip):
		return ip.String()
	}
	return ""
}

var carrierGradeNAT = netip.MustParsePrefix("100.64.0.0/10")

// probeIPAllowed is the address rule of the whole probe: loopback, RFC 1918,
// CGNAT (Tailscale and similar), IPv6 unique-local and link-local addresses
// are the host's own network; every other address is public.
func probeIPAllowed(ip net.IP) bool {
	// IPv4 link-local (169.254/16) is excluded: cloud metadata services live
	// there, and a container port is never published on it.
	if ip.IsLoopback() || ip.IsPrivate() || (ip.IsLinkLocalUnicast() && ip.To4() == nil) {
		return true
	}
	address, ok := netip.AddrFromSlice(ip)
	return ok && carrierGradeNAT.Contains(address.Unmap())
}

// fingerprintKey identifies one published port of one container; a recreated
// container has a new ID and is probed again.
type fingerprintKey struct {
	container string
	hostPort  int
}

type fingerprintEntry struct {
	probe   portProbe
	expires time.Time
	// misses counts consecutive probes that proved no web UI.
	misses int
}

// fingerprintCache remembers port results, bounded in size.
type fingerprintCache struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[fingerprintKey]fingerprintEntry
}

func (c *fingerprintCache) get(key fingerprintKey) (portProbe, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || !c.now().Before(entry.expires) {
		return portProbe{}, false
	}
	return entry.probe, true
}

func (c *fingerprintCache) put(key fingerprintKey, probe portProbe) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	entry := fingerprintEntry{probe: probe, expires: now.Add(fingerprintCacheTTL)}
	previous, known := c.entries[key]
	if !probe.web {
		entry.misses = previous.misses + 1
		backoff := fingerprintMissBackoff << min(entry.misses-1, fingerprintMaxMissShift)
		entry.expires = now.Add(min(backoff, fingerprintCacheTTL))
	}
	if !known && len(c.entries) >= fingerprintCacheLimit {
		c.evictLocked(now)
	}
	c.entries[key] = entry
}

// evictLocked drops expired entries and, when the cache is still full, the one
// that expires first.
func (c *fingerprintCache) evictLocked(now time.Time) {
	var oldest fingerprintKey
	var oldestExpiry time.Time
	for key, entry := range c.entries {
		if !now.Before(entry.expires) {
			delete(c.entries, key)
			continue
		}
		if oldestExpiry.IsZero() || entry.expires.Before(oldestExpiry) {
			oldest, oldestExpiry = key, entry.expires
		}
	}
	if len(c.entries) >= fingerprintCacheLimit {
		delete(c.entries, oldest)
	}
}
