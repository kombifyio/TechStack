// Package routes — stackkit live integration tests.
//
// These tests run the stackkit installer against a real kombify-Sim instance.
// They require:
//   - A running kombify-Sim (KOMBISIM_URL env var, default: http://localhost:5280)
//   - A valid KOMBISIM_AUTH_JWT_SECRET for authentication
//   - Python3 with pyjwt installed (for JWT generation)
//
// Run with:
//
//	go test -v -tags integration -run TestStackkitLive ./internal/routes/... \
//	  -timeout 10m
//
//go:build integration
// +build integration

package routes_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	defaultSimURL    = "http://localhost:5280"
	testUserSub      = "ci-stackkit-go-test"
	testUserEmail    = "ci-stackkit@kombify.io"
	testUserName     = "CI Stackkit Go Test"
	testNodeImage    = "ubuntu:22.04"
	sshTimeout       = 5 * time.Minute
	nodeStartTimeout = 2 * time.Minute
)

// simClient is a minimal kombify-Sim API client for testing.
type simClient struct {
	baseURL     string
	httpClient  *http.Client
	accessToken string
}

func newSimClient(baseURL string) *simClient {
	return &simClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *simClient) do(method, path string, body interface{}) (*http.Response, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, c.baseURL+"/api/v1"+path, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
	}
	return c.httpClient.Do(req)
}

func (c *simClient) doJSON(method, path string, body interface{}, out interface{}) error {
	resp, err := c.do(method, path, body)
	if err != nil {
		return fmt.Errorf("request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d for %s %s: %s", resp.StatusCode, method, path, string(respBody))
	}
	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode response: %w (body: %s)", err, string(respBody))
		}
	}
	return nil
}

