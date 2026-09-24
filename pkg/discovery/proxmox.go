package discovery

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// FingerprintProxmox inspects the public login API on an already discovered IP.
// A fingerprint identifies a candidate, never a trusted management connection.
// No credential, cookie, redirect, proxy or DNS lookup is used by this probe.
func FingerprintProxmox(ctx context.Context, ip string, port int, timeout time.Duration) (DiscoveredService, error) {
	result := DiscoveredService{Name: "https", Port: port}
	if net.ParseIP(ip) == nil || port < 1 || port > 65535 {
		return result, errors.New("an IP address and valid port are required")
	}
	if timeout <= 0 || timeout > 5*time.Second {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	transport := &http.Transport{
		// Deliberately unauthenticated discovery: self-signed Proxmox certificates
		// are observed, not trusted. Enrollment must independently verify this pin.
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}, // #nosec G402 -- credential-free fingerprint only
		DialContext:       (&net.Dialer{Timeout: timeout}).DialContext,
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+net.JoinHostPort(ip, strconv.Itoa(port))+"/api2/json/access/domains", nil)
	if err != nil {
		return result, err
	}
	response, err := client.Do(req)
	if err != nil {
		return result, errors.New("Proxmox fingerprint unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(strings.ToLower(response.Header.Get("Server")), "pve-api-daemon/") || response.TLS == nil || len(response.TLS.PeerCertificates) == 0 {
		return result, errors.New("response is not a Proxmox login API")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
	if err != nil || len(body) > 64*1024 {
		return result, errors.New("invalid Proxmox login response")
	}
	var envelope struct {
		Data []struct {
			Realm string `json:"realm"`
			Type  string `json:"type"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return result, errors.New("invalid Proxmox login response")
	}
	for _, realm := range envelope.Data {
		if (realm.Realm == "pam" && realm.Type == "pam") || (realm.Realm == "pve" && realm.Type == "pve") {
			pin := sha256.Sum256(response.TLS.PeerCertificates[0].Raw)
			result.Name = "proxmox"
			result.FingerprintVerified = true
			result.CertificateSHA256 = hex.EncodeToString(pin[:])
			return result, nil
		}
	}
	return result, errors.New("Proxmox authentication realms were not found")
}
