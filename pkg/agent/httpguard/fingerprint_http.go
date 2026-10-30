package httpguard

// The network half of the HTTP fingerprint probe (see fingerprint.go): prove
// the scheme of one published port, fetch a few pages, and let pkg/appcatalog
// recognize them. Every connection goes through pinnedDial, so a request can
// only ever reach the one host address and port it was planned for.

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"

	"github.com/kombifyio/techstack/pkg/appcatalog"
)

const (
	fingerprintPageLimit    = 512 << 10
	fingerprintFaviconLimit = 256 << 10
	fingerprintMaxRedirects = 3
	fingerprintHeaderLimit  = 64 << 10
	fingerprintUserAgent    = "kombify-guard-fingerprint/1"

	schemeHTTP  = "http"
	schemeHTTPS = "https"
)

var errProbeTarget = errors.New("fingerprint probe refused an address outside the host's own published port")

type dialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// pinnedDial returns a dial function that connects only to ip:port and only
// while that address is loopback or on the local network. The address the
// transport asks for is checked, not trusted: a redirect, a proxy setting or a
// name that resolves elsewhere gets errProbeTarget instead of a connection.
func pinnedDial(ip net.IP, port int) (dial dialFunc, origin string) {
	origin = net.JoinHostPort(ip.String(), strconv.Itoa(port))
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != origin || !probeIPAllowed(ip) || (network != "tcp" && network != "tcp4" && network != "tcp6") {
			return nil, errProbeTarget
		}
		var dialer net.Dialer
		return dialer.DialContext(ctx, "tcp", origin)
	}, origin
}

// probeOnePort is the production portProber. It tries a TLS handshake first:
// a port that completes one speaks https, any other reachable port is tried as
// plain http. Only a port that answers HTTP is a web port; one that answers
// something else, or nothing, proves no web UI.
func probeOnePort(ctx context.Context, target probeTarget) portProbe {
	ip := net.ParseIP(target.ip)
	if ip == nil || !probeIPAllowed(ip) || target.port < 1 || target.port > 65535 {
		return portProbe{}
	}
	dial, origin := pinnedDial(ip, target.port)
	scheme, reachable := detectScheme(ctx, dial, origin)
	if !reachable {
		return portProbe{}
	}
	client := newProbeClient(dial, scheme, origin)
	defer client.CloseIdleConnections()
	return recognizePort(ctx, client, scheme, origin)
}

// detectScheme completes a TLS handshake on the port. reachable is false when
// nothing accepted the connection.
func detectScheme(ctx context.Context, dial dialFunc, origin string) (scheme string, reachable bool) {
	ctx, cancel := context.WithTimeout(ctx, fingerprintRequestTimeout)
	defer cancel()
	conn, err := dial(ctx, "tcp", origin)
	if err != nil {
		return "", false
	}
	defer func() { _ = conn.Close() }()
	// Certificate verification is skipped on purpose: an application on the
	// user's own network almost always serves a self-signed certificate, and
	// this handshake carries nothing secret. Its only result is whether the
	// port speaks https, which decides the scheme of the open link.
	// #nosec G402 -- LAN apps use self-signed certificates; no secret is sent.
	tlsConn := tls.Client(conn, &tls.Config{InsecureSkipVerify: true})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		// A web server answers a stray ClientHello at once (a 400 or a close). A
		// peer that stays silent until the timeout is not one, and asking it
		// again in plain text would only spend the budget twice.
		var network net.Error
		if ctx.Err() != nil || (errors.As(err, &network) && network.Timeout()) {
			return "", false
		}
		return schemeHTTP, true
	}
	return schemeHTTPS, true
}

// newProbeClient builds the client for one port. It sends no cookies or
// credentials, ignores proxy settings, never reuses a connection, and follows a
// redirect only inside the same scheme, host and port, at most three times.
func newProbeClient(dial dialFunc, scheme, origin string) *http.Client {
	transport := &http.Transport{
		Proxy:       nil,
		DialContext: dial,
		// See detectScheme: the certificate is not verified, nothing secret is sent.
		// #nosec G402 -- LAN apps use self-signed certificates; no secret is sent.
		TLSClientConfig:        &tls.Config{InsecureSkipVerify: true},
		TLSHandshakeTimeout:    fingerprintRequestTimeout,
		ResponseHeaderTimeout:  fingerprintRequestTimeout,
		MaxResponseHeaderBytes: fingerprintHeaderLimit,
		DisableKeepAlives:      true,
		DisableCompression:     true,
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) > fingerprintMaxRedirects || next.URL.Scheme != scheme || next.URL.Host != origin {
				// The redirect response itself is the answer; it is not followed.
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
}

// fetchedPage is one response, bounded.
type fetchedPage struct {
	observation appcatalog.HTTPObservation
	truncated   bool
}

// fetchPage GETs one path of the port. A response of any status counts, and a
// body that fails mid-read keeps what arrived: the status and headers already
// proved HTTP.
func fetchPage(ctx context.Context, client *http.Client, scheme, origin, path string, limit int64) (fetchedPage, error) {
	ctx, cancel := context.WithTimeout(ctx, fingerprintRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+origin+path, nil)
	if err != nil {
		return fetchedPage{}, err
	}
	request.Header.Set("User-Agent", fingerprintUserAgent)
	request.Header.Set("Accept", "*/*")
	response, err := client.Do(request)
	if err != nil {
		return fetchedPage{}, err
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(response.Body, limit+1))
	truncated := int64(len(body)) > limit
	if truncated {
		body = body[:limit]
	}
	return fetchedPage{
		observation: appcatalog.HTTPObservation{
			Path: response.Request.URL.Path, Status: response.StatusCode, Header: response.Header, Body: body,
		},
		truncated: truncated,
	}, nil
}

// recognizePort fetches `/` (a port that answers it is a web port), then the
// favicon, then, only while the application is still unknown, the few most
// common login and panel paths of the fingerprint set.
func recognizePort(ctx context.Context, client *http.Client, scheme, origin string) portProbe {
	root, err := fetchPage(ctx, client, scheme, origin, "/", fingerprintPageLimit)
	if err != nil {
		return portProbe{}
	}
	probe := portProbe{web: true, scheme: scheme}
	observations := []appcatalog.HTTPObservation{root.observation}
	if probe.app = appcatalog.MatchHTTP(observations); probe.app != "" {
		return probe
	}
	if icon, err := fetchPage(ctx, client, scheme, origin, "/favicon.ico", fingerprintFaviconLimit); err == nil &&
		icon.observation.Status == http.StatusOK && !icon.truncated {
		if probe.app = appcatalog.MatchFavicon(icon.observation.Body); probe.app != "" {
			return probe
		}
	}
	for _, path := range appcatalog.FingerprintPaths() {
		page, err := fetchPage(ctx, client, scheme, origin, path, fingerprintPageLimit)
		if err != nil {
			continue
		}
		observations = append(observations, page.observation)
		if probe.app = appcatalog.MatchHTTP(observations); probe.app != "" {
			return probe
		}
	}
	return probe
}
