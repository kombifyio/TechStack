// Package testutil provides integration test utilities for kombifyTechstack.
// Integration tests require a Docker-compatible engine, locally or through DOCKER_HOST.
package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcnetwork "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

// kombifyTechstackContainer holds a running kombifyTechstack container for integration tests.
type kombifyTechstackContainer struct {
	Container testcontainers.Container
	BaseURL   string // e.g., http://localhost:5260
	GRPCAddr  string // e.g., localhost:5263
}

// KombifyTechstackContainer exposes the integration test container handle to
// external test packages.
type KombifyTechstackContainer = kombifyTechstackContainer

var (
	imageBuildMu          sync.Mutex
	imageBuiltThisProcess bool
)

// ensureImageBuilt builds the kombifyTechstack Docker image once per test
// process by default. Set TECHSTACK_TEST_REUSE_IMAGE=1 only when an outer gate
// has already built techstack:test for the current workspace.
func ensureImageBuilt(t *testing.T) {
	t.Helper()
	imageBuildMu.Lock()
	defer imageBuildMu.Unlock()

	projectRoot, err := GetProjectRoot()
	if err != nil {
		t.Fatalf("cannot find project root: %v", err)
	}

	forceBuild := os.Getenv("TECHSTACK_TEST_FORCE_IMAGE_BUILD") == "1"
	reuseExisting := os.Getenv("TECHSTACK_TEST_REUSE_IMAGE") == "1"
	if imageBuiltThisProcess {
		t.Log("Using techstack:test image built earlier in this test process")
		return
	}

	if reuseExisting && !forceBuild {
		checkCmd := exec.Command("docker", "images", "-q", "techstack:test")
		output, _ := checkCmd.Output()
		if len(strings.TrimSpace(string(output))) > 0 {
			t.Log("Using externally built techstack:test image")
			return
		}
	}

	t.Log("Building techstack:test image (this may take a moment)...")
	buildArgs := []string{"build", "-t", "techstack:test"}
	if tokenEnv := githubTokenEnvName(); tokenEnv != "" {
		buildArgs = append(buildArgs, "--secret", "id=GITHUB_TOKEN,env="+tokenEnv)
	}
	buildArgs = append(buildArgs, ".")
	buildCmd := exec.Command("docker", buildArgs...)
	buildCmd.Dir = projectRoot
	buildCmd.Env = append(os.Environ(), "DOCKER_BUILDKIT=1")
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr

	if err := buildCmd.Run(); err != nil {
		t.Fatalf("failed to build Docker image: %v", err)
	}
	imageBuiltThisProcess = true
	t.Log("Image built successfully")
}

func githubTokenEnvName() string {
	for _, key := range []string{"GITHUB_TOKEN", "GH_TOKEN", "GH_PAT", "GITHUB_PAT"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return key
		}
	}
	return ""
}

// StartkombifyTechstack starts a kombifyTechstack container for integration testing.
// Uses pre-built techstack:test image for speed.
func StartkombifyTechstack(t *testing.T, ctx context.Context) *kombifyTechstackContainer {
	return StartkombifyTechstackWithEnv(t, ctx, nil)
}

// StartkombifyTechstackWithEnv starts a kombifyTechstack container for
// integration testing with additional environment overrides.
func StartkombifyTechstackWithEnv(t *testing.T, ctx context.Context, extraEnv map[string]string) *kombifyTechstackContainer {
	return StartkombifyTechstackWithEnvAndRequestMutator(t, ctx, extraEnv, nil)
}

