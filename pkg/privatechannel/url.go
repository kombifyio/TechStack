// Package privatechannel owns the narrow TLS channel a local control plane
// opens on a user's private LAN for the account-free Windows alpha. The bridge
// presents a self-signed certificate; a Guard trusts it only by the public-key
// pin that reaches the server inside the install command the user pastes.
package privatechannel

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

const LANBridgePort = "5264"

// NormalizeLANOrigin accepts only an explicit RFC1918 IPv4 HTTPS origin on the
// dedicated bridge port. DNS names, credentials and URL suffixes are rejected.
func NormalizeLANOrigin(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("private LAN origin must be HTTPS without credentials or suffixes")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("private LAN origin must not contain a path")
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || ip.To4() == nil || !ip.IsPrivate() || parsed.Port() != LANBridgePort {
		return "", errors.New("private LAN origin must use a literal RFC1918 IPv4 address on port 5264")
	}
	return "https://" + net.JoinHostPort(ip.String(), LANBridgePort), nil
}
