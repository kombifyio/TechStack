// Package trustedproxy decides when a forwarded-header claim (client address,
// protocol, host) may be believed. A header such as X-Forwarded-For is written
// by whoever sent the request, so it is honored only when the direct peer is a
// proxy the operator configured. Without that, every client could name its own
// address or make the control plane build URLs for a host of its choosing.
package trustedproxy

import (
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
)

// Env names the proxies allowed to set forwarded headers: a comma or space
// separated list of IP addresses and CIDR ranges, or the keywords "loopback"
// and "private" (RFC 1918, unique local and link-local). "none" trusts no
// proxy. Unset means loopback and private ranges, which is the topology of a
// reverse proxy or platform router in front of one process; the direct peer of
// an internet client is never in those ranges.
const Env = "TECHSTACK_TRUSTED_PROXIES"

// Set is an immutable set of trusted proxy networks.
type Set struct{ prefixes []netip.Prefix }

var (
	loopbackPrefixes = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	privatePrefixes  = []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10"),
	}
)

// Parse builds a set from the Env syntax. Unparseable entries are ignored, so
// a typo narrows trust instead of widening it.
func Parse(raw string) Set {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Set{prefixes: append(append([]netip.Prefix{}, loopbackPrefixes...), privatePrefixes...)}
	}
	var prefixes []netip.Prefix
	for _, field := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		switch strings.ToLower(field) {
		case "none":
		case "loopback":
			prefixes = append(prefixes, loopbackPrefixes...)
		case "private":
			prefixes = append(prefixes, privatePrefixes...)
		default:
			if prefix, err := netip.ParsePrefix(field); err == nil {
				prefixes = append(prefixes, prefix.Masked())
			} else if addr, addrErr := netip.ParseAddr(field); addrErr == nil {
				prefixes = append(prefixes, netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen()))
			}
		}
	}
	return Set{prefixes: prefixes}
}

var (
	cacheMu  sync.Mutex
	cacheRaw string
	cacheSet Set
	cacheOK  bool
)

// FromEnv returns the set configured by Env. The parse is cached per value.
func FromEnv() Set {
	raw := os.Getenv(Env)
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if !cacheOK || cacheRaw != raw {
		cacheRaw, cacheSet, cacheOK = raw, Parse(raw), true
	}
	return cacheSet
}

// Contains reports whether the address belongs to a trusted proxy.
func (s Set) Contains(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, prefix := range s.prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// Trusts reports whether the direct peer of the request (RemoteAddr) is a
// trusted proxy.
func (s Set) Trusts(r *http.Request) bool {
	if r == nil {
		return false
	}
	peer, ok := peerAddr(r.RemoteAddr)
	return ok && s.Contains(peer)
}

// ClientIP returns the address of the client. It is the direct peer unless
// that peer is a trusted proxy; then the X-Forwarded-For chain is walked from
// the right, skipping trusted hops, and the first address that is not a trusted
// proxy is the client. Entries a client prepended to the header are therefore
// never reached. X-Real-IP is not consulted: a proxy that does not overwrite
// it would pass the client's value through.
func (s Set) ClientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	peer, ok := peerAddr(r.RemoteAddr)
	if !ok {
		return strings.TrimSpace(r.RemoteAddr)
	}
	if !s.Contains(peer) {
		return peer.String()
	}
	chain := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(chain) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(chain[i]))
		if err != nil {
			// A malformed hop ends the trustworthy part of the chain.
			return peer.String()
		}
		if !s.Contains(hop) {
			return hop.Unmap().String()
		}
	}
	return peer.String()
}

// ForwardedOrigin returns the protocol and host a trusted proxy reports, and
// false when the request did not come from one.
func (s Set) ForwardedOrigin(r *http.Request) (proto, host string, ok bool) {
	if !s.Trusts(r) {
		return "", "", false
	}
	proto = firstValue(r.Header.Get("X-Forwarded-Proto"))
	host = firstValue(r.Header.Get("X-Forwarded-Host"))
	return proto, host, proto != "" || host != ""
}

func firstValue(v string) string {
	first, _, _ := strings.Cut(v, ",")
	return strings.TrimSpace(first)
}

func peerAddr(remote string) (netip.Addr, bool) {
	host := strings.TrimSpace(remote)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}
