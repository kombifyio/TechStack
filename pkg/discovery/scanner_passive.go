package discovery

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
)

type passiveMDNSEvent struct {
	ip       string
	hostname string
}

// ScanPassive performs a passive network scan using ARP and ping.
// Accepts an optional onProgress callback which is invoked during the scan with
// intermediate progress updates (useful for streaming progress to callers).
func (s *Scanner) ScanPassive(ctx context.Context, req *ScanRequest, onProgress func(*ScanResult)) (*ScanResult, error) {
	result := &ScanResult{
		ScanID:    generateScanID(),
		Status:    ScanStatusRunning,
		Profile:   ProfilePassive,
		Subnet:    req.Subnet,
		StartedAt: time.Now(),
		Devices:   make([]DiscoveredDevice, 0),
	}

	var resultMu sync.Mutex
	emitProgress := func() {
		if onProgress == nil {
			return
		}
		resultMu.Lock()
		snap := cloneScanResultLocked(result)
		resultMu.Unlock()
		onProgress(snap)
	}

	hosts, err := s.resolvePassiveScanHosts(req, result, &resultMu)
	if err != nil {
		return result, err
	}
	excludeSet := s.passiveScanExclusions(req)

	resultMu.Lock()
	result.TotalHosts = len(hosts)
	resultMu.Unlock()

	// If mDNS is enabled, start a passive listener that will inject discovered hosts.
	seenIPs := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		seenIPs[h] = true
	}
	mdnsEvents := make(chan passiveMDNSEvent, 128)
	var mdnsCancel context.CancelFunc
	if req.Mdns {
		mdnsCtx, cancel := context.WithCancel(ctx)
		mdnsCancel = cancel
		go func() {
			err := ListenMDNS(mdnsCtx, func(e *zeroconf.ServiceEntry) {
				if e == nil {
					return
				}
				for _, ip := range e.AddrIPv4 {
					select {
					case <-mdnsCtx.Done():
						return
					default:
					}
					select {
					case mdnsEvents <- passiveMDNSEvent{ip: ip.String(), hostname: e.Instance}:
					default:
						// drop if caller is behind
					}
				}
			})
			if err != nil {
				resultMu.Lock()
				result.Errors = append(result.Errors, "mdns: "+err.Error())
				resultMu.Unlock()
				emitProgress()
			}
		}()
		defer func() {
			if mdnsCancel != nil {
				mdnsCancel()
			}
		}()
	}

	concurrency, timeout := s.passiveScanLimits(req, result, &resultMu)
	deviceChan, probeErr := s.startPassiveHostProbes(ctx, hosts, excludeSet, concurrency, timeout, result)
	if probeErr != nil {
		return result, probeErr
	}
	if collectErr := collectPassiveScanResults(ctx, req.Mdns, result, &resultMu, emitProgress, deviceChan, mdnsEvents, mdnsCancel, excludeSet, seenIPs); collectErr != nil {
		return result, collectErr
	}

	// ARP enrichment: try to fill missing MAC addresses from OS ARP cache
	if arpCache, err := GetARPCache(); err == nil {
		for i := range result.Devices {
			if result.Devices[i].MAC == "" {
				if mac, ok := arpCache[result.Devices[i].IP]; ok {
					result.Devices[i].MAC = mac
					result.Devices[i].DeviceID = GenerateDeviceID(mac, result.Devices[i].IP)
				}
			}
		}
		emitProgress()
	}

	now := time.Now()
	resultMu.Lock()
	result.CompletedAt = &now
	result.Status = ScanStatusCompleted
	resultMu.Unlock()

	emitProgress()

	return result, nil
}

func (s *Scanner) resolvePassiveScanHosts(req *ScanRequest, result *ScanResult, resultMu *sync.Mutex) ([]string, error) {
	subnet := req.Subnet
	if subnet == "" {
		subnets, err := s.GetLocalSubnets()
		if err != nil {
			result.Status = ScanStatusFailed
			result.Errors = append(result.Errors, err.Error())
			return nil, err
		}
		if len(subnets) > 0 {
			subnet = subnets[0]
		}
	}
	if subnet == "" {
		result.Status = ScanStatusFailed
		result.Errors = append(result.Errors, "no subnet to scan")
		return nil, fmt.Errorf("no subnet to scan")
	}
	result.Subnet = subnet
	hosts, err := s.passiveScanSubnetHosts(req, subnet, result, resultMu)
	if err != nil {
		return nil, err
	}
	if req.DetectTailscale {
		hosts = s.appendTailscaleHosts(hosts)
	}
	return hosts, nil
}