// StartkombifyTechstackWithEnvAndRequestMutator starts a kombifyTechstack
// container with environment overrides and an optional request mutator for
// integration tests that need custom networks or similar container settings.
func StartkombifyTechstackWithEnvAndRequestMutator(t *testing.T, ctx context.Context, extraEnv map[string]string, mutator func(*testcontainers.ContainerRequest)) *kombifyTechstackContainer {
	t.Helper()
	RequireDocker(t)

	// Ensure image is built (uses cached image if available)
	ensureImageBuilt(t)

	testNetwork, err := tcnetwork.New(ctx)
	if err != nil {
		t.Fatalf("failed to create TechStack integration test network: %v", err)
	}
	testcontainers.CleanupNetwork(t, testNetwork)

	env := map[string]string{
		"TECHSTACK_ENV":                 "dev",
		"TECHSTACK_LOG_LEVEL":           "warn",
		"TECHSTACK_HTTP_ADDR":           ":5260",
		"TECHSTACK_GRPC_ADDR":           ":5263",
		"TECHSTACK_V2_SESSION_AUDIENCE": "techstack-integration",
		"TECHSTACK_V2_SESSION_SECRET":   "techstack-integration-session-secret-32",
	}
	if dsn := strings.TrimSpace(os.Getenv("TECHSTACK_TEST_DATABASE_URL")); dsn != "" {
		env["DATABASE_URL"] = dsn
	}
	for key, value := range extraEnv {
		env[key] = value
	}
	if strings.TrimSpace(env["DATABASE_URL"]) == "" {
		startIntegrationPostgres(t, ctx, testNetwork.Name)
		env["DATABASE_URL"] = "postgres://techstack:techstack@techstack-postgres:5432/techstack?sslmode=disable"
	}

	req := testcontainers.ContainerRequest{
		Image:        "techstack:test",
		ExposedPorts: []string{"5260/tcp", "5263/tcp"},
		Env:          env,
		Cmd:          []string{"serve"},
		Networks:     []string{testNetwork.Name},
		NetworkAliases: map[string][]string{
			testNetwork.Name: {"techstack"},
		},
		WaitingFor: wait.ForAll(
			wait.ForHTTP("/api/v1/health").WithPort("5260/tcp").WithStartupTimeout(60*time.Second),
			wait.ForListeningPort("5263/tcp").WithStartupTimeout(60*time.Second),
		),
	}
	if mutator != nil {
		mutator(&req)
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("failed to start kombifyTechstack container: %v", err)
	}

	// Get mapped ports
	httpHost, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("failed to get container host: %v", err)
	}

	httpPort, err := container.MappedPort(ctx, "5260")
	if err != nil {
		t.Fatalf("failed to get HTTP port: %v", err)
	}

	grpcPort, err := container.MappedPort(ctx, "5263")
	if err != nil {
		t.Fatalf("failed to get gRPC port: %v", err)
	}

	ks := &kombifyTechstackContainer{
		Container: container,
		BaseURL:   fmt.Sprintf("http://%s:%s", httpHost, httpPort.Port()),
		GRPCAddr:  fmt.Sprintf("%s:%s", httpHost, grpcPort.Port()),
	}

	t.Logf("kombifyTechstack container started: API=%s, gRPC=%s", ks.BaseURL, ks.GRPCAddr)

	// Register cleanup
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Logf("warning: failed to terminate container: %v", err)
		}
	})

	return ks
}

func startIntegrationPostgres(t *testing.T, ctx context.Context, networkName string) {
	t.Helper()
	req := testcontainers.ContainerRequest{
		Image:        "postgres:16-alpine",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_DB":       "techstack",
			"POSTGRES_USER":     "techstack",
			"POSTGRES_PASSWORD": "techstack",
		},
		Networks: []string{networkName},
		NetworkAliases: map[string][]string{
			networkName: {"techstack-postgres"},
		},
		WaitingFor: wait.ForListeningPort("5432/tcp").WithStartupTimeout(60 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("failed to start Postgres test container: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Logf("warning: failed to terminate Postgres test container: %v", err)
		}
	})
}

// HealthCheck verifies the kombifyTechstack container is healthy.
func (ks *kombifyTechstackContainer) HealthCheck(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ks.BaseURL+"/api/v1/health", nil)
	if err != nil {
		t.Fatalf("failed to create health request: %v", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("health check returned %d: %s", resp.StatusCode, string(body))
	}

	t.Log("kombifyTechstack health check passed")
}

