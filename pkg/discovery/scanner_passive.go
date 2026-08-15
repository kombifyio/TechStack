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

	// Determine subnet to scan
	subnet := req.Subnet
	if subnet == "" {
		subnets, err := s.GetLocalSubnets()
		if err != nil {
			result.Status = ScanStatusFailed
			result.Errors = append(result.Errors, err.Error())
			return result, err
		}
		if len(subnets) > 0 {
			subnet = subnets[0]
		}
	}

	if subnet == "" {
		result.Status = ScanStatusFailed
		result.Errors = append(result.Errors, "no subnet to scan")
		return result, fmt.Errorf("no subnet to scan")
	}

	result.Subnet = subnet

	// Resolve host list without fully expanding huge CIDRs.
	limit := s.config.SampleLimit
	if limit <= 0 {
		// sensible safety default if misconfigured
		limit = 4096
	}
	var hosts []string
	if len(req.TargetIPs) > 0 {
		if len(req.TargetIPs) > limit {
			resultMu.Lock()
			result.Errors = append(result.Errors, fmt.Sprintf("target_ips truncated: %d -> %d", len(req.TargetIPs), limit))
			resultMu.Unlock()
			hosts = req.TargetIPs[:limit]
		} else {
			hosts = req.TargetIPs
		}
	} else {
		total, err := estimateIPv4HostCount(subnet)
		if err != nil {
			resultMu.Lock()
			result.Status = ScanStatusFailed
			result.Errors = append(result.Errors, err.Error())
			resultMu.Unlock()
			return result, err
		}
		if total > limit {
			hosts, err = sampleIPv4Hosts(subnet, limit)
			if err != nil {
				resultMu.Lock()
				result.Status = ScanStatusFailed
				result.Errors = append(result.Errors, err.Error())
				resultMu.Unlock()
				return result, err
			}
			resultMu.Lock()
			result.Errors = append(result.Errors, fmt.Sprintf("sampled subnet: scanning %d of %d hosts", len(hosts), total))
			resultMu.Unlock()
		} else {
			hosts, err = expandSubnet(subnet)
			if err != nil {
				resultMu.Lock()
				result.Status = ScanStatusFailed
				result.Errors = append(result.Errors, err.Error())
				resultMu.Unlock()
				return result, err
			}
		}
	}

	// If detect_tailscale is requested, try to include Tailscale interface subnets
	if req.DetectTailscale {
		ifs, _ := s.GetLocalInterfaces()
		for _, iface := range ifs {
			if strings.Contains(strings.ToLower(iface.Name), "tailscale") {
				for _, sn := range iface.Subnets {
					more, e := expandSubnet(sn)
					if e == nil {
						for _, h := range more {
							if !containsString(hosts, h) {
								hosts = append(hosts, h)
							}
						}
					}
				}
			}
		}
	}

	// Exclude IPs
	excludeSet := make(map[string]bool)
	for _, ip := range req.ExcludeIPs {
		excludeSet[ip] = true
	}

	// Exclude local IPs if configured
	if s.config.ExcludeLocalIPs {
		localIPs, _ := s.GetLocalIPs()
		for _, ip := range localIPs {
			excludeSet[ip] = true
		}
	}

	resultMu.Lock()
	result.TotalHosts = len(hosts)
	resultMu.Unlock()

	// If mDNS is enabled, start a passive listener that will inject discovered hosts.
	seenIPs := make(map[string]bool, len(hosts))
	for _, h := range hosts {
		seenIPs[h] = true
	}
	type mdnsEvent struct {
		ip       string
		hostname string
	}
	mdnsEvents := make(chan mdnsEvent, 128)
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
					case mdnsEvents <- mdnsEvent{ip: ip.String(), hostname: e.Instance}:
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

	// Concurrent scanning (clamped to configured maximum)
	concurrency := s.config.MaxConcurrent
	if concurrency <= 0 {
		concurrency = 50
	}
	if req.MaxConcurrent > 0 {
		if req.MaxConcurrent < 1 {
			concurrency = 1
		} else if req.MaxConcurrent > concurrency {
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
	if req.TimeoutSeconds > 0 {
		requested := time.Duration(req.TimeoutSeconds) * time.Second
		if requested > 60*time.Second {
			resultMu.Lock()
			result.Errors = append(result.Errors, fmt.Sprintf("timeout_seconds clamped: %ds -> 60s", req.TimeoutSeconds))
			resultMu.Unlock()
			timeout = 60 * time.Second
		} else {
			timeout = requested
		}
	}

	var wg sync.WaitGroup
	deviceChan := make(chan *DiscoveredDevice, len(hosts))
	semaphore := make(chan struct{}, concurrency)

	for _, host := range hosts {
		if excludeSet[host] {
			continue
		}

		select {
		case <-ctx.Done():
			result.Status = ScanStatusCancelled
			return result, ctx.Err()
		default:
		}

		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			device := s.probeHost(ctx, ip, timeout)
			if device != nil {
				deviceChan <- device
			}
		}(host)
	}

	go func() {
		wg.Wait()
		close(deviceChan)
	}()

	hostCh := deviceChan
	mdnsCh := (<-chan mdnsEvent)(nil)
	if req.Mdns {
		mdnsCh = mdnsEvents
	}
	var mdnsDrainTimer *time.Timer
	mdnsAdded := 0
	mdnsMax := 512
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
			resultMu.Lock()
			result.Devices = append(result.Devices, *device)
			result.ScannedHosts++
			if result.TotalHosts > 0 {
				result.Progress = (result.ScannedHosts * 100) / result.TotalHosts
			}
			resultMu.Unlock()
			emitProgress()

		case ev := <-mdnsCh:
			if mdnsAdded >= mdnsMax {
				continue
			}
			if ev.ip == "" {
				continue
			}
			if excludeSet[ev.ip] || seenIPs[ev.ip] {
				continue
			}
			seenIPs[ev.ip] = true
			mdnsAdded++
			dev := DiscoveredDevice{
				DeviceID:         GenerateDeviceID("", ev.ip),
				IP:               ev.ip,
				Hostname:         ev.hostname,
				DiscoveryProfile: ProfilePassive,
				ProbeStatus:      ProbeStatusPending,
				FirstSeen:        time.Now(),
				LastSeen:         time.Now(),
				Confidence:       0.5,
			}
			resultMu.Lock()
			result.Devices = append(result.Devices, dev)
			result.TotalHosts++
			if result.TotalHosts > 0 {
				result.Progress = (result.ScannedHosts * 100) / result.TotalHosts
			}
			resultMu.Unlock()
			emitProgress()

		case <-ctx.Done():
			resultMu.Lock()
			result.Status = ScanStatusCancelled
			resultMu.Unlock()
			return result, ctx.Err()

		case <-func() <-chan time.Time {
			if mdnsDrainTimer == nil {
				return nil
			}
			return mdnsDrainTimer.C
		}():
			if mdnsDrainTimer != nil {
				mdnsDrainTimer.Stop()
				mdnsDrainTimer = nil
			}
			mdnsCh = nil
		}
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

var _ = net.LookupAddr
