package devicereadiness

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Egress lends a device a temporary way out.
//
// The repair case is circular without it: the device needs a package to fix its
// network, and needs its network to fetch the package. The executor is already
// connected and already has a working route, so it lends that route back down
// the same connection: a listener on the device's own loopback, forwarded to a
// proxy in this process, which will only carry what the allowlist names.
//
// This is real authority, so it is fenced. The listener is on loopback and on
// an ephemeral port. The proxy resolves names itself and refuses anything not
// on the list. It exists for the length of one repair and the apt configuration
// that points at it is removed when it closes. It is never opened by the hosted
// control plane: an operator lending their own connection to their own device
// is a different act from a shared service becoming an open relay.

// EgressPolicy names what a device may reach while the way out is open.
type EgressPolicy struct {
	// AllowedHosts are exact hostnames. There are no wildcards: a pattern is
	// how an allowlist stops being one.
	AllowedHosts []string
	// AllowedTunnelPorts are the ports an encrypted tunnel may be opened to.
	// Empty means 443 only. A tunnel to an arbitrary port would make this a
	// general-purpose relay rather than a way to fetch a package, so the set
	// is stated rather than assumed.
	AllowedTunnelPorts []int
}

// DefaultEgressPolicy is what a repair needs and nothing else: the places a
// package, a runtime, or the agent comes from.
func DefaultEgressPolicy(controlPlaneHost string) EgressPolicy {
	hosts := []string{
		"archive.ubuntu.com",
		"security.ubuntu.com",
		"ports.ubuntu.com",
		"deb.debian.org",
		"security.debian.org",
		"download.docker.com",
		"get.opentofu.org",
		"ghcr.io",
		"pkg-containers.githubusercontent.com",
		"github.com",
		"objects.githubusercontent.com",
	}
	if host := strings.TrimSpace(controlPlaneHost); host != "" {
		hosts = append(hosts, strings.Split(host, ":")[0])
	}
	return EgressPolicy{AllowedHosts: hosts}
}

func (p EgressPolicy) permitsPort(port string) bool {
	if len(p.AllowedTunnelPorts) == 0 {
		return port == "443"
	}
	for _, allowed := range p.AllowedTunnelPorts {
		if port == strconv.Itoa(allowed) {
			return true
		}
	}
	return false
}

func (p EgressPolicy) permits(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if idx := strings.LastIndexByte(host, ':'); idx > 0 && !strings.Contains(host[idx:], "]") {
		host = host[:idx]
	}
	for _, allowed := range p.AllowedHosts {
		if strings.EqualFold(host, allowed) {
			return true
		}
	}
	return false
}

// aptProxyDropIn points the package manager at the borrowed way out for as long
// as it exists. It is a drop-in file so closing the egress removes it entirely
// rather than editing configuration back.
const aptProxyDropInPath = "/etc/apt/apt.conf.d/99-kombify-readiness-egress"

// Egress is an open way out. It stops existing when it is closed.
type Egress struct {
	session   *Session
	listener  net.Listener
	policy    EgressPolicy
	proxyPort int

	closeOnce sync.Once
	closeErr  error
	wg        sync.WaitGroup
}

// OpenEgress lends the device this executor's route.
//
// It refuses when the device already has a proxy: two ways out on one device
// produce failures nobody can read, and the proxy the owner configured is the
// one their network expects to see.
func (s *Session) OpenEgress(ctx context.Context, policy EgressPolicy, deviceProxyConfigured bool) (*Egress, error) {
	if s.egress != nil {
		return s.egress, nil
	}
	if deviceProxyConfigured {
		return nil, errors.New("device readiness: this device already has a proxy; use it rather than opening a second way out")
	}
	if len(policy.AllowedHosts) == 0 {
		return nil, errors.New("device readiness: refusing to open a way out that allows nothing, which would only look like it worked")
	}

	// The listener lives on the device, on loopback, on a port the device
	// chooses. Nothing outside the device can reach it.
	listener, err := s.client.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("device readiness: the device refused to open a local port, so no way out can be lent to it: %w", err)
	}

	port := 0
	if addr, ok := listener.Addr().(*net.TCPAddr); ok {
		port = addr.Port
	}
	if port == 0 {
		_ = listener.Close()
		return nil, errors.New("device readiness: the device opened a port that cannot be addressed")
	}

	egress := &Egress{session: s, listener: listener, policy: policy, proxyPort: port}
	egress.wg.Add(1)
	go egress.serve()

	if err := egress.configureAPT(ctx); err != nil {
		_ = egress.Close()
		return nil, err
	}

	s.egress = egress
	return egress, nil
}