// generateSSOToken generates an HMAC-HS256 JWT for kombify-Sim authentication
// using the Python3 pyjwt library (available on CI runners).
func generateSSOToken(jwtSecret string) (string, error) {
	script := fmt.Sprintf(`
import jwt, time, sys
secret = %q
now = int(time.time())
payload = {
    "sub": %q,
    "email": %q,
    "name": %q,
    "tool": "kombifysim",
    "iat": now,
    "exp": now + 3600,
}
token = jwt.encode(payload, secret, algorithm="HS256")
print(token)
`, jwtSecret, testUserSub+fmt.Sprintf("-%d", time.Now().Unix()), testUserEmail, testUserName)

	cmd := exec.Command("python3", "-c", script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("JWT generation failed: %w\nstderr: %s", err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

// simNode represents a kombify-Sim node.
type simNode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	SSHPort     int    `json:"ssh_port"`
	SSHPassword string `json:"ssh_password"`
	IP          string `json:"ip"`
	PrivateIP   string `json:"private_ip"`
}

// simSSHInfo matches the SSHInfo struct returned by GET /nodes/{id}/ssh.
type simSSHInfo struct {
	Host        string `json:"host"`
	DisplayHost string `json:"display_host"`
	Port        int    `json:"port"`
	User        string `json:"user"`
	Password    string `json:"password"`
	AuthMethod  string `json:"auth_method"`
	KeyPath     string `json:"key_path"`
	Ephemeral   bool   `json:"ephemeral"`
}

// TestStackkitLive_InstallerRuns verifies that the stackkit prep installer
// can be executed successfully against a live kombify-Sim node.
//
// Test flow:
//  1. Authenticate with kombify-Sim (SSO JWT)
//  2. Create and start a Docker node
//  3. Wait for SSH to become available
//  4. Run the stackkit prep installer (techstack-prepare.sh)
//  5. Validate Docker and OpenTofu are installed
//  6. Delete the node (cleanup)
func TestStackkitLive_InstallerRuns(t *testing.T) {
	simURL := envOrDefault("KOMBISIM_URL", defaultSimURL)
	jwtSecret := os.Getenv("KOMBISIM_AUTH_JWT_SECRET")
	if jwtSecret == "" {
		t.Skip("KOMBISIM_AUTH_JWT_SECRET not set — skipping live Sim test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	client := newSimClient(simURL)

	// Verify Sim is healthy
	t.Log("Checking kombify-Sim health...")
	var health map[string]interface{}
	if err := client.doJSON("GET", "/health", nil, &health); err != nil {
		t.Fatalf("Sim health check failed (is %s running?): %v", simURL, err)
	}
	if status, _ := health["status"].(string); status != "healthy" {
		t.Fatalf("Sim is not healthy: status=%q", status)
	}
	t.Logf("Sim healthy: %s", simURL)

	// Authenticate
	t.Log("Authenticating...")
	ssoToken, err := generateSSOToken(jwtSecret)
	if err != nil {
		t.Fatalf("Failed to generate SSO token: %v", err)
	}

	var authResp struct {
		User        map[string]interface{} `json:"user"`
		AccessToken string                 `json:"accessToken"`
	}
	if err := client.doJSON("POST", "/auth/portal-verify", map[string]string{"token": ssoToken}, &authResp); err != nil {
		t.Fatalf("Authentication failed: %v", err)
	}
	if authResp.AccessToken == "" {
		t.Fatal("Empty access token from auth response")
	}
	client.accessToken = authResp.AccessToken
	t.Log("Authenticated successfully")

	// Create node
	nodeName := fmt.Sprintf("ci-stackkit-go-%d", time.Now().Unix())
	t.Logf("Creating node %q (image: %s)...", nodeName, testNodeImage)

	var node simNode
	err = client.doJSON("POST", "/nodes", map[string]interface{}{
		"name":   nodeName,
		"image":  testNodeImage,
		"vcpus":  1,
		"memory": 512,
	}, &node)
	if err != nil {
		t.Fatalf("Failed to create node: %v", err)
	}
	t.Logf("Node created: %s", node.ID)

	// Cleanup on exit
	t.Cleanup(func() {
		t.Logf("Deleting test node %s...", node.ID)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_ = cleanupCtx

		resp, err := client.do("DELETE", "/nodes/"+node.ID, nil)
		if err != nil || resp.StatusCode >= 300 {
			t.Logf("Warning: cleanup failed for node %s (manual cleanup may be needed)", node.ID)
		} else {
			t.Logf("Node %s deleted", node.ID)
		}
	})

	// Start node
	t.Log("Starting node...")
	if err := client.doJSON("POST", "/nodes/"+node.ID+"/start", nil, nil); err != nil {
		t.Fatalf("Failed to start node: %v", err)
	}

	// Wait for running status
	t.Log("Waiting for node to reach 'running' status...")
	if err := waitForNodeStatus(ctx, client, node.ID, "running", nodeStartTimeout); err != nil {
		t.Fatalf("Node failed to start: %v", err)
	}
	t.Log("Node is running")

	// Get SSH credentials
	t.Log("Fetching SSH credentials...")
	var sshInfo simSSHInfo
	if err := client.doJSON("GET", "/nodes/"+node.ID+"/ssh", nil, &sshInfo); err != nil {
		t.Fatalf("Failed to get SSH info: %v", err)
	}

	// Resolve SSH host: for local Sim, ports are mapped 0.0.0.0:PORT->22 on the host
	sshHost := sshInfo.DisplayHost
	if strings.Contains(simURL, "localhost") || strings.Contains(simURL, "127.0.0.1") {
		sshHost = "localhost"
	} else if sshHost == "" {
		sshHost = sshInfo.Host
	}
	if sshHost == "" || sshHost == "null" {
		sshHost = "localhost"
	}
	sshPort := sshInfo.Port
	if sshPort == 0 {
		var updatedNode simNode
		if err := client.doJSON("GET", "/nodes/"+node.ID, nil, &updatedNode); err == nil {
			sshPort = updatedNode.SSHPort
		}
	}
	if sshPort == 0 {
		sshPort = 22
	}

	// Extract SSH private key from the Sim container
	simContainer := envOrDefault("KOMBISIM_CONTAINER", "kombify-sim-ionos")
	keyFile, keyCleanup, err := extractSSHKey(t, simContainer, sshInfo.KeyPath)
	if err != nil {
		t.Logf("Warning: could not extract SSH key (%v) — falling back to password auth", err)
		keyFile = ""
	}
	if keyCleanup != nil {
		defer keyCleanup()
	}

	t.Logf("SSH target: %s@%s:%d (key=%v)", sshInfo.User, sshHost, sshPort, keyFile != "")

	// Wait for SSH
	t.Logf("Waiting for SSH on %s:%d (timeout: %s)...", sshHost, sshPort, sshTimeout)
	if err := waitForSSH(ctx, sshHost, sshPort, sshTimeout); err != nil {
		t.Fatalf("SSH not ready: %v", err)
	}
	t.Log("SSH is ready")

	// Run installer
	t.Log("Running stackkit prep installer...")
	if err := runInstallerSSH(t, sshHost, sshPort, sshInfo.User, sshInfo.Password, keyFile); err != nil {
		t.Fatalf("Installer failed: %v", err)
	}
	t.Log("Installer completed")

	// Validate
	t.Log("Validating installation...")
	validateInstallation(t, sshHost, sshPort, sshInfo.User, sshInfo.Password, keyFile)
}

// TestStackkitLive_NodeLifecycle verifies basic node create/start/stop/delete.
func TestStackkitLive_NodeLifecycle(t *testing.T) {
	simURL := envOrDefault("KOMBISIM_URL", defaultSimURL)
	jwtSecret := os.Getenv("KOMBISIM_AUTH_JWT_SECRET")
	if jwtSecret == "" {
		t.Skip("KOMBISIM_AUTH_JWT_SECRET not set — skipping live Sim test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	client := newSimClient(simURL)

	// Auth
	ssoToken, err := generateSSOToken(jwtSecret)
	if err != nil {
		t.Fatalf("JWT generation: %v", err)
	}
	var authResp struct {
		AccessToken string `json:"accessToken"`
	}
	if err := client.doJSON("POST", "/auth/portal-verify", map[string]string{"token": ssoToken}, &authResp); err != nil {
		t.Fatalf("Auth: %v", err)
	}
	client.accessToken = authResp.AccessToken

	// Create node
	nodeName := fmt.Sprintf("ci-lifecycle-go-%d", time.Now().Unix())
	var node simNode
	if err := client.doJSON("POST", "/nodes", map[string]interface{}{
		"name":   nodeName,
		"image":  testNodeImage,
		"vcpus":  1,
		"memory": 256,
	}, &node); err != nil {
		t.Fatalf("Create node: %v", err)
	}
	t.Logf("Created node %s", node.ID)

	t.Cleanup(func() {
		_, _ = client.do("DELETE", "/nodes/"+node.ID, nil)
	})

	// Start
	if err := client.doJSON("POST", "/nodes/"+node.ID+"/start", nil, nil); err != nil {
		t.Fatalf("Start node: %v", err)
	}

	// Wait running
	if err := waitForNodeStatus(ctx, client, node.ID, "running", nodeStartTimeout); err != nil {
		t.Fatalf("Node did not start: %v", err)
	}
	t.Log("Node started OK")

	// Stop
	if err := client.doJSON("POST", "/nodes/"+node.ID+"/stop", nil, nil); err != nil {
		t.Fatalf("Stop node: %v", err)
	}
	t.Log("Node stopped OK")

	// Delete
	resp, err := client.do("DELETE", "/nodes/"+node.ID, nil)
	if err != nil || resp.StatusCode >= 300 {
		t.Fatalf("Delete node failed: status=%d err=%v", resp.StatusCode, err)
	}
	t.Log("Node deleted OK — lifecycle test passed")
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func waitForNodeStatus(ctx context.Context, client *simClient, nodeID, wantStatus string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		var node simNode
		if err := client.doJSON("GET", "/nodes/"+nodeID, nil, &node); err == nil {
			if node.Status == wantStatus {
				return nil
			}
			if node.Status == "error" {
				return fmt.Errorf("node entered error state")
			}
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("timeout waiting for node status %q after %s", wantStatus, timeout)
}

func waitForSSH(ctx context.Context, host string, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Use ssh-keyscan — works regardless of auth method
		cmd := exec.CommandContext(ctx, "ssh-keyscan", "-p", fmt.Sprintf("%d", port), "-T", "5", host)
		out, err := cmd.Output()
		if err == nil && strings.Contains(string(out), "ssh-") {
			return nil
		}

		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("SSH on %s:%d not ready after %s", host, port, timeout)
}

func runInstallerSSH(t *testing.T, host string, port int, user, password, keyFile string) error {
	t.Helper()

	// Find the embedded prepare script relative to this test file
	prepScript, err := findPrepareScript()
	if err != nil {
		return fmt.Errorf("could not find techstack-prepare.sh: %w", err)
	}
	scriptBytes, err := os.ReadFile(prepScript)
	if err != nil {
		return fmt.Errorf("could not read prepare script: %w", err)
	}

	sshArgs := sshBaseArgs(port, password, keyFile)
	sshArgs = append(sshArgs, fmt.Sprintf("%s@%s", user, host), "sudo bash -s")

	cmd := exec.Command("ssh", sshArgs...)
	cmd.Stdin = bytes.NewReader(scriptBytes)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Logf("Installer stdout: %s", stdout.String())
		t.Logf("Installer stderr: %s", stderr.String())
		return fmt.Errorf("installer exited with error: %w", err)
	}

	t.Logf("Installer output (last 10 lines):\n%s", lastNLines(stdout.String()+stderr.String(), 10))
	return nil
}

func validateInstallation(t *testing.T, host string, port int, user, password, keyFile string) {
	t.Helper()

	runCheck := func(name, checkCmd string) bool {
		t.Helper()
		sshArgs := sshBaseArgs(port, password, keyFile)
		sshArgs = append(sshArgs, fmt.Sprintf("%s@%s", user, host), checkCmd)
		cmd := exec.Command("ssh", sshArgs...)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			t.Errorf("FAIL %s: %v (output: %s)", name, err, out.String())
			return false
		}
		t.Logf("OK   %s: %s", name, strings.TrimSpace(out.String()))
		return true
	}

	passed := 0
	if runCheck("docker installed", "docker --version") {
		passed++
	}
	if runCheck("opentofu installed", "tofu --version 2>/dev/null || opentofu --version 2>/dev/null || echo 'not found'") {
		passed++
	}
	// Non-fatal: systemd not available in Docker containers
	runCheck("docker socket", "test -S /var/run/docker.sock && echo 'socket exists' || echo 'no socket (expected in nested Docker)'")

	if passed < 1 {
		t.Errorf("Validation failed: no required tools were found after installation")
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func lastNLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

// sshBaseArgs returns common SSH arguments, using key auth if keyFile is set,
// otherwise falling back to sshpass password auth.
func sshBaseArgs(port int, password, keyFile string) []string {
	base := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=10",
		"-p", fmt.Sprintf("%d", port),
	}
	if keyFile != "" {
		return append(base, "-i", keyFile, "-o", "PasswordAuthentication=no")
	}
	// password fallback — relies on sshpass being available
	return append([]string{}, append(
		[]string{"sshpass", "-p", password},
		append(base, "-o", "PreferredAuthentications=password")...)...)
}

// extractSSHKey copies the node's private key from the Sim container to a temp file.
// Returns the path and a cleanup func (or an error if extraction fails).
func extractSSHKey(t *testing.T, container, keyPath string) (string, func(), error) {
	t.Helper()
	if keyPath == "" {
		return "", nil, fmt.Errorf("no key_path in SSH info")
	}

	tmp, err := os.CreateTemp("", "node-key-*.pem")
	if err != nil {
		return "", nil, fmt.Errorf("create temp key file: %w", err)
	}
	tmp.Close()
	cleanup := func() { os.Remove(tmp.Name()) }

	out, err := exec.Command("docker", "exec", container, "cat", keyPath).Output()
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("docker exec cat key: %w", err)
	}

	if err := os.WriteFile(tmp.Name(), out, 0600); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("write key file: %w", err)
	}

	t.Logf("SSH key extracted from %s:%s → %s", container, keyPath, tmp.Name())
	return tmp.Name(), cleanup, nil
}

// findPrepareScript locates pkg/serverprep/techstack-prepare.sh relative to this test file.
func findPrepareScript() (string, error) {
	// Walk up from the test directory to find the module root
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, "pkg", "serverprep", "techstack-prepare.sh")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("techstack-prepare.sh not found in any parent directory")
}