// APIRequest performs an HTTP request to the kombifyTechstack API.
func (ks *kombifyTechstackContainer) APIRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	url := ks.BaseURL + path
	if !strings.HasPrefix(path, "/") {
		url = ks.BaseURL + "/" + path
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	return http.DefaultClient.Do(req)
}

// AuthenticatedAPIRequest performs an HTTP request with a TechStack session token.
func (ks *kombifyTechstackContainer) AuthenticatedAPIRequest(ctx context.Context, method, path string, body io.Reader, token string) (*http.Response, error) {
	url := ks.BaseURL + path
	if !strings.HasPrefix(path, "/") {
		url = ks.BaseURL + "/" + path
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if token = normalizeBearerToken(token); token != "" {
		req.Header.Set("Authorization", token)
	}
	return http.DefaultClient.Do(req)
}

type breakglassRevealResponse struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type localLoginResponse struct {
	OK       bool   `json:"ok"`
	Email    string `json:"email"`
	Provider string `json:"provider"`
}

// Authenticate logs in through TechStack's V2 local break-glass flow and returns
// the session JWT. The legacy PocketBase users collection is no longer an auth
// authority for integration tests.
func (ks *kombifyTechstackContainer) Authenticate(t *testing.T, ctx context.Context) string {
	t.Helper()

	creds := ks.revealBreakglassCredentials(t, ctx)
	authPayload, err := json.Marshal(map[string]string{
		"email":    creds.Email,
		"password": creds.Password,
	})
	if err != nil {
		t.Fatalf("failed to encode auth request: %v", err)
	}

	resp, err := ks.APIRequest(ctx, http.MethodPost, "/api/v1/auth/login", strings.NewReader(string(authPayload)))
	if err != nil {
		t.Fatalf("auth request failed: %v", err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("auth failed (HTTP %d): %s", resp.StatusCode, string(respBytes))
	}

	var result localLoginResponse
	if err := json.Unmarshal(respBytes, &result); err != nil {
		t.Fatalf("failed to parse auth response: %v", err)
	}
	if !result.OK {
		t.Fatalf("auth response did not confirm login: %s", string(respBytes))
	}

	for _, cookie := range resp.Cookies() {
		if strings.TrimSpace(cookie.Value) != "" {
			t.Logf("Authenticated as %s via TechStack local auth", result.Email)
			return cookie.Value
		}
	}
	t.Fatalf("auth response did not include a session cookie: %s", string(respBytes))
	return ""
}

func (ks *kombifyTechstackContainer) revealBreakglassCredentials(t *testing.T, ctx context.Context) breakglassRevealResponse {
	t.Helper()

	resp, err := ks.APIRequest(ctx, http.MethodGet, "/api/v1/auth/breakglass/reveal", nil)
	if err != nil {
		t.Fatalf("break-glass reveal request failed: %v", err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("break-glass reveal failed (HTTP %d): %s", resp.StatusCode, string(respBytes))
	}

	var result breakglassRevealResponse
	if err := json.Unmarshal(respBytes, &result); err != nil {
		t.Fatalf("failed to parse break-glass reveal response: %v", err)
	}
	if strings.TrimSpace(result.Email) == "" || strings.TrimSpace(result.Password) == "" {
		t.Fatalf("break-glass reveal returned incomplete credentials: %s", string(respBytes))
	}
	return result
}

func normalizeBearerToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(token), "bearer ") {
		return token
	}
	return "Bearer " + token
}

// Logs returns the container logs for debugging.
func (ks *kombifyTechstackContainer) Logs(ctx context.Context) (string, error) {
	reader, err := ks.Container.Logs(ctx)
	if err != nil {
		return "", err
	}
	defer reader.Close()

	logs, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(logs), nil
}