// ProxyURL is what the device should use while this is open.
func (e *Egress) ProxyURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", e.proxyPort)
}

// AllowedHosts is what the device may reach, for the operator to see before
// consenting.
func (e *Egress) AllowedHosts() []string {
	return append([]string(nil), e.policy.AllowedHosts...)
}

// Environment is the assignment prefix for a command that should use the way
// out. It is per command rather than persisted, so a command run outside the
// repair never silently travels through the executor.
func (e *Egress) Environment() string {
	url := e.ProxyURL()
	return fmt.Sprintf("http_proxy=%s https_proxy=%s HTTP_PROXY=%s HTTPS_PROXY=%s", url, url, url, url)
}

func (e *Egress) configureAPT(ctx context.Context) error {
	content := fmt.Sprintf("// Written by Kombify Techstack device readiness for one repair.\n"+
		"// It is removed when the repair ends; if you are reading it afterwards, remove it.\n"+
		"Acquire::http::Proxy \"%s\";\nAcquire::https::Proxy \"%s\";\n", e.ProxyURL(), e.ProxyURL())

	script := fmt.Sprintf("set -e; mkdir -p /etc/apt/apt.conf.d; cat > %s; chmod 0644 %s",
		shellQuote(aptProxyDropInPath), shellQuote(aptProxyDropInPath))
	result, err := e.session.run(ctx, elevate(script, true), "point the package manager at the borrowed route", []byte(content), DefaultCommandTimeout)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("device readiness: could not point the package manager at the borrowed route: %s", firstLine(result.Stderr))
	}
	return nil
}

// Close removes the way out and the configuration that pointed at it.
func (e *Egress) Close() error {
	e.closeOnce.Do(func() {
		_ = e.listener.Close()
		e.wg.Wait()

		// Removing the drop-in matters more than reporting the removal: a
		// device left pointing at a proxy that no longer exists cannot install
		// anything, which is a worse state than the one we found it in.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result, err := e.session.run(ctx, elevate("rm -f "+shellQuote(aptProxyDropInPath), true), "remove the borrowed-route setting", nil, 30*time.Second)
		switch {
		case err != nil:
			e.closeErr = fmt.Errorf("device readiness: the borrowed route is closed but its package-manager setting may remain at %s: %w", aptProxyDropInPath, err)
		case result.ExitCode != 0:
			e.closeErr = fmt.Errorf("device readiness: the borrowed route is closed but its package-manager setting may remain at %s", aptProxyDropInPath)
		}
		if e.session.egress == e {
			e.session.egress = nil
		}
	})
	return e.closeErr
}

// serve accepts what the device sends and carries only what the policy names.
func (e *Egress) serve() {
	defer e.wg.Done()
	proxy := EgressProxy{Policy: e.policy}
	for {
		conn, err := e.listener.Accept()
		if err != nil {
			return
		}
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			proxy.ServeConn(conn)
		}()
	}
}

// EgressProxy is the policy enforcement itself, separate from the connection
// that carries it. Keeping it apart is what makes "what may this device reach"
// answerable without a device.
type EgressProxy struct {
	Policy EgressPolicy
}

// ServeConn handles one connection from the device and closes it.
func (e EgressProxy) ServeConn(deviceConn net.Conn) {
	defer func() { _ = deviceConn.Close() }()
	e.handle(deviceConn)
}

