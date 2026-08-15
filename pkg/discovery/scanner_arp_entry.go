package discovery

import (
	"context"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// getARPEntry retrieves the MAC address for an IP from the ARP cache.
func (s *Scanner) getARPEntry(ip string) string {
	var cmd *exec.Cmd
	var output []byte
	var err error
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	switch runtime.GOOS {
	case "windows":
		cmd = exec.CommandContext(ctx, "arp", "-a", ip)
		output, err = cmd.Output()
		if err != nil {
			return ""
		}
		return parseWindowsARP(string(output), ip)
	default: // Linux/Darwin
		cmd = exec.CommandContext(ctx, "arp", "-n", ip)
		output, err = cmd.Output()
		if err != nil {
			return ""
		}
		return parseUnixARP(string(output), ip)
	}
}

// parseWindowsARP extracts MAC from Windows arp -a output.
func parseWindowsARP(output, ip string) string {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, ip) {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				mac := fields[1]
				// Windows uses - as separator, normalize to :
				mac = strings.ReplaceAll(mac, "-", ":")
				if _, err := net.ParseMAC(mac); err == nil {
					return mac
				}
			}
		}
	}
	return ""
}

// parseUnixARP extracts MAC from Unix arp -n output.
func parseUnixARP(output, ip string) string {
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, ip) {
			fields := strings.Fields(line)
			for _, field := range fields {
				if _, err := net.ParseMAC(field); err == nil {
					return field
				}
			}
		}
	}
	return ""
}
