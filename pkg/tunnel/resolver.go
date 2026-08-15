// Package tunnel provides tunnel management for exposing local services publicly.
// It supports Cloudflare Quick Tunnels, Tailscale, and direct networking modes.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/privatechannel"
)

// NetworkMode represents the networking mode for registry exposure.
type NetworkMode string

const (
	// NetworkModeAuto automatically selects the best available mode.
	NetworkModeAuto NetworkMode = "auto"
	// NetworkModeLocal uses direct IP for local network access.
	NetworkModeLocal NetworkMode = "local"
	// NetworkModeTunnel uses Cloudflare Quick Tunnel for public access.
	NetworkModeTunnel NetworkMode = "tunnel"
	// NetworkModeTailscale uses Tailscale mesh VPN.
	NetworkModeTailscale NetworkMode = "tailscale"
	// NetworkModeCustom uses a user-provided URL.
	NetworkModeCustom NetworkMode = "custom"
)

// ErrNoValidURL indicates no valid public URL could be determined.
var ErrNoValidURL = errors.New("no valid public URL available - localhost/127.0.0.1 are not allowed")

// ErrURLValidationFailed indicates the URL validation failed.
var ErrURLValidationFailed = errors.New("URL validation failed")

var installTokenPattern = regexp.MustCompile(`^[A-Za-z0-9._~=-]+$`)

// Config holds tunnel/networking configuration.
type Config struct {
	// Mode specifies the networking mode (auto, local, tunnel, tailscale, custom).
	Mode NetworkMode `json:"mode" yaml:"mode"`
	// CustomURL is used when Mode is "custom".
	CustomURL string `json:"custom_url,omitempty" yaml:"custom_url,omitempty"`
	// LocalPort is the local port to expose.
	LocalPort int `json:"local_port" yaml:"local_port"`
	// GRPCPort is the gRPC port for agent connections.
	GRPCPort int `json:"grpc_port" yaml:"grpc_port"`
	// PreferIPv4 prefers IPv4 addresses when detecting local IP.
	PreferIPv4 bool `json:"prefer_ipv4" yaml:"prefer_ipv4"`
	// AllowPrivateNetworks allows private network IPs for local mode.
	AllowPrivateNetworks bool `json:"allow_private_networks" yaml:"allow_private_networks"`
}

// DefaultConfig returns a default tunnel configuration.
func DefaultConfig() Config {
	return Config{
		Mode:                 NetworkModeAuto,
		LocalPort:            5260,
		GRPCPort:             5263,
		PreferIPv4:           true,
		AllowPrivateNetworks: true,
	}
}

// RegistryURLResolver resolves the public URL for worker registration.
// It ensures that localhost/127.0.0.1 URLs are NEVER returned, as these
// would fail when workers try to connect from different machines.
type RegistryURLResolver struct {
	mu sync.RWMutex

	config        Config
	tunnel        *CloudflareTunnel
	currentURL    string
	currentMode   NetworkMode
	lastValidated time.Time
	log           *logger.Logger
}

// NewRegistryURLResolver creates a new URL resolver with the given configuration.
func NewRegistryURLResolver(config Config, log *logger.Logger) *RegistryURLResolver {
	return &RegistryURLResolver{
		config: config,
		log:    log,
	}
}

