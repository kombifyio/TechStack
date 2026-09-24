package devicereadiness_test

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/devicereadiness"
)

// The egress is the one place where a repair hands a device a route it did not
// have. What it may carry is therefore a security boundary, and these tests
// hold it: the allowlist decides, a redirect does not get to escape it, and a
// tunnel is not a general-purpose relay.

// serveProxy runs the egress handler against a local connection, standing in
// for the listener the device would normally own. It returns the proxy's first
// response line.
func serveProxy(t *testing.T, policy devicereadiness.EgressPolicy, request string) string {
	t.Helper()

	device, proxy := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		devicereadiness.EgressProxy{Policy: policy}.ServeConn(proxy)
	}()

	go func() {
		_, _ = io.WriteString(device, request)
	}()

	reader := bufio.NewReader(device)
	line, err := reader.ReadString('\n')
	_ = device.Close()
	<-done
	if err != nil && line == "" {
		t.Fatalf("the proxy answered nothing: %v", err)
	}
	return strings.TrimSpace(line)
}

func TestEgressRefusesAHostTheAllowlistDoesNotName(t *testing.T) {
	policy := devicereadiness.EgressPolicy{AllowedHosts: []string{"archive.ubuntu.com"}}

	status := serveProxy(t, policy, "CONNECT evil.example.com:443 HTTP/1.1\r\nHost: evil.example.com:443\r\n\r\n")

	if !strings.Contains(status, "403") {
		t.Fatalf("proxy answered %q for a host outside the allowlist, want a refusal", status)
	}
}

func TestEgressRefusesATunnelToAnArbitraryPort(t *testing.T) {
	// An allowed hostname on an arbitrary port is a general-purpose relay,
	// which is not what fetching a package needs.
	policy := devicereadiness.EgressPolicy{AllowedHosts: []string{"archive.ubuntu.com"}}

	status := serveProxy(t, policy, "CONNECT archive.ubuntu.com:22 HTTP/1.1\r\nHost: archive.ubuntu.com:22\r\n\r\n")

	if !strings.Contains(status, "403") {
		t.Fatalf("proxy answered %q for a tunnel to a non-HTTPS port, want a refusal", status)
	}
}

func TestEgressCarriesAnAllowedHost(t *testing.T) {
	// The upstream is a local listener standing in for the mirror, so the test
	// proves the allowlist admits rather than that the internet works.
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot start a stand-in upstream: %v", err)
	}
	defer func() { _ = upstream.Close() }()
	go func() {
		conn, acceptErr := upstream.Accept()
		if acceptErr != nil {
			return
		}
		_, _ = io.WriteString(conn, "hello")
		_ = conn.Close()
	}()

	host, port, _ := net.SplitHostPort(upstream.Addr().String())
	tunnelPort, _ := strconv.Atoi(port)
	policy := devicereadiness.EgressPolicy{AllowedHosts: []string{host}, AllowedTunnelPorts: []int{tunnelPort}}

	status := serveProxy(t, policy, fmt.Sprintf("CONNECT %s:%s HTTP/1.1\r\nHost: %s:%s\r\n\r\n", host, port, host, port))

	if !strings.Contains(status, fmt.Sprint(http.StatusOK)) {
		t.Fatalf("proxy answered %q for an allowed host, want the tunnel to open", status)
	}
}

func TestDefaultPolicyIncludesTheControlPlaneAndNothingInvented(t *testing.T) {
	policy := devicereadiness.DefaultEgressPolicy("techstack.example.com:443")

	var foundControlPlane bool
	for _, host := range policy.AllowedHosts {
		if host == "techstack.example.com" {
			foundControlPlane = true
		}
		if strings.ContainsAny(host, "*?") {
			t.Fatalf("the default policy carries a pattern (%q); a pattern is how an allowlist stops being one", host)
		}
	}
	if !foundControlPlane {
		t.Fatal("the default policy does not admit the control plane, so a repaired device still could not enroll")
	}
}