func (e EgressProxy) handle(deviceConn net.Conn) {
	_ = deviceConn.SetDeadline(time.Now().Add(10 * time.Minute))

	// The reader is kept rather than discarded: parsing the request can pull
	// in bytes that belong to the body or to the tunnel that follows, and
	// dropping them would corrupt the very download this exists to carry.
	reader := bufio.NewReader(deviceConn)
	request, err := http.ReadRequest(reader)
	if err != nil {
		return
	}

	if request.Method == http.MethodConnect {
		e.handleConnect(deviceConn, reader, request)
		return
	}
	e.handlePlain(deviceConn, request)
}

// handleConnect carries an encrypted stream to an allowed host. The proxy never
// sees inside it, which is the point: lending a route is not the same as
// reading what travels on it.
func (e EgressProxy) handleConnect(deviceConn net.Conn, buffered *bufio.Reader, request *http.Request) {
	target := request.Host
	if !e.Policy.permits(target) {
		writeProxyRefusal(deviceConn, target)
		return
	}
	if !strings.Contains(target, ":") {
		target += ":443"
	}
	if !e.Policy.permitsPort(portOf(target)) {
		writeProxyRefusal(deviceConn, target)
		return
	}

	upstream, err := net.DialTimeout("tcp", target, 20*time.Second)
	if err != nil {
		writeProxyStatus(deviceConn, http.StatusBadGateway)
		return
	}
	defer func() { _ = upstream.Close() }()

	writeProxyStatus(deviceConn, http.StatusOK)

	// Anything the device sent ahead of the response goes up first, in order.
	if pending := buffered.Buffered(); pending > 0 {
		if _, err := io.CopyN(upstream, buffered, int64(pending)); err != nil {
			return
		}
	}
	pipe(readWriter{Reader: buffered, Writer: deviceConn}, upstream)
}

func (e EgressProxy) handlePlain(deviceConn net.Conn, request *http.Request) {
	host := request.Host
	if host == "" && request.URL != nil {
		host = request.URL.Host
	}
	if !e.Policy.permits(host) {
		writeProxyRefusal(deviceConn, host)
		return
	}
	if request.URL == nil || request.URL.Scheme == "" {
		writeProxyStatus(deviceConn, http.StatusBadRequest)
		return
	}

	// The executor resolves and connects itself. The device's own resolver is
	// very likely the thing that is broken, and a proxy that trusted the
	// device's name resolution would inherit that fault.
	outbound, err := http.NewRequest(request.Method, request.URL.String(), request.Body)
	if err != nil {
		writeProxyStatus(deviceConn, http.StatusBadRequest)
		return
	}
	outbound.Header = request.Header.Clone()
	outbound.Header.Del("Proxy-Connection")

	client := &http.Client{
		Timeout: 10 * time.Minute,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			// A redirect is how an allowlist is escaped, so each hop is
			// checked like the first.
			if !e.Policy.permits(req.URL.Host) {
				return fmt.Errorf("redirect to %s is not allowed", req.URL.Host)
			}
			return nil
		},
	}
	response, err := client.Do(outbound)
	if err != nil {
		writeProxyStatus(deviceConn, http.StatusBadGateway)
		return
	}
	defer func() { _ = response.Body.Close() }()
	_ = response.Write(deviceConn)
}

func portOf(target string) string {
	if idx := strings.LastIndexByte(target, ':'); idx > 0 {
		return target[idx+1:]
	}
	return ""
}

func writeProxyStatus(conn net.Conn, status int) {
	_, _ = fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\n\r\n", status, http.StatusText(status))
}

func writeProxyRefusal(conn net.Conn, host string) {
	body := fmt.Sprintf("Kombify readiness egress does not carry traffic to %s.\n", host)
	_, _ = fmt.Fprintf(conn, "HTTP/1.1 403 Forbidden\r\nContent-Length: %d\r\nContent-Type: text/plain\r\n\r\n%s", len(body), body)
}

// readWriter pairs the buffered reader with the connection it came from, so a
// tunnel reads through the buffer and writes straight to the device.
type readWriter struct {
	io.Reader
	io.Writer
}

func pipe(device readWriter, upstream net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(device, upstream) }()
	go func() { defer wg.Done(); _, _ = io.Copy(upstream, device) }()
	wg.Wait()
}
