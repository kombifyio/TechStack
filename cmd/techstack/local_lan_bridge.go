package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/internal/routes"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/tunnel"
)

const (
	localLANBridgePort = 5264
	localLANBridgeMode = "private-lan-http-alpha"
)

type localLANBridge struct {
	mu       sync.Mutex
	router   *httpx.Router
	resolver *tunnel.RegistryURLResolver
	log      *logger.Logger
	state    string
	server   *http.Server
	listener net.Listener
	status   routes.LocalLANBridgeStatus
}

func newLocalLANBridge(router *httpx.Router, dataDir string, resolver *tunnel.RegistryURLResolver, log *logger.Logger) *localLANBridge {
	return &localLANBridge{
		router: router, resolver: resolver, log: log,
		state:  filepath.Join(dataDir, "lan-bridge-enabled"),
		status: routes.LocalLANBridgeStatus{Port: localLANBridgePort, Mode: localLANBridgeMode},
	}
}

func (b *localLANBridge) Status() routes.LocalLANBridgeStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.status
}

func (b *localLANBridge) Enable(_ context.Context) (routes.LocalLANBridgeStatus, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.status.Enabled {
		return b.status, nil
	}
	privateIP, err := outboundPrivateIPv4()
	if err != nil {
		b.status.Error = err.Error()
		return b.status, err
	}
	listener, err := net.Listen("tcp4", fmt.Sprintf("0.0.0.0:%d", localLANBridgePort))
	if err != nil {
		b.status.Error = err.Error()
		return b.status, err
	}
	origin := fmt.Sprintf("http://%s:%d", privateIP, localLANBridgePort)
	if err := b.resolver.SetLocalBridgeURL(origin); err != nil {
		_ = listener.Close()
		b.status.Error = err.Error()
		return b.status, err
	}
	server := &http.Server{
		Handler:           localLANBridgeHandler{next: b.router.BuildMux()},
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if err := os.MkdirAll(filepath.Dir(b.state), 0o700); err != nil {
		b.resolver.ClearLocalBridgeURL()
		_ = listener.Close()
		return b.status, err
	}
	if err := os.WriteFile(b.state, []byte("enabled\n"), 0o600); err != nil {
		b.resolver.ClearLocalBridgeURL()
		_ = listener.Close()
		return b.status, err
	}
	b.server, b.listener = server, listener
	b.status = routes.LocalLANBridgeStatus{Enabled: true, Origin: origin, Address: privateIP, Port: localLANBridgePort, Mode: localLANBridgeMode}
	b.log.Info("local_lan_bridge_started", "origin", origin, "routes", "guard-only")
	go b.serve(server, listener)
	return b.status, nil
}

func (b *localLANBridge) Disable(ctx context.Context) (routes.LocalLANBridgeStatus, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.stopLocked(ctx, false); err != nil {
		return b.status, err
	}
	if err := os.Remove(b.state); err != nil && !errors.Is(err, os.ErrNotExist) {
		return b.status, err
	}
	return b.status, nil
}

func (b *localLANBridge) Restore(ctx context.Context) error {
	if _, err := os.Stat(b.state); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	_, err := b.Enable(ctx)
	return err
}

func (b *localLANBridge) Shutdown(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stopLocked(ctx, true)
}

func (b *localLANBridge) stopLocked(ctx context.Context, preserveState bool) error {
	server := b.server
	b.server, b.listener = nil, nil
	b.resolver.ClearLocalBridgeURL()
	b.status = routes.LocalLANBridgeStatus{Port: localLANBridgePort, Mode: localLANBridgeMode}
	if server == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if !preserveState {
		b.log.Info("local_lan_bridge_stopped")
	}
	return nil
}

func (b *localLANBridge) serve(server *http.Server, listener net.Listener) {
	err := server.Serve(listener)
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.server != server {
		return
	}
	b.server, b.listener = nil, nil
	b.resolver.ClearLocalBridgeURL()
	b.status.Enabled = false
	b.status.Error = err.Error()
	b.log.Error("local_lan_bridge_failed", "error", err)
}

type localLANBridgeHandler struct{ next http.Handler }

func (h localLANBridgeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !privateRemoteAddress(r.RemoteAddr) || !allowedLocalLANBridgeRoute(r.Method, r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	r.Header.Del("X-Forwarded-Host")
	r.Header.Del("X-Forwarded-Proto")
	r.Header.Set("X-Techstack-LAN-Bridge", "1")
	h.next.ServeHTTP(w, r)
}

func allowedLocalLANBridgeRoute(method, path string) bool {
	if method == http.MethodGet && (path == "/install.sh" || path == "/install.ps1") {
		return true
	}
	if method != http.MethodPost {
		return false
	}
	if path == "/api/v1/workers/register" || path == "/api/v1/workers/bootstrap/logs" || strings.HasPrefix(path, "/api/v1/agent/") {
		return true
	}
	workerPath := strings.TrimPrefix(path, "/api/v1/workers/")
	if workerPath == path {
		return false
	}
	separator := strings.IndexByte(workerPath, '/')
	if separator <= 0 {
		return false
	}
	switch workerPath[separator+1:] {
	case "heartbeat", "inventory", "commands/next", "commands/result", "stackkit/operations", "runtime/logs":
		return true
	default:
		return false
	}
}

func privateRemoteAddress(remote string) bool {
	host := strings.TrimSpace(remote)
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && (ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

func outboundPrivateIPv4() (string, error) {
	connection, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("1.1.1.1"), Port: 80})
	if err == nil {
		defer connection.Close()
		if ip := connection.LocalAddr().(*net.UDPAddr).IP; ip.IsPrivate() && ip.To4() != nil {
			return ip.String(), nil
		}
	}
	addresses, listErr := net.InterfaceAddrs()
	if listErr != nil {
		return "", listErr
	}
	for _, address := range addresses {
		ip, _, parseErr := net.ParseCIDR(address.String())
		if parseErr == nil && ip.To4() != nil && ip.IsPrivate() && !ip.IsLoopback() {
			return ip.String(), nil
		}
	}
	return "", errors.New("no private IPv4 address is available for local server enrollment")
}