// SetLocalBridgeURL makes the explicit private-LAN listener the worker
// registry authority. It is called only after that listener has bound.
func (r *RegistryURLResolver) SetLocalBridgeURL(rawURL string) error {
	if err := ValidateURL(rawURL); err != nil {
		return err
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "http" {
		return errors.New("local bridge URL must use HTTP")
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || !ip.IsPrivate() {
		return errors.New("local bridge URL must use a literal private IP")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.currentURL = strings.TrimRight(rawURL, "/")
	r.currentMode = NetworkModeLocal
	r.lastValidated = time.Now()
	return nil
}

func (r *RegistryURLResolver) ClearLocalBridgeURL() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.currentMode != NetworkModeLocal {
		return
	}
	r.currentURL = ""
	r.currentMode = ""
	r.lastValidated = time.Time{}
}

// GetRegistryURL returns a validated public URL for worker registration.
// This URL is guaranteed to be reachable from external machines.
// It will NEVER return localhost or 127.0.0.1 URLs.
func (r *RegistryURLResolver) GetRegistryURL(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// If we have a cached URL that was validated recently, return it
	if r.currentURL != "" && time.Since(r.lastValidated) < 5*time.Minute {
		return r.currentURL, nil
	}

	var resolvedURL string
	var err error

	switch r.config.Mode {
	case NetworkModeCustom:
		resolvedURL, err = r.resolveCustom()
	case NetworkModeTunnel:
		resolvedURL, err = r.resolveTunnel(ctx)
	case NetworkModeTailscale:
		resolvedURL, err = r.resolveTailscale()
	case NetworkModeLocal:
		resolvedURL, err = r.resolveLocal()
	case NetworkModeAuto:
		resolvedURL, err = r.resolveAuto(ctx)
	default:
		resolvedURL, err = r.resolveAuto(ctx)
	}

	if err != nil {
		return "", err
	}

	// Validate that the URL is not localhost
	if err := ValidateURL(resolvedURL); err != nil {
		return "", fmt.Errorf("resolved URL %q is invalid: %w", resolvedURL, err)
	}

	r.currentURL = resolvedURL
	r.lastValidated = time.Now()

	return resolvedURL, nil
}

// ValidateURL validates that a URL is suitable for worker registration.
// It rejects localhost, 127.0.0.1, and other loopback addresses.
func ValidateURL(rawURL string) error {
	if rawURL == "" {
		return ErrNoValidURL
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrURLValidationFailed, err)
	}

	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("%w: empty hostname", ErrURLValidationFailed)
	}

	// Check for localhost variants
	lowHost := strings.ToLower(host)
	if lowHost == "localhost" || lowHost == "localhost.localdomain" {
		return fmt.Errorf("%w: localhost URLs are not allowed - workers cannot reach localhost", ErrNoValidURL)
	}

	// Parse as IP and check for loopback
	ip := net.ParseIP(host)
	if ip != nil {
		if ip.IsLoopback() {
			return fmt.Errorf("%w: loopback IP %s is not allowed - workers cannot reach this address", ErrNoValidURL, ip)
		}
		if ip.IsUnspecified() {
			return fmt.Errorf("%w: unspecified IP %s is not allowed", ErrNoValidURL, ip)
		}
	}

	return nil
}

// resolveCustom validates and returns the custom URL.
func (r *RegistryURLResolver) resolveCustom() (string, error) {
	if r.config.CustomURL == "" {
		return "", errors.New("custom mode requires a custom_url to be set")
	}

	// Ensure scheme
	customURL := r.config.CustomURL
	if !strings.HasPrefix(customURL, "http://") && !strings.HasPrefix(customURL, "https://") {
		customURL = "https://" + customURL
	}

	r.currentMode = NetworkModeCustom
	return customURL, nil
}

// resolveTunnel starts or returns the Cloudflare tunnel URL.
func (r *RegistryURLResolver) resolveTunnel(ctx context.Context) (string, error) {
	// Initialize tunnel if not exists
	if r.tunnel == nil {
		r.tunnel = NewCloudflareTunnel(r.config.LocalPort, r.log)
	}

	// Check if already running
	if r.tunnel.IsRunning() {
		r.currentMode = NetworkModeTunnel
		return r.tunnel.URL(), nil
	}

	// Start new tunnel
	url, err := r.tunnel.Start(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to start Cloudflare tunnel: %w", err)
	}

	r.currentMode = NetworkModeTunnel
	return url, nil
}

// resolveTailscale attempts to get the Tailscale IP/hostname.
func (r *RegistryURLResolver) resolveTailscale() (string, error) {
	// Check if tailscaled is running and get the IP
	tailscaleIP := getTailscaleIP()
	if tailscaleIP == "" {
		return "", errors.New("tailscale is not connected or not installed")
	}

	r.currentMode = NetworkModeTailscale
	return fmt.Sprintf("https://%s:%d", tailscaleIP, r.config.LocalPort), nil
}

// resolveLocal attempts to get a routable local IP.
func (r *RegistryURLResolver) resolveLocal() (string, error) {
	ip, err := getOutboundIP(r.config.PreferIPv4)
	if err != nil {
		return "", fmt.Errorf("failed to get local IP: %w", err)
	}

	// Validate it's not loopback
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil || parsedIP.IsLoopback() {
		return "", fmt.Errorf("%w: detected IP %s is loopback", ErrNoValidURL, ip)
	}

	// Check if private networks are allowed
	if !r.config.AllowPrivateNetworks && isPrivateIP(parsedIP) {
		return "", fmt.Errorf("private IP %s detected but allow_private_networks is false", ip)
	}

	r.currentMode = NetworkModeLocal
	return fmt.Sprintf("http://%s:%d", ip, r.config.LocalPort), nil
}