func (s *Scanner) passiveScanSubnetHosts(req *ScanRequest, subnet string, result *ScanResult, resultMu *sync.Mutex) ([]string, error) {
	limit := s.config.SampleLimit
	if limit <= 0 {
		limit = 4096
	}
	if len(req.TargetIPs) > 0 {
		if len(req.TargetIPs) <= limit {
			return req.TargetIPs, nil
		}
		resultMu.Lock()
		result.Errors = append(result.Errors, fmt.Sprintf("target_ips truncated: %d -> %d", len(req.TargetIPs), limit))
		resultMu.Unlock()
		return req.TargetIPs[:limit], nil
	}
	total, err := estimateIPv4HostCount(subnet)
	if err != nil {
		markPassiveScanFailed(result, resultMu, err)
		return nil, err
	}
	if total <= limit {
		hosts, expandErr := expandSubnet(subnet)
		if expandErr != nil {
			markPassiveScanFailed(result, resultMu, expandErr)
		}
		return hosts, expandErr
	}
	hosts, sampleErr := sampleIPv4Hosts(subnet, limit)
	if sampleErr != nil {
		markPassiveScanFailed(result, resultMu, sampleErr)
		return nil, sampleErr
	}
	resultMu.Lock()
	result.Errors = append(result.Errors, fmt.Sprintf("sampled subnet: scanning %d of %d hosts", len(hosts), total))
	resultMu.Unlock()
	return hosts, nil
}

func markPassiveScanFailed(result *ScanResult, resultMu *sync.Mutex, err error) {
	resultMu.Lock()
	result.Status = ScanStatusFailed
	result.Errors = append(result.Errors, err.Error())
	resultMu.Unlock()
}

func (s *Scanner) appendTailscaleHosts(hosts []string) []string {
	interfaces, _ := s.GetLocalInterfaces()
	for _, iface := range interfaces {
		if !strings.Contains(strings.ToLower(iface.Name), "tailscale") {
			continue
		}
		for _, subnet := range iface.Subnets {
			more, err := expandSubnet(subnet)
			if err != nil {
				continue
			}
			for _, host := range more {
				if !containsString(hosts, host) {
					hosts = append(hosts, host)
				}
			}
		}
	}
	return hosts
}

func (s *Scanner) passiveScanExclusions(req *ScanRequest) map[string]bool {
	excluded := make(map[string]bool)
	for _, ip := range req.ExcludeIPs {
		excluded[ip] = true
	}
	if s.config.ExcludeLocalIPs {
		localIPs, _ := s.GetLocalIPs()
		for _, ip := range localIPs {
			excluded[ip] = true
		}
	}
	return excluded
}

func (s *Scanner) passiveScanLimits(req *ScanRequest, result *ScanResult, resultMu *sync.Mutex) (int, time.Duration) {
	concurrency := s.config.MaxConcurrent
	if concurrency <= 0 {
		concurrency = 50
	}
	if req.MaxConcurrent > 0 {
		if req.MaxConcurrent > concurrency {
			resultMu.Lock()
			result.Errors = append(result.Errors, fmt.Sprintf("max_concurrent clamped: %d -> %d", req.MaxConcurrent, concurrency))
			resultMu.Unlock()
		} else {
			concurrency = req.MaxConcurrent
		}
	}
	timeout := time.Duration(s.config.DefaultTimeout) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	if req.TimeoutSeconds <= 0 {
		return concurrency, timeout
	}
	requested := time.Duration(req.TimeoutSeconds) * time.Second
	if requested <= 60*time.Second {
		return concurrency, requested
	}
	resultMu.Lock()
	result.Errors = append(result.Errors, fmt.Sprintf("timeout_seconds clamped: %ds -> 60s", req.TimeoutSeconds))
	resultMu.Unlock()
	return concurrency, 60 * time.Second
}

