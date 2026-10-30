package discovery

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/fulllocal"
)

func (s *Scanner) probeHost(ctx context.Context, ip string, timeout time.Duration) *DiscoveredDevice {
	ip, err := fulllocal.AdmitIP(ip)
	if err != nil {
		return nil
	}
	// Skip probing hosts that are Docker veth bridge or common docker IPs (heuristic to reduce noise)
	if strings.HasPrefix(ip, "172.") && strings.Contains(ip, ".") && s.config.ExcludeLocalIPs {
		// common docker network starts with 172.*, but we still allow scanning if user specified subnet explicitly
	}

	if !s.ping(ctx, ip, timeout) {
		return nil
	}

	device := &DiscoveredDevice{
		IP:               ip,
		DeviceID:         GenerateDeviceID("", ip),
		DiscoveryProfile: ProfilePassive,
		ProbeStatus:      ProbeStatusPending,
		FirstSeen:        time.Now(),
		LastSeen:         time.Now(),
	}

	// Try to resolve hostname
	names, err := net.DefaultResolver.LookupAddr(ctx, ip)
	if err == nil && len(names) > 0 {
		device.Hostname = strings.TrimSuffix(names[0], ".")
	}

	// Try to get MAC address from ARP cache
	device.MAC = s.getARPEntry(ip)
	if device.MAC != "" {
		device.DeviceID = GenerateDeviceID(device.MAC, ip)
	}

	return device
}

// ping checks if a host is reachable.
func (s *Scanner) ping(ctx context.Context, ip string, timeout time.Duration) bool {
	ip, err := fulllocal.AdmitIP(ip)
	if err != nil {
		return false
	}
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "windows":
		cmd = exec.CommandContext(ctx, "ping", "-n", "1", "-w", fmt.Sprintf("%d", timeout.Milliseconds()), ip)
	case "darwin":
		cmd = exec.CommandContext(ctx, "ping", "-c", "1", "-W", fmt.Sprintf("%d", int(timeout.Seconds()*1000)), ip)
	default: // Linux
		cmd = exec.CommandContext(ctx, "ping", "-c", "1", "-W", fmt.Sprintf("%d", int(timeout.Seconds())), ip)
	}

	err = cmd.Run()
	return err == nil
}