// resolveAuto tries different modes in order of preference.
func (r *RegistryURLResolver) resolveAuto(ctx context.Context) (string, error) {
	r.log.Info("Auto-detecting best networking mode")

	// 1. If this process is running on a managed cloud host (Render, etc.)
	//    and the operator told us its public URL, use that. Workers external
	//    to the cloud cannot reach LAN/Tailscale addresses of the SaaS host.
	if saasURL, ok := r.resolveSaaSExternal(); ok {
		r.log.Info("Using configured public SaaS URL for networking", "url", saasURL)
		r.currentMode = NetworkModeCustom
		return saasURL, nil
	}

	// 2. Check if Tailscale is available and connected
	if tailscaleIP := getTailscaleIP(); tailscaleIP != "" {
		r.log.Info("Using Tailscale for networking", "ip", tailscaleIP)
		r.currentMode = NetworkModeTailscale
		return fmt.Sprintf("https://%s:%d", tailscaleIP, r.config.LocalPort), nil
	}

	// 3. Check if we have a routable local IP that's not loopback
	if ip, err := getOutboundIP(r.config.PreferIPv4); err == nil {
		parsedIP := net.ParseIP(ip)
		if parsedIP != nil && !parsedIP.IsLoopback() && (r.config.AllowPrivateNetworks || !isPrivateIP(parsedIP)) {
			r.log.Info("Using local IP for networking", "ip", ip)
			r.currentMode = NetworkModeLocal
			return fmt.Sprintf("http://%s:%d", ip, r.config.LocalPort), nil
		}
	}

	// 4. Fall back to Cloudflare tunnel
	if IsCloudflaredInstalled() {
		r.log.Info("Starting Cloudflare tunnel for public access")
		return r.resolveTunnel(ctx)
	}

	// 5. No valid option available
	return "", fmt.Errorf("%w: no suitable networking mode available - install cloudflared for public access or use Tailscale", ErrNoValidURL)
}

// resolveSaaSExternal returns the operator-configured public URL for this
// TechStack instance when running on a managed cloud host (e.g. Render).
// On SaaS hosts the LAN/loopback address the process binds to is never
// reachable from the workers a customer wants to enroll, so we prefer an
// explicitly configured public hostname.
//
// Probe order (first non-empty wins):
//  1. TECHSTACK_PUBLIC_ORIGIN - canonical hosted-URL knob (see .env.example).
//  2. TECHSTACK_PUBLIC_URL    - alias accepted for forward compatibility.
//  3. RENDER_EXTERNAL_URL     - Render-injected fully qualified URL.
//  4. RENDER_EXTERNAL_HOSTNAME - Render-injected hostname (we add https://).
func (r *RegistryURLResolver) resolveSaaSExternal() (string, bool) {
	for _, key := range []string{"TECHSTACK_PUBLIC_ORIGIN", "TECHSTACK_PUBLIC_URL", "RENDER_EXTERNAL_URL"} {
		if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
			if normalized, ok := normalizeSaaSExternalURL(raw); ok {
				return normalized, true
			}
		}
	}
	if host := strings.TrimSpace(os.Getenv("RENDER_EXTERNAL_HOSTNAME")); host != "" {
		if normalized, ok := normalizeSaaSExternalURL("https://" + host); ok {
			return normalized, true
		}
	}
	return "", false
}

func normalizeSaaSExternalURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	raw = strings.TrimRight(raw, "/")
	if err := ValidateURL(raw); err != nil {
		return "", false
	}
	return raw, true
}

// GetCurrentMode returns the currently active networking mode.
func (r *RegistryURLResolver) GetCurrentMode() NetworkMode {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.currentMode
}

// Stop stops any active tunnels.
func (r *RegistryURLResolver) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.tunnel != nil {
		return r.tunnel.Stop()
	}
	return nil
}

// Status returns the current resolver status.
func (r *RegistryURLResolver) Status() ResolverStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()

	status := ResolverStatus{
		Mode:       r.currentMode,
		URL:        r.currentURL,
		ValidUntil: r.lastValidated.Add(5 * time.Minute),
	}

	if r.tunnel != nil {
		status.TunnelStatus = r.tunnel.Status()
	}

	return status
}

