// Package tunnel provides Cloudflare tunnel management for exposing local services.
package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/logger"
)

// ErrCloudflaredNotFound indicates cloudflared binary is not installed.
var ErrCloudflaredNotFound = errors.New("cloudflared binary not found - install from https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/")

// ErrTunnelURLNotFound indicates the tunnel URL could not be parsed from output.
var ErrTunnelURLNotFound = errors.New("tunnel URL not found in cloudflared output")

// ErrTunnelNotRunning indicates an operation was attempted on a stopped tunnel.
var ErrTunnelNotRunning = errors.New("tunnel is not running")

// ErrTunnelAlreadyRunning indicates Start was called on an already running tunnel.
var ErrTunnelAlreadyRunning = errors.New("tunnel is already running")

// TunnelState represents the current state of the tunnel.
type TunnelState int

const (
	// TunnelStateStopped indicates the tunnel is not running.
	TunnelStateStopped TunnelState = iota
	// TunnelStateStarting indicates the tunnel is starting up.
	TunnelStateStarting
	// TunnelStateRunning indicates the tunnel is active and connected.
	TunnelStateRunning
	// TunnelStateStopping indicates the tunnel is shutting down.
	TunnelStateStopping
	// TunnelStateError indicates the tunnel encountered an error.
	TunnelStateError
)

// String returns the string representation of the tunnel state.
func (s TunnelState) String() string {
	switch s {
	case TunnelStateStopped:
		return "stopped"
	case TunnelStateStarting:
		return "starting"
	case TunnelStateRunning:
		return "running"
	case TunnelStateStopping:
		return "stopping"
	case TunnelStateError:
		return "error"
	default:
		return "unknown"
	}
}

// TunnelStatus contains the current status of a tunnel.
type TunnelStatus struct {
	State     TunnelState `json:"state"`
	URL       string      `json:"url,omitempty"`
	LocalPort int         `json:"local_port"`
	StartedAt time.Time   `json:"started_at,omitempty"`
	Error     string      `json:"error,omitempty"`
}

// CloudflareTunnel manages a Cloudflare Quick Tunnel subprocess.
type CloudflareTunnel struct {
	mu sync.RWMutex

	localPort int
	url       string
	state     TunnelState
	startedAt time.Time
	lastError error

	cmd       *exec.Cmd
	cancelFn  context.CancelFunc
	waitGroup sync.WaitGroup

	log *logger.Logger

	// urlPattern matches trycloudflare.com URLs in cloudflared output.
	urlPattern *regexp.Regexp
}

// NewCloudflareTunnel creates a new tunnel manager.
// The tunnel will forward traffic to the specified local port.
func NewCloudflareTunnel(localPort int, log *logger.Logger) *CloudflareTunnel {
	return &CloudflareTunnel{
		localPort:  localPort,
		state:      TunnelStateStopped,
		log:        log,
		urlPattern: regexp.MustCompile(`https://[a-zA-Z0-9-]+\.trycloudflare\.com`),
	}
}

// IsCloudflaredInstalled checks if cloudflared binary is available in PATH.
func IsCloudflaredInstalled() bool {
	_, err := exec.LookPath(cloudflaredBinary())
	return err == nil
}

// GetCloudflaredPath returns the path to cloudflared binary or empty string if not found.
func GetCloudflaredPath() string {
	path, err := exec.LookPath(cloudflaredBinary())
	if err != nil {
		return ""
	}
	return path
}

// cloudflaredBinary returns the platform-specific binary name.
func cloudflaredBinary() string {
	if runtime.GOOS == "windows" {
		return "cloudflared.exe"
	}
	return "cloudflared"
}

// Start initiates the tunnel subprocess.
// It blocks until the tunnel URL is obtained or context is canceled.
// Returns the public tunnel URL on success.
func (t *CloudflareTunnel) Start(ctx context.Context) (string, error) {
	t.mu.Lock()

	if t.state == TunnelStateRunning || t.state == TunnelStateStarting {
		t.mu.Unlock()
		return "", ErrTunnelAlreadyRunning
	}

	// Check if cloudflared is installed
	if !IsCloudflaredInstalled() {
		t.state = TunnelStateError
		t.lastError = ErrCloudflaredNotFound
		t.mu.Unlock()
		return "", ErrCloudflaredNotFound
	}

	t.state = TunnelStateStarting
	t.lastError = nil
	t.mu.Unlock()

	// Create cancellable context for the subprocess
	cmdCtx, cancel := context.WithCancel(context.Background())

	// Build the command
	cmd := exec.CommandContext(cmdCtx,
		cloudflaredBinary(),
		"tunnel",
		"--url", fmt.Sprintf("http://localhost:%d", t.localPort),
		"--no-autoupdate",
	)

	// cloudflared writes to stderr, not stdout
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		t.setError(fmt.Errorf("failed to create stderr pipe: %w", err))
		return "", t.lastError
	}

	// Start the process
	if err := cmd.Start(); err != nil {
		cancel()
		t.setError(fmt.Errorf("failed to start cloudflared: %w", err))
		return "", t.lastError
	}

	t.mu.Lock()
	t.cmd = cmd
	t.cancelFn = cancel
	t.mu.Unlock()

	// Channel to receive the parsed URL
	urlChan := make(chan string, 1)
	errChan := make(chan error, 1)

	// Start goroutine to parse stderr for URL
	t.waitGroup.Add(1)
	go func() {
		defer t.waitGroup.Done()
		t.parseStderr(stderr, urlChan, errChan)
	}()

	// Wait for URL, error, or context cancellation
	select {
	case url := <-urlChan:
		t.mu.Lock()
		t.url = url
		t.state = TunnelStateRunning
		t.startedAt = time.Now()
		t.mu.Unlock()

		t.log.Info("Cloudflare tunnel started", "url", url, "local_port", t.localPort)

		// Start goroutine to monitor process exit
		t.waitGroup.Add(1)
		go t.monitorProcess()

		return url, nil

	case err := <-errChan:
		t.Stop()
		t.setError(err)
		return "", err

	case <-ctx.Done():
		t.Stop()
		t.setError(ctx.Err())
		return "", ctx.Err()

	case <-time.After(60 * time.Second):
		t.Stop()
		err := fmt.Errorf("timeout waiting for tunnel URL (60s)")
		t.setError(err)
		return "", err
	}
}

