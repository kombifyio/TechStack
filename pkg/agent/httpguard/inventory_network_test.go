package httpguard

import (
	"os"
	"path/filepath"
	"testing"
)

// The fingerprint identifies the network, not the host: it changes with any of
// gateway, subnet or SSID and with nothing else.
func TestNetworkFingerprintChangesWithTheNetwork(t *testing.T) {
	home := NetworkFingerprint("192.168.1.1", "192.168.1.0/24", "Home")
	if home != NetworkFingerprint(" 192.168.1.1", "192.168.1.0/24 ", "Home") {
		t.Fatal("fingerprint must be stable for the same network")
	}
	for name, other := range map[string]string{
		"gateway": NetworkFingerprint("192.168.1.254", "192.168.1.0/24", "Home"),
		"subnet":  NetworkFingerprint("192.168.1.1", "10.0.0.0/24", "Home"),
		"ssid":    NetworkFingerprint("192.168.1.1", "192.168.1.0/24", "Office"),
	} {
		if other == home {
			t.Fatalf("a different %s must give a different fingerprint", name)
		}
	}
}

func TestDefaultRoutePicksTheLowestMetricGateway(t *testing.T) {
	table := "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n" +
		"wlan0\t00000000\t0100A8C0\t0003\t0\t0\t600\t00000000\t0\t0\t0\n" +
		"eth0\t00000000\tFE01A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n" +
		"eth0\t0001A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n"
	path := filepath.Join(t.TempDir(), "route")
	if err := os.WriteFile(path, []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	iface, gateway := defaultRoute(path)
	if iface != "eth0" || gateway != "192.168.1.254" {
		t.Fatalf("default route = %s via %s, want eth0 via 192.168.1.254", iface, gateway)
	}
}
