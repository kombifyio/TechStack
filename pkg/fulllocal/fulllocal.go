// Package fulllocal enforces the Full Local data protection profile of the
// self-hosted edition (workspace platform/standards/DATA-ARCHITECTURE.md §5,
// D-2026-10-21-08 and -12): with KOMBIFY_EDITION=selfhost-oss and
// KOMBIFY_DATA_PROFILE=full-local, nothing leaves the homelab after the
// installation phase. No kombify service, no third party.
//
// The profile is read once at boot and never fetched. An unreadable value
// refuses the start; it is never treated as Standard. Under Full Local the
// process refuses to start with settings that would send data out, and every
// outbound connection of the shared HTTP transport is checked against the
// homelab address ranges before it is dialed.
package fulllocal

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/kombifyio/techstack/internal/gocommon/dataprofile"
	"github.com/kombifyio/techstack/internal/gocommon/edition"

	"github.com/kombifyio/techstack/pkg/config"
)

// ReasonCode names a Full Local refusal in logs, gates and denial envelopes.
const ReasonCode = "data_profile_full_local"

var (
	active  atomic.Bool
	profile atomic.Pointer[dataprofile.Profile]
)

// Resolve reads the boot profile of this process. KOMBIFY_DATA_PROFILE unset
// is hosted Standard; "full-local" is accepted only with selfhost-oss.
func Resolve() (dataprofile.Profile, error) {
	ed, err := config.BootEditionFromEnv()
	if err != nil {
		return dataprofile.Profile{}, err
	}
	return dataprofile.FromBoot(edition.Edition(ed), os.Getenv(dataprofile.BootEnvVar))
}

// Boot resolves the profile and, under Full Local, refuses conflicting
// settings and guards the shared HTTP transport. It runs before telemetry or
// any client is created, so nothing is sent before the guard is in place.
func Boot() (dataprofile.Profile, error) {
	p, err := Resolve()
	if err != nil {
		return dataprofile.Profile{}, fmt.Errorf("data protection profile: %w", err)
	}
	profile.Store(&p)
	if !p.Local() {
		return p, nil
	}
	if conflicts := Conflicts(os.Getenv); len(conflicts) > 0 {
		return dataprofile.Profile{}, fmt.Errorf("%s=%s: these settings send data outside the homelab, remove them: %s",
			dataprofile.BootEnvVar, dataprofile.FullLocal, strings.Join(conflicts, ", "))
	}
	if err := guardDefaultTransport(); err != nil {
		return dataprofile.Profile{}, err
	}
	active.Store(true)
	return p, nil
}

// CheckEdition fails when the loaded configuration resolved another edition
// than the one Boot accepted Full Local for (for example from a config file).
func CheckEdition(loaded config.Edition) error {
	if Active() && loaded != config.EditionSelfHostOSS {
		return fmt.Errorf("data protection profile: %s=%s needs edition %s, the configuration resolved %s",
			dataprofile.BootEnvVar, dataprofile.FullLocal, config.EditionSelfHostOSS, loaded)
	}
	return nil
}

// Active reports whether this process runs under Full Local.
func Active() bool { return active.Load() }

// Profile returns the boot profile, or Standard before Boot ran.
func Profile() dataprofile.Profile {
	if p := profile.Load(); p != nil {
		return *p
	}
	return dataprofile.Standard()
}

// Settings that always reach a kombify service or a third party.
var hostedSettings = []string{
	"KOMBIFY_API_KEY",
	"KOMBIFY_ME_API_KEY",
	"TECHSTACK_SAAS_BUILD_PROOF",
	"CLOUDFLARE_KOMBIFY_ME_ORIGIN_CA_TOKEN",
	"TECHSTACK_SELFHOSTED_CLOUD_LOGIN_TOKEN",
	"CONTEXT_DEV_LOGOLINK_ID",
	"PUBLIC_CONTEXT_DEV_LOGOLINK_ID",
}