// parseStderr reads cloudflared stderr and extracts the tunnel URL.
func (t *CloudflareTunnel) parseStderr(stderr io.ReadCloser, urlChan chan<- string, errChan chan<- error) {
	scanner := bufio.NewScanner(stderr)
	urlFound := false

	for scanner.Scan() {
		line := scanner.Text()
		t.log.Debug("cloudflared output", "line", line)

		// Look for the tunnel URL
		if !urlFound {
			if match := t.urlPattern.FindString(line); match != "" {
				urlChan <- match
				urlFound = true
				// Continue reading to keep the pipe drained
			}
		}

		// Check for common error patterns
		if containsError(line) {
			errChan <- fmt.Errorf("cloudflared error: %s", line)
			return
		}
	}

	if err := scanner.Err(); err != nil && !urlFound {
		errChan <- fmt.Errorf("error reading cloudflared output: %w", err)
	}
}

// containsError checks if a log line indicates an error condition.
func containsError(line string) bool {
	errorPatterns := []string{
		"ERR ",
		"failed to request quick Tunnel",
		"Could not serve",
		"Unable to reach",
		"connection refused",
	}
	lineLower := strings.ToLower(line)
	for _, pattern := range errorPatterns {
		if strings.Contains(lineLower, strings.ToLower(pattern)) {
			return true
		}
	}
	return false
}

// monitorProcess watches for cloudflared process exit.
func (t *CloudflareTunnel) monitorProcess() {
	defer t.waitGroup.Done()

	t.mu.RLock()
	cmd := t.cmd
	t.mu.RUnlock()

	if cmd == nil {
		return
	}

	err := cmd.Wait()

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.state == TunnelStateStopping {
		// Expected shutdown
		t.state = TunnelStateStopped
		return
	}

	// Unexpected exit
	t.state = TunnelStateError
	if err != nil {
		t.lastError = fmt.Errorf("cloudflared process exited unexpectedly: %w", err)
	} else {
		t.lastError = errors.New("cloudflared process exited unexpectedly")
	}
	t.log.Error("Cloudflare tunnel stopped unexpectedly", "error", t.lastError)
}

// Stop terminates the tunnel subprocess gracefully.
func (t *CloudflareTunnel) Stop() error {
	t.mu.Lock()

	if t.state == TunnelStateStopped || t.state == TunnelStateStopping {
		t.mu.Unlock()
		return nil
	}

	t.state = TunnelStateStopping
	cancel := t.cancelFn
	cmd := t.cmd
	t.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	// Give the process time to exit gracefully
	done := make(chan struct{})
	go func() {
		t.waitGroup.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Process exited
	case <-time.After(10 * time.Second):
		// Force kill if still running
		if cmd != nil && cmd.Process != nil {
			t.log.Warn("Force killing cloudflared process")
			_ = cmd.Process.Kill()
		}
	}

	t.mu.Lock()
	t.state = TunnelStateStopped
	t.url = ""
	t.cmd = nil
	t.cancelFn = nil
	t.mu.Unlock()

	t.log.Info("Cloudflare tunnel stopped")
	return nil
}

// Status returns the current tunnel status.
func (t *CloudflareTunnel) Status() TunnelStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()

	status := TunnelStatus{
		State:     t.state,
		URL:       t.url,
		LocalPort: t.localPort,
		StartedAt: t.startedAt,
	}

	if t.lastError != nil {
		status.Error = t.lastError.Error()
	}

	return status
}

// URL returns the public tunnel URL, or empty string if not running.
func (t *CloudflareTunnel) URL() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.url
}

// IsRunning returns true if the tunnel is currently active.
func (t *CloudflareTunnel) IsRunning() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.state == TunnelStateRunning
}

// HealthCheck verifies the tunnel is working by checking process state.
func (t *CloudflareTunnel) HealthCheck() error {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.state != TunnelStateRunning {
		return ErrTunnelNotRunning
	}

	if t.cmd == nil || t.cmd.Process == nil {
		return errors.New("tunnel process not found")
	}

	// On Unix, sending signal 0 checks if process exists
	// On Windows, we just verify the process handle is valid
	if t.cmd.ProcessState != nil && t.cmd.ProcessState.Exited() {
		return errors.New("tunnel process has exited")
	}

	return nil
}

// setError sets the tunnel to error state.
func (t *CloudflareTunnel) setError(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.state = TunnelStateError
	t.lastError = err
	t.log.Error("Cloudflare tunnel error", "error", err)
}

// Restart stops and starts the tunnel again.
func (t *CloudflareTunnel) Restart(ctx context.Context) (string, error) {
	if err := t.Stop(); err != nil {
		return "", fmt.Errorf("failed to stop tunnel: %w", err)
	}

	// Wait a moment before restarting
	time.Sleep(time.Second)

	return t.Start(ctx)
}
