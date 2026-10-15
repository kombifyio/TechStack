package privatechannel

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// A Guard pinned to the bridge's key keeps reaching it after the bridge
// restarts, and refuses a bridge that presents any other key.
func TestPinnedGuardReachesOnlyThePersistedBridgeKey(t *testing.T) {
	certPath := filepath.Join(t.TempDir(), "lan-bridge")
	_, pin, err := LoadOrCreateCertificate(certPath)
	if err != nil {
		t.Fatal(err)
	}
	restarted, restartedPin, err := LoadOrCreateCertificate(certPath)
	if err != nil || restartedPin != pin {
		t.Fatalf("restart changed the pin: %q -> %q (%v)", pin, restartedPin, err)
	}
	other, _, err := LoadOrCreateCertificate(filepath.Join(t.TempDir(), "other"))
	if err != nil {
		t.Fatal(err)
	}

	get := func(cert tls.Certificate) error {
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "bridge") }))
		server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
		server.StartTLS()
		defer server.Close()
		transport, err := PinnedTransport(http.DefaultTransport, "https://192.168.10.2:5264", pin)
		if err != nil {
			t.Fatal(err)
		}
		// The bridge address is a LAN literal; route it to the test listener.
		transport.(*lanTransport).pinned.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		}
		response, err := (&http.Client{Transport: transport}).Get("https://192.168.10.2:5264/install.sh")
		if err != nil {
			return err
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if string(body) != "bridge" {
			t.Fatalf("pinned request read %q", body)
		}
		return nil
	}
	if err := get(restarted); err != nil {
		t.Fatalf("pinned Guard could not reach its bridge: %v", err)
	}
	if err := get(other); !errors.Is(err, ErrPinMismatch) {
		t.Fatalf("pinned Guard accepted another key: %v", err)
	}
}
