package kombifypoint

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	defaultDNSAddr  = ":53"
	defaultHTTPAddr = ":8088"
	defaultTTL      = 30
)

var defaultUpstreams = []string{"1.1.1.1:53", "8.8.8.8:53"}

// Config controls the local DNS resolver used by Kombify Point.
type Config struct {
	Zones        []string
	TargetIP     netip.Addr
	DNSAddr      string
	HTTPAddr     string
	Upstreams    []string
	AllowedCIDRs []netip.Prefix
	TTL          uint32
	Timeout      time.Duration
}

// ConfigFromEnv builds a production config from environment variables.
func ConfigFromEnv() (Config, error) {
	target := strings.TrimSpace(os.Getenv("KOMBIFY_POINT_TARGET_IP"))
	if target == "" {
		return Config{}, fmt.Errorf("KOMBIFY_POINT_TARGET_IP is required")
	}

	targetIP, err := netip.ParseAddr(target)
	if err != nil {
		return Config{}, fmt.Errorf("invalid KOMBIFY_POINT_TARGET_IP: %w", err)
	}

	cfg := Config{
		Zones:     splitList(envDefault("KOMBIFY_POINT_ZONES", "home")),
		TargetIP:  targetIP,
		DNSAddr:   envDefault("KOMBIFY_POINT_DNS_ADDR", defaultDNSAddr),
		HTTPAddr:  envDefault("KOMBIFY_POINT_HTTP_ADDR", defaultHTTPAddr),
		Upstreams: normalizeUpstreams(splitList(envDefault("KOMBIFY_POINT_UPSTREAMS", strings.Join(defaultUpstreams, ",")))),
		TTL:       defaultTTL,
		Timeout:   2 * time.Second,
	}

	cfg.AllowedCIDRs, err = parseAllowedCIDRs(envDefault(
		"KOMBIFY_POINT_ALLOWED_CIDRS",
		"10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,127.0.0.0/8,::1/128,fd00::/8,fe80::/10",
	))
	if err != nil {
		return Config{}, err
	}
	if envBool("KOMBIFY_POINT_ALLOW_LOOPBACK") {
		cfg.AllowedCIDRs = append(cfg.AllowedCIDRs, netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128"))
	}

	return cfg.Validated()
}

// Validated normalizes and validates the resolver configuration.
func (c Config) Validated() (Config, error) {
	if !isPrivateLANAddr(c.TargetIP) {
		return Config{}, fmt.Errorf("target IP %s is not a private LAN address", c.TargetIP)
	}
	if c.DNSAddr == "" {
		c.DNSAddr = defaultDNSAddr
	}
	if c.HTTPAddr == "" {
		c.HTTPAddr = defaultHTTPAddr
	}
	if c.TTL == 0 {
		c.TTL = defaultTTL
	}
	if c.Timeout <= 0 {
		c.Timeout = 2 * time.Second
	}
	if len(c.Upstreams) == 0 {
		c.Upstreams = defaultUpstreams
	}
	if len(c.AllowedCIDRs) == 0 {
		c.AllowedCIDRs = []netip.Prefix{
			netip.MustParsePrefix("10.0.0.0/8"),
			netip.MustParsePrefix("172.16.0.0/12"),
			netip.MustParsePrefix("192.168.0.0/16"),
			netip.MustParsePrefix("127.0.0.0/8"),
			netip.MustParsePrefix("::1/128"),
			netip.MustParsePrefix("fd00::/8"),
			netip.MustParsePrefix("fe80::/10"),
		}
	}

	zones, err := normalizeZones(c.Zones)
	if err != nil {
		return Config{}, err
	}
	c.Zones = zones
	c.Upstreams = normalizeUpstreams(c.Upstreams)
	return c, nil
}

func envDefault(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func splitList(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\t' || r == ' '
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

func normalizeZones(zones []string) ([]string, error) {
	if len(zones) == 0 {
		zones = []string{"home"}
	}

	seen := map[string]struct{}{}
	out := make([]string, 0, len(zones))
	for _, zone := range zones {
		zone = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zone)), ".")
		if zone == "" {
			continue
		}
		if zone == "arpa" || strings.HasSuffix(zone, ".arpa") {
			return nil, fmt.Errorf("local zone %q is not allowed: .arpa is reserved", zone)
		}
		if zone == "kombify.me" || strings.HasSuffix(zone, ".kombify.me") {
			return nil, fmt.Errorf("local zone %q is not allowed: kombify.me is public DNS", zone)
		}
		if _, ok := seen[zone]; ok {
			continue
		}
		seen[zone] = struct{}{}
		out = append(out, zone)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("at least one local zone is required")
	}

	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) == len(out[j]) {
			return out[i] < out[j]
		}
		return len(out[i]) > len(out[j])
	})
	return out, nil
}

func normalizeUpstreams(upstreams []string) []string {
	out := make([]string, 0, len(upstreams))
	for _, upstream := range upstreams {
		upstream = strings.TrimSpace(upstream)
		if upstream == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(upstream); err != nil {
			upstream = net.JoinHostPort(upstream, "53")
		}
		out = append(out, upstream)
	}
	if len(out) == 0 {
		return defaultUpstreams
	}
	return out
}

func parseAllowedCIDRs(value string) ([]netip.Prefix, error) {
	parts := splitList(value)
	out := make([]netip.Prefix, 0, len(parts))
	for _, part := range parts {
		prefix, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("invalid allowed CIDR %q: %w", part, err)
		}
		out = append(out, prefix)
	}
	return out, nil
}

func isPrivateLANAddr(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	return addr.IsPrivate() || addr.IsLinkLocalUnicast()
}