// Settings whose value is an address: allowed only when it points into the
// homelab (for example a Sentry or OpenTelemetry collector on the LAN).
var addressSettings = []string{
	"SENTRY_DSN",
	"SENTRY_DSN_FRONTEND",
	"PUBLIC_SENTRY_DSN",
	"OTEL_EXPORTER_OTLP_ENDPOINT",
	"NOTIFICATIONS_ENGINE_URL",
	"PUBLIC_POSTHOG_HOST",
}

// Conflicts lists the set environment variables that would send data outside
// the homelab under Full Local. PostHog without an explicit local host would
// fall back to PostHog Cloud, so a key alone is a conflict.
func Conflicts(getenv func(string) string) []string {
	var found []string
	for _, name := range hostedSettings {
		if strings.TrimSpace(getenv(name)) != "" {
			found = append(found, name)
		}
	}
	for _, name := range addressSettings {
		value := strings.TrimSpace(getenv(name))
		if value != "" && !localAddressSetting(value) {
			found = append(found, name)
		}
	}
	if strings.TrimSpace(getenv("PUBLIC_POSTHOG_KEY")) != "" && strings.TrimSpace(getenv("PUBLIC_POSTHOG_HOST")) == "" {
		found = append(found, "PUBLIC_POSTHOG_KEY")
	}
	sort.Strings(found)
	return found
}

// localAddressSetting accepts a URL or host[:port] whose host is a literal
// homelab address or a homelab-only name. Names that need public DNS are not
// accepted at boot; the dial guard decides those per connection.
func localAddressSetting(value string) bool {
	host := value
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		host = parsed.Hostname()
	} else if h, _, err := net.SplitHostPort(value); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if addr, err := netip.ParseAddr(host); err == nil {
		return LocalAddr(addr)
	}
	return localName(host)
}

// Homelab names that never resolve through public DNS (RFC 6762, RFC 8375).
var localSuffixes = []string{".local", ".lan", ".home.arpa", ".internal"}

func localName(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	for _, suffix := range localSuffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// The shared address space of RFC 6598, used by homelab overlays such as
// Tailscale and Headscale.
var sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")

// LocalAddr reports whether addr stays inside the homelab: loopback, private
// (RFC 1918, RFC 4193), link-local or the shared overlay range. Cloud
// metadata addresses are link-local and stay reachable only on the host
// itself, so they count as local.
func LocalAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || sharedAddressSpace.Contains(addr)
}

// DeniedError is the error of a connection Full Local refused.
type DeniedError struct {
	Host string
}

func (e *DeniedError) Error() string {
	return fmt.Sprintf("%s: %s is outside the homelab (%s=%s)", ReasonCode, e.Host, dataprofile.BootEnvVar, dataprofile.FullLocal)
}

// IsDenied reports whether err is a Full Local refusal.
func IsDenied(err error) bool {
	var denied *DeniedError
	return errors.As(err, &denied)
}

type dialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// GuardDial wraps a dial function so it only connects to homelab addresses.
// Names are resolved first and every address must be local; the connection
// goes to the checked address, so a second lookup cannot redirect it.
func GuardDial(next dialFunc) dialFunc {
	if next == nil {
		next = (&net.Dialer{}).DialContext
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if addr, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
			if !LocalAddr(addr) {
				return nil, &DeniedError{Host: host}
			}
			return next(ctx, network, address)
		}
		resolved, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		if len(resolved) == 0 {
			return nil, &DeniedError{Host: host}
		}
		for _, addr := range resolved {
			if !LocalAddr(addr) {
				return nil, &DeniedError{Host: host}
			}
		}
		var lastErr error
		for _, addr := range resolved {
			conn, dialErr := next(ctx, network, net.JoinHostPort(addr.Unmap().String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, lastErr
	}
}

// guardDefaultTransport routes http.DefaultTransport, and every client and
// clone built from it later, through GuardDial. Proxies are dropped: a proxy
// would carry the request out of the homelab under a local dial address.
func guardDefaultTransport() error {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return fmt.Errorf("%s: the shared HTTP transport is %T, not *http.Transport, and cannot be guarded", ReasonCode, http.DefaultTransport)
	}
	guarded := base.Clone()
	guarded.Proxy = nil
	guarded.DialContext = GuardDial(base.DialContext)
	guarded.DialTLSContext = nil
	http.DefaultTransport = guarded
	return nil
}
