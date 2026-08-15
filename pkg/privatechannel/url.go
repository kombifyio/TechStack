// Package privatechannel owns the narrow clear-text channel used by the
// account-free Windows alpha on a user's private LAN.
package privatechannel

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

const LANBridgePort = "5264"

// NormalizeLANOrigin accepts only an explicit RFC1918 IPv4 HTTP origin on the
// dedicated bridge port. DNS names, credentials and URL suffixes are rejected.
func NormalizeLANOrigin(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("private LAN origin must be plain HTTP without credentials or suffixes")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("private LAN origin must not contain a path")
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || ip.To4() == nil || !ip.IsPrivate() || parsed.Port() != LANBridgePort {
		return "", errors.New("private LAN origin must use a literal RFC1918 IPv4 address on port 5264")
	}
	return "http://" + net.JoinHostPort(ip.String(), LANBridgePort), nil
}

// MatchesLANOrigin binds an HTTP endpoint to the one enrolled bridge origin.
func MatchesLANOrigin(rawEndpoint, allowedOrigin string) bool {
	endpoint, err := url.Parse(strings.TrimSpace(rawEndpoint))
	if err != nil || endpoint.Scheme != "http" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return false
	}
	origin, err := NormalizeLANOrigin(endpoint.Scheme + "://" + endpoint.Host)
	if err != nil {
		return false
	}
	allowed, err := NormalizeLANOrigin(allowedOrigin)
	return err == nil && origin == allowed
}
