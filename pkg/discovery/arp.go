package discovery

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// GetARPCache returns a map of IP -> MAC learned from the OS ARP cache.
func GetARPCache() (map[string]string, error) {
	if runtime.GOOS == "windows" {
		return parseWindowsARPCache()
	}
	// Prefer /proc/net/arp on Linux/Unix
	if _, err := os.Stat("/proc/net/arp"); err == nil {
		return parseProcARP()
	}
	// Fallback to arp -n
	return parseArpCommand()
}

func parseProcARP() (map[string]string, error) {
	f, err := os.Open("/proc/net/arp")
	if err != nil {
		return nil, fmt.Errorf("open /proc/net/arp: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	res := make(map[string]string)
	// skip header
	if !scanner.Scan() {
		return res, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) >= 4 {
			ip := fields[0]
			mac := fields[3]
			if _, err := net.ParseMAC(mac); err == nil {
				res[ip] = mac
			}
		}
	}
	return res, nil
}

func parseArpCommand() (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "arp", "-n")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("arp -n failed: %w", err)
	}
	lines := strings.Split(string(out), "\n")
	res := make(map[string]string)
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		// heuristics: first token is often IP
		ip := fields[0]
		for _, f := range fields {
			mac := strings.ReplaceAll(f, "-", ":")
			if _, err := net.ParseMAC(mac); err == nil {
				res[ip] = mac
				break
			}
		}
	}
	return res, nil
}

func parseWindowsARPCache() (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "arp", "-a")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("arp -a failed: %w", err)
	}
	// parseWindowsARP expects a single ip; reuse parse function to extract all MACs
	lines := bytes.Split(out, []byte("\n"))
	res := make(map[string]string)
	for _, l := range lines {
		line := string(l)
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			ip := fields[0]
			mac := strings.ReplaceAll(fields[1], "-", ":")
			if _, err := net.ParseMAC(mac); err == nil {
				res[ip] = mac
			}
		}
	}
	return res, nil
}
