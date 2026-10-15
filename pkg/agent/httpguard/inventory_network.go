package httpguard

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Network is the host's observed network position: the facts a home network
// move changes. Everything is best effort and Linux only; a field the host
// cannot answer stays empty and never fails the inventory.
type Network struct {
	// Interface is the default-route interface.
	Interface string `json:"interface,omitempty"`
	// Gateway is the default IPv4 gateway.
	Gateway string `json:"gateway,omitempty"`
	// CIDR is the subnet of the route-selected address, for example
	// 192.168.1.0/24.
	CIDR     string `json:"cidr,omitempty"`
	Wireless bool   `json:"wireless,omitempty"`
	// SSID is the connected WiFi network of a wireless interface.
	SSID string `json:"ssid,omitempty"`
	// Fingerprint identifies the network, not the host: gateway, subnet and SSID.
	Fingerprint string `json:"fingerprint,omitempty"`
}

const networkProbeTimeout = 1500 * time.Millisecond

// NetworkFingerprint is the stable identity of a network: the same gateway,
// subnet and SSID give the same value on every host, and any of them changing
// gives a different one. It is truncated; it identifies, it does not secure.
func NetworkFingerprint(gateway, cidr, ssid string) string {
	gateway, cidr, ssid = strings.TrimSpace(gateway), strings.TrimSpace(cidr), strings.TrimSpace(ssid)
	if gateway == "" && cidr == "" && ssid == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(gateway + "\x00" + cidr + "\x00" + ssid))
	return hex.EncodeToString(sum[:])[:16]
}

// collectNetwork observes the network position behind localIP. It returns nil
// where nothing could be observed (non-Linux hosts, no default route).
func collectNetwork(ctx context.Context, localIP string) *Network {
	if runtime.GOOS != "linux" {
		return nil
	}
	iface, gateway := defaultRoute("/proc/net/route")
	network := &Network{Interface: iface, Gateway: gateway}
	if ip := net.ParseIP(strings.TrimSpace(localIP)); ip != nil {
		network.CIDR = subnetOf(ip, iface)
	}
	if iface != "" {
		if _, err := os.Stat(filepath.Join("/sys/class/net", filepath.Base(iface), "wireless")); err == nil {
			network.Wireless = true
			network.SSID = connectedSSID(ctx)
		}
	}
	network.Fingerprint = NetworkFingerprint(network.Gateway, network.CIDR, network.SSID)
	if network.Interface == "" && network.Gateway == "" && network.CIDR == "" && network.SSID == "" {
		return nil
	}
	return network
}

// defaultRoute reads the lowest-metric default IPv4 route from a /proc/net/route
// formatted file.
func defaultRoute(path string) (iface, gateway string) {
	file, err := os.Open(path) // #nosec G304 -- fixed procfs path, or a test fixture.
	if err != nil {
		return "", ""
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	scanner.Scan() // header
	bestMetric := -1
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 8 || fields[1] != "00000000" || fields[7] != "00000000" {
			continue
		}
		flags, ok := parseHexUint32(fields[3])
		if !ok || flags&0x2 == 0 { // RTF_GATEWAY
			continue
		}
		metric, metricErr := strconv.Atoi(fields[6]) // decimal in /proc/net/route
		if metricErr != nil {
			metric = 0
		}
		if bestMetric >= 0 && metric >= bestMetric {
			continue
		}
		raw, ok := parseHexUint32(fields[2])
		if !ok || raw == 0 {
			continue
		}
		var octets [4]byte
		binary.LittleEndian.PutUint32(octets[:], raw)
		bestMetric, iface, gateway = metric, fields[0], net.IP(octets[:]).String()
	}
	return iface, gateway
}

func parseHexUint32(value string) (uint32, bool) {
	if len(value) == 0 || len(value) > 8 {
		return 0, false
	}
	var out uint32
	for _, r := range value {
		var digit uint32
		switch {
		case r >= '0' && r <= '9':
			digit = uint32(r - '0')
		case r >= 'a' && r <= 'f':
			digit = uint32(r-'a') + 10
		case r >= 'A' && r <= 'F':
			digit = uint32(r-'A') + 10
		default:
			return 0, false
		}
		out = out<<4 | digit
	}
	return out, true
}

// subnetOf returns the subnet of ip, preferring the interface that carries the
// default route and falling back to any interface that holds the address.
func subnetOf(ip net.IP, preferred string) string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	fallback := ""
	for _, candidate := range interfaces {
		addresses, addrErr := candidate.Addrs()
		if addrErr != nil {
			continue
		}
		for _, address := range addresses {
			network, ok := address.(*net.IPNet)
			if !ok || !network.IP.Equal(ip) {
				continue
			}
			masked := &net.IPNet{IP: network.IP.Mask(network.Mask), Mask: network.Mask}
			if candidate.Name == preferred {
				return masked.String()
			}
			if fallback == "" {
				fallback = masked.String()
			}
		}
	}
	return fallback
}

// connectedSSID asks the host's WiFi tooling for the connected network. A host
// without iwgetid or nmcli reports none; failure is never an inventory error.
func connectedSSID(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, networkProbeTimeout)
	defer cancel()
	if path, err := exec.LookPath("iwgetid"); err == nil {
		out, runErr := exec.CommandContext(ctx, path, "-r").Output() // #nosec G204 -- fixed binary and argv.
		if runErr == nil {
			if ssid := cleanSSID(string(out)); ssid != "" {
				return ssid
			}
		}
	}
	if path, err := exec.LookPath("nmcli"); err == nil {
		out, runErr := exec.CommandContext(ctx, path, "-t", "-f", "active,ssid", "dev", "wifi").Output() // #nosec G204 -- fixed binary and argv.
		if runErr == nil {
			return activeSSIDFromNmcli(string(out))
		}
	}
	return ""
}

// activeSSIDFromNmcli reads the terse `active,ssid` listing: the line that
// starts with "yes:". nmcli escapes ':' and '\' inside a field with a backslash.
func activeSSIDFromNmcli(output string) string {
	for _, line := range strings.Split(output, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "yes:")
		if !ok {
			continue
		}
		rest = strings.NewReplacer(`\:`, ":", `\\`, `\`).Replace(rest)
		if ssid := cleanSSID(rest); ssid != "" {
			return ssid
		}
	}
	return ""
}

// cleanSSID bounds an SSID to the 802.11 limit and drops control characters.
func cleanSSID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 32 {
		return ""
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return value
}