func (s *Scanner) startPassiveHostProbes(
	ctx context.Context,
	hosts []string,
	excluded map[string]bool,
	concurrency int,
	timeout time.Duration,
	result *ScanResult,
) (<-chan *DiscoveredDevice, error) {
	var wg sync.WaitGroup
	devices := make(chan *DiscoveredDevice, len(hosts))
	semaphore := make(chan struct{}, concurrency)
	for _, host := range hosts {
		if excluded[host] {
			continue
		}
		select {
		case <-ctx.Done():
			result.Status = ScanStatusCancelled
			return nil, ctx.Err()
		default:
		}
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			if device := s.probeHost(ctx, ip, timeout); device != nil {
				devices <- device
			}
		}(host)
	}
	go func() {
		wg.Wait()
		close(devices)
	}()
	return devices, nil
}

func collectPassiveScanResults(
	ctx context.Context,
	mdnsEnabled bool,
	result *ScanResult,
	resultMu *sync.Mutex,
	emitProgress func(),
	devices <-chan *DiscoveredDevice,
	mdnsEvents <-chan passiveMDNSEvent,
	mdnsCancel context.CancelFunc,
	excluded map[string]bool,
	seenIPs map[string]bool,
) error {
	hostCh := devices
	mdnsCh := (<-chan passiveMDNSEvent)(nil)
	if mdnsEnabled {
		mdnsCh = mdnsEvents
	}
	var mdnsDrainTimer *time.Timer
	mdnsAdded := 0
	const mdnsMax = 512
	for hostCh != nil || mdnsCh != nil {
		select {
		case device, ok := <-hostCh:
			if !ok {
				hostCh = nil
				if mdnsCancel != nil {
					mdnsCancel()
					mdnsCancel = nil
					mdnsDrainTimer = time.NewTimer(250 * time.Millisecond)
				}
				continue
			}
			appendPassiveProbeResult(result, resultMu, *device)
			emitProgress()
		case event := <-mdnsCh:
			if mdnsAdded >= mdnsMax || event.ip == "" || excluded[event.ip] || seenIPs[event.ip] {
				continue
			}
			seenIPs[event.ip] = true
			mdnsAdded++
			appendPassiveMDNSResult(result, resultMu, event)
			emitProgress()
		case <-ctx.Done():
			resultMu.Lock()
			result.Status = ScanStatusCancelled
			resultMu.Unlock()
			return ctx.Err()
		case <-passiveMDNSDrainChannel(mdnsDrainTimer):
			if mdnsDrainTimer != nil {
				mdnsDrainTimer.Stop()
				mdnsDrainTimer = nil
			}
			mdnsCh = nil
		}
	}
	return nil
}

func appendPassiveProbeResult(result *ScanResult, resultMu *sync.Mutex, device DiscoveredDevice) {
	resultMu.Lock()
	result.Devices = append(result.Devices, device)
	result.ScannedHosts++
	if result.TotalHosts > 0 {
		result.Progress = (result.ScannedHosts * 100) / result.TotalHosts
	}
	resultMu.Unlock()
}

func appendPassiveMDNSResult(result *ScanResult, resultMu *sync.Mutex, event passiveMDNSEvent) {
	device := DiscoveredDevice{
		DeviceID:         GenerateDeviceID("", event.ip),
		IP:               event.ip,
		Hostname:         event.hostname,
		DiscoveryProfile: ProfilePassive,
		ProbeStatus:      ProbeStatusPending,
		FirstSeen:        time.Now(),
		LastSeen:         time.Now(),
		Confidence:       0.5,
	}
	resultMu.Lock()
	result.Devices = append(result.Devices, device)
	result.TotalHosts++
	if result.TotalHosts > 0 {
		result.Progress = (result.ScannedHosts * 100) / result.TotalHosts
	}
	resultMu.Unlock()
}

func passiveMDNSDrainChannel(timer *time.Timer) <-chan time.Time {
	if timer == nil {
		return nil
	}
	return timer.C
}

var _ = net.LookupAddr
