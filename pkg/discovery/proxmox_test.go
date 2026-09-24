package discovery_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/discovery"
)

// Sensitive boundary: unauthenticated discovery must not turn an arbitrary
// open HTTPS port or redirect into a verified Proxmox management candidate.
func TestProxmoxFingerprintRequiresPublicAPIAndDoesNotFollowRedirects(t *testing.T) {
	for _, scenario := range []string{"proxmox", "unrelated", "redirect"} {
		t.Run(scenario, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Error("probe sent credentials")
				}
				if r.URL.Path != "/api2/json/access/domains" {
					t.Error("probe followed a redirect")
					w.WriteHeader(500)
					return
				}
				switch scenario {
				case "redirect":
					http.Redirect(w, r, "/login", http.StatusFound)
				case "unrelated":
					_, _ = w.Write([]byte(`{"data":[{"realm":"pam","type":"pam"}]}`))
				default:
					w.Header().Set("Server", "pve-api-daemon/3.0")
					_, _ = w.Write([]byte(`{"data":[{"realm":"pam","type":"pam"}]}`))
				}
			}))
			defer srv.Close()
			host, rawPort, _ := net.SplitHostPort(srv.Listener.Addr().String())
			port, _ := strconv.Atoi(rawPort)
			result, err := discovery.FingerprintProxmox(context.Background(), host, port, time.Second)
			if scenario == "proxmox" {
				pin := sha256.Sum256(srv.Certificate().Raw)
				if err != nil || !result.FingerprintVerified || result.Name != "proxmox" || result.CertificateSHA256 != hex.EncodeToString(pin[:]) {
					t.Fatalf("candidate not bound to observed certificate: %+v, %v", result, err)
				}
			} else if err == nil || result.FingerprintVerified || result.Name == "proxmox" {
				t.Fatalf("non-Proxmox endpoint accepted: %+v", result)
			}
		})
	}
}
