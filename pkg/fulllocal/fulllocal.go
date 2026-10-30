// Package fulllocal enforces the Full Local data protection profile of the
// self-hosted edition (workspace platform/standards/DATA-ARCHITECTURE.md §5,
// D-2026-10-21-08 and -12): with KOMBIFY_EDITION=selfhost-oss and
// KOMBIFY_DATA_PROFILE=full-local, nothing leaves the homelab after the
// installation phase. No kombify service, no third party.
//
// The profile is read once at boot and never fetched. An unreadable value
// refuses the start; it is never treated as Standard. Under Full Local the
// process refuses to start with settings that would send data out, and the
// egress guard of kombify-go-common (dataprofile.EnforceEgress) binds the shared
// HTTP transport and the default resolver to the homelab address ranges. Clients
// with a transport or dialer of their own are bound where they are built.
package fulllocal

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/dataprofile"
	"github.com/kombifyio/techstack/internal/gocommon/edition"

	"github.com/kombifyio/techstack/pkg/config"
)

// ReasonCode names a Full Local refusal in logs, gates and denial envelopes.
const ReasonCode = dataprofile.ReasonFullLocal

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
	if err := p.EnforceEgress(); err != nil {
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
	if _, name, err := OTLPTraceEndpoint(getenv, true); err != nil {
		found = append(found, name)
	}
	if strings.TrimSpace(getenv("PUBLIC_POSTHOG_KEY")) != "" && strings.TrimSpace(getenv("PUBLIC_POSTHOG_HOST")) == "" {
		found = append(found, "PUBLIC_POSTHOG_KEY")
	}
	sort.Strings(found)
	return found
}

// OTLPTraceEndpoint resolves the HTTP trace destination with the SDK's signal
// precedence. Techstack deliberately validates both configured URLs more
// strictly than the SDK, including overridden values: malformed input must not
// be logged or replaced by SDK defaults, and credentials/queries are rejected.
// Locality applies only to the effective destination. Boot and telemetry share
// this admission; the SDK still owns paths and all remaining exporter settings.
// Returned errors contain setting names only, never raw configuration values.
func OTLPTraceEndpoint(getenv func(string) string, local bool) (string, string, error) {
	var endpoint, setting string
	for _, name := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"} {
		raw := strings.TrimSpace(getenv(name))
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Opaque != "" || u.Hostname() == "" ||
			(u.Scheme != "http" && u.Scheme != "https") || u.User != nil ||
			u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") || strings.HasSuffix(u.Host, ":") {
			return "", name, fmt.Errorf("%s: invalid OTLP HTTP endpoint; use an http(s) URL without credentials, query or fragment", name)
		}
		if port := u.Port(); port != "" {
			number, err := strconv.Atoi(port)
			if err != nil || number < 1 || number > 65535 {
				return "", name, fmt.Errorf("%s: invalid OTLP endpoint port", name)
			}
		}
		endpoint, setting = raw, name
	}
	if local && endpoint != "" && !localAddressSetting(endpoint) {
		return "", setting, fmt.Errorf("%s: %s is outside the homelab", ReasonCode, setting)
	}
	return endpoint, setting, nil
}

// localAddressSetting accepts a URL or host[:port] whose host is a literal
// homelab address, a homelab-only name, or a name that the homelab resolver
// resolves to homelab addresses only (a homelab chooses its own names). A name
// that does not resolve to the homelab is not accepted at boot; the dial guard
// still decides every connection.
func localAddressSetting(value string) bool {
	host := value
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		host = parsed.Hostname()
	} else if h, _, err := net.SplitHostPort(value); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if addr, err := netip.ParseAddr(host); err == nil {
		return dataprofile.LocalAddr(addr)
	}
	return localName(host) || resolvesInHomelab(host)
}

// resolvesInHomelab asks only homelab nameservers (dataprofile.LocalResolver).
func resolvesInHomelab(host string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addrs, err := dataprofile.LocalResolver().LookupNetIP(ctx, "ip", host)
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, addr := range addrs {
		if !dataprofile.LocalAddr(addr) {
			return false
		}
	}
	return true
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

// Dial returns next unchanged outside Full Local. Under Full Local it returns
// next bound to the homelab (dataprofile.GuardDial). Clients that build a
// dialer or transport of their own call it where they create it, because
// EnforceEgress only covers the shared HTTP transport and the default resolver.
func Dial(next dataprofile.DialFunc) dataprofile.DialFunc {
	if !Active() {
		return next
	}
	return dataprofile.GuardDial(next)
}

// AdmitIP pins a literal target before a subprocess can resolve or contact it.
// Standard keeps its existing behavior; Full Local accepts only the shared
// SDK's homelab addresses, with no second hostname lookup in the subprocess.
func AdmitIP(host string) (string, error) {
	if !Active() {
		return host, nil
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !dataprofile.LocalAddr(addr) {
		// Invalid input is never echoed into the metadata-only audit.
		auditHost := ""
		if err == nil {
			auditHost = addr.Unmap().String()
		}
		slog.Warn("egress", "decision", "deny", "kind", "admission", "network", "ip", "host", auditHost, "outcome", "refused", "reason", ReasonCode)
		return "", &dataprofile.DeniedError{Host: host}
	}
	literal := addr.Unmap().String()
	slog.Info("egress", "decision", "allow", "kind", "admission", "network", "ip", "host", literal, "outcome", "admitted", "reason", "homelab")
	return literal, nil
}

// Transport is Dial for a whole transport: outside Full Local it returns t
// unchanged, under Full Local it is bound to the homelab.
func Transport(t *http.Transport) *http.Transport {
	if Active() {
		dataprofile.GuardTransport(t)
	}
	return t
}
