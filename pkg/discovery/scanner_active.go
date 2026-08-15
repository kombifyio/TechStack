package discovery

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"
)

// ScanActive performs an active port scan on discovered or specified hosts.
// Accepts an optional onProgress callback which is invoked during the scan with
// intermediate progress updates. The callback is passed the current ScanResult snapshot.
func (s *Scanner) ScanActive(ctx context.Context, req *ScanRequest, onProgress func(*ScanResult)) (*ScanResult, error) {
	// First do passive scan to find hosts (propagate progress)
	result, err := s.ScanPassive(ctx, req, onProgress)
	if err != nil {
		return result, err
	}

	result.Profile = ProfileActive
	result.Status = ScanStatusRunning

	ports := s.config.ActivePorts
	if len(ports) == 0 {
		ports = DefaultActivePorts()
	}

	timeout := time.Duration(s.config.DefaultTimeout) * time.Second
	if req.TimeoutSeconds > 0 {
		timeout = time.Duration(req.TimeoutSeconds) * time.Second
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

	type deviceScan struct {
		idx      int
		ports    []int
		services []DiscoveredService
	}
	out := make(chan deviceScan, len(result.Devices))
	var wg sync.WaitGroup
	for i := range result.Devices {
		wg.Add(1)
		ip := result.Devices[i].IP
		go func(idx int, ip string) {
			defer wg.Done()
			p, svcs := s.scanPortsResult(ctx, ip, ports, timeout)
			out <- deviceScan{idx: idx, ports: p, services: svcs}
		}(i, ip)
	}
	go func() {
		wg.Wait()
		close(out)
	}()

	processed := 0
	for r := range out {
		resultMu.Lock()
		result.Devices[r.idx].Ports = r.ports
		result.Devices[r.idx].Services = r.services
		processed++
		if len(result.Devices) > 0 {
			result.Progress = (processed * 100) / len(result.Devices)
		}
		resultMu.Unlock()
		emitProgress()
	}

	for i := range result.Devices {
		result.Devices[i].DiscoveryProfile = ProfileActive
		result.Devices[i].RolesSuggested = SuggestRoles(&result.Devices[i])
		result.Devices[i].Confidence = CalculateConfidence(&result.Devices[i])
	}

	now := time.Now()
	result.CompletedAt = &now
	result.Progress = 100
	result.Status = ScanStatusCompleted

	emitProgress()

	return result, nil
}

// scanPortsResult performs a TCP port scan and returns discovered ports/services.
func (s *Scanner) scanPortsResult(ctx context.Context, ip string, ports []int, timeout time.Duration) ([]int, []DiscoveredService) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	foundPorts := make([]int, 0)
	foundSvcs := make([]DiscoveredService, 0)

	for _, port := range ports {
		select {
		case <-ctx.Done():
			return foundPorts, foundSvcs
		default:
		}

		wg.Add(1)
		go func(p int) {
			defer wg.Done()

			addr := net.JoinHostPort(ip, strconv.Itoa(p))
			conn, err := net.DialTimeout("tcp", addr, timeout)
			if err != nil {
				return
			}
			conn.Close()

			svc := identifyService(p)
			mu.Lock()
			foundPorts = append(foundPorts, p)
			foundSvcs = append(foundSvcs, svc)
			mu.Unlock()
		}(port)
	}

	wg.Wait()
	return foundPorts, foundSvcs
}