// ResolverStatus contains the current state of the URL resolver.
type ResolverStatus struct {
	Mode         NetworkMode  `json:"mode"`
	URL          string       `json:"url"`
	ValidUntil   time.Time    `json:"valid_until"`
	TunnelStatus TunnelStatus `json:"tunnel_status,omitempty"`
}

// getOutboundIP gets the preferred outbound IP of this machine.
func getOutboundIP(preferIPv4 bool) (string, error) {
	// Dial UDP to a public DNS server to determine our outbound IP
	// This doesn't actually send traffic, just determines the route
	network := "udp4"
	if !preferIPv4 {
		network = "udp"
	}

	conn, err := net.Dial(network, "8.8.8.8:80")
	if err != nil {
		// Try with any network
		conn, err = net.Dial("udp", "8.8.8.8:80")
		if err != nil {
			return "", err
		}
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String(), nil
}

// isPrivateIP checks if an IP is in a private range.
func isPrivateIP(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		// 10.0.0.0/8
		if ip4[0] == 10 {
			return true
		}
		// 172.16.0.0/12
		if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
			return true
		}
		// 192.168.0.0/16
		if ip4[0] == 192 && ip4[1] == 168 {
			return true
		}
	}
	return false
}

// getTailscaleIP attempts to get the Tailscale IP.
func getTailscaleIP() string {
	// Look for tailscale0 interface
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}

	for _, iface := range ifaces {
		if strings.HasPrefix(iface.Name, "tailscale") || iface.Name == "utun" {
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				if ipnet, ok := addr.(*net.IPNet); ok {
					if ip4 := ipnet.IP.To4(); ip4 != nil {
						// Tailscale uses 100.x.x.x CGNAT range
						if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
							return ip4.String()
						}
					}
				}
			}
		}
	}

	return ""
}

// GenerateInstallCommand generates a worker installation command with the resolved URL.
// This command is guaranteed to work on the target worker machine.
func (r *RegistryURLResolver) GenerateInstallCommand(ctx context.Context, token string) (string, error) {
	if err := validateInstallToken(token); err != nil {
		return "", err
	}
	url, err := r.GetRegistryURL(ctx)
	if err != nil {
		return "", fmt.Errorf("cannot generate install command: %w", err)
	}

	// Generate a one-liner that works on Linux
	privateLANEnv := ""
	if origin, originErr := privatechannel.NormalizeLANOrigin(url); originErr == nil {
		privateLANEnv = " KOMBI_PRIVATE_LAN_HTTP_ORIGIN=" + shQuote(origin)
	}
	cmd := fmt.Sprintf(`curl -fsSL %s | KOMBI_SERVER=%s KOMBI_TOKEN=%s%s bash`, shQuote(url+"/install.sh"), shQuote(url), shQuote(token), privateLANEnv)

	return cmd, nil
}

// GenerateInstallCommandPS generates a Windows PowerShell installation command.
func (r *RegistryURLResolver) GenerateInstallCommandPS(ctx context.Context, token string) (string, error) {
	if err := validateInstallToken(token); err != nil {
		return "", err
	}
	url, err := r.GetRegistryURL(ctx)
	if err != nil {
		return "", fmt.Errorf("cannot generate install command: %w", err)
	}

	// Generate PowerShell one-liner
	privateLANEnv := ""
	if origin, originErr := privatechannel.NormalizeLANOrigin(url); originErr == nil {
		privateLANEnv = fmt.Sprintf(`; $env:KOMBI_PRIVATE_LAN_HTTP_ORIGIN=%s`, psQuote(origin))
	}
	cmd := fmt.Sprintf(`$env:KOMBI_SERVER=%s; $env:KOMBI_TOKEN=%s%s; irm %s | iex`, psQuote(url), psQuote(token), privateLANEnv, psQuote(url+"/install.ps1"))

	return cmd, nil
}

func validateInstallToken(token string) error {
	if token == "" {
		return fmt.Errorf("install token is required")
	}
	if !installTokenPattern.MatchString(token) {
		return fmt.Errorf("install token contains unsafe characters")
	}
	return nil
}

func shQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func psQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// Log returns the logger for use by other components.
func (r *RegistryURLResolver) Log() *logger.Logger {
	return r.log
}
