package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/identity"
	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/kombifyio/techstack/internal/runtimeproduct/runtimeaction"
	"github.com/kombifyio/techstack/pkg/serviceauth"
)

const (
	runtimeActionTargetSimulate = runtimeaction.TargetSimulate

	runtimeActionSimulateUpdate = string(runtimeaction.ActionSimulateUpdate)

	defaultRuntimeActionPathPrefix = runtimeaction.PathPrefix
	defaultSimulationGatePath      = runtimeaction.PathSimulateUpdate
	runtimeActionServiceName       = "techstack"
	runtimeActionHTTPTimeout       = 14*time.Minute + 30*time.Second
)

type HTTPRuntimeActionRunnerConfig struct {
	BaseURL           string
	Target            string
	Action            string
	Path              string
	ServiceAuthSecret string
	ServiceAuthNext   string
	HTTPClient        *http.Client
}

// RuntimeActionResponseError preserves bounded non-2xx response diagnostics for callers.
type RuntimeActionResponseError struct {
	Action     string
	StatusCode int
	Body       string
}

func (e *RuntimeActionResponseError) Error() string {
	return fmt.Sprintf("runtime action %s returned %d: %s", e.Action, e.StatusCode, e.Body)
}

type HTTPRuntimeActionRunner struct {
	baseURL string
	path    string
	action  string
	target  string
	client  *servicecall.Client
}

type RuntimeActionsEnvDiagnostics struct {
	Configured []string
	Warnings   []string
}

type localSimulationGateRunner struct{}

func (localSimulationGateRunner) Run(ctx context.Context, req RuntimeActionRequest) error {
	return nil
}

func NewHTTPRuntimeActionRunner(cfg HTTPRuntimeActionRunnerConfig) (*HTTPRuntimeActionRunner, error) {
	baseURL, err := normalizeRuntimeActionBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	target := strings.TrimSpace(cfg.Target)
	if target == "" {
		return nil, fmt.Errorf("runtime action target required")
	}
	action := strings.TrimSpace(cfg.Action)
	if action == "" {
		return nil, fmt.Errorf("runtime action name required")
	}
	path := normalizeRuntimeActionPath(cfg.Path)
	if path == "" {
		return nil, fmt.Errorf("runtime action path required")
	}
	authCfg, err := serviceauth.Apply(servicecall.Config{
		ServiceName: runtimeActionServiceName,
		Target:      target,
		Secret:      cfg.ServiceAuthSecret,
		SecretNext:  cfg.ServiceAuthNext,
	})
	if err != nil {
		return nil, fmt.Errorf("runtime action service auth: %w", err)
	}
	client, err := servicecall.NewClient(authCfg)
	if err != nil {
		return nil, err
	}
	return &HTTPRuntimeActionRunner{
		baseURL: baseURL,
		path:    path,
		action:  action,
		target:  target,
		client:  client.WithHTTPClient(runtimeActionHTTPClient(cfg.HTTPClient)),
	}, nil
}

func runtimeActionHTTPClient(configured *http.Client) *http.Client {
	if configured != nil {
		return configured
	}
	return &http.Client{Timeout: runtimeActionHTTPTimeout}
}

func (r *HTTPRuntimeActionRunner) Run(ctx context.Context, req RuntimeActionRequest) error {
	_, err := r.RunWithResult(ctx, req)
	return err
}

func (r *HTTPRuntimeActionRunner) RuntimeActionDescriptor() RuntimeActionDescriptor {
	if r == nil {
		return RuntimeActionDescriptor{}
	}
	return RuntimeActionDescriptor{
		Action:  r.action,
		Target:  r.target,
		BaseURL: r.baseURL,
		Path:    r.path,
	}
}

func (r *HTTPRuntimeActionRunner) RunWithResult(ctx context.Context, req RuntimeActionRequest) (map[string]interface{}, error) {
	if r == nil || r.client == nil {
		return nil, fmt.Errorf("runtime action %q is not configured", runtimeActionName(r))
	}
	payload := RuntimeActionRequest{
		Action:              runtimeaction.NormalizeAction(r.action),
		StackID:             strings.TrimSpace(req.StackID),
		StackName:           strings.TrimSpace(req.StackName),
		StackKit:            strings.TrimSpace(req.StackKit),
		TenantID:            strings.TrimSpace(req.TenantID),
		OwnerID:             strings.TrimSpace(req.OwnerID),
		StackSpec:           req.StackSpec,
		StackSpecPath:       strings.TrimSpace(req.StackSpecPath),
		TofuDir:             strings.TrimSpace(req.TofuDir),
		UnifiedPath:         strings.TrimSpace(req.UnifiedPath),
		OwnerSpecBootstrap:  normalizeOwnerSpecBootstrap(req.OwnerSpecBootstrap),
		RuntimeTarget:       normalizeRuntimeActionTarget(req.RuntimeTarget),
		PlatformNodes:       normalizePlatformNodes(req.PlatformNodes),
		PreviewPolicy:       normalizePreviewPolicy(req.PreviewPolicy),
		TechStackEnrollment: normalizeTechStackEnrollment(req.TechStackEnrollment),
	}
	resp, err := r.client.PostJSON(ctx, r.baseURL+r.path, payload, servicecall.OnBehalfOfIdentity(identity.FromContext(ctx)))
	if err != nil {
		return nil, fmt.Errorf("runtime action %s request failed: %w", r.action, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &RuntimeActionResponseError{
			Action:     r.action,
			StatusCode: resp.StatusCode,
			Body:       strings.TrimSpace(string(body)),
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("runtime action %s response read failed: %w", r.action, err)
	}
	body = []byte(strings.TrimSpace(string(body)))
	if len(body) == 0 {
		return nil, nil
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("runtime action %s returned invalid JSON: %w", r.action, err)
	}
	if data, ok := decoded["data"].(map[string]interface{}); ok {
		return data, nil
	}
	return decoded, nil
}

func normalizeTechStackEnrollment(in *TechStackEnrollment) *TechStackEnrollment {
	if in == nil {
		return nil
	}
	out := &TechStackEnrollment{
		TenantID:         strings.TrimSpace(in.TenantID),
		OwnerID:          strings.TrimSpace(in.OwnerID),
		StackID:          strings.TrimSpace(in.StackID),
		LeaseID:          strings.TrimSpace(in.LeaseID),
		ServerURL:        strings.TrimRight(strings.TrimSpace(in.ServerURL), "/"),
		ServerID:         strings.TrimSpace(in.ServerID),
		RuntimeAgentID:   strings.TrimSpace(in.RuntimeAgentID),
		AgentToken:       strings.TrimSpace(in.AgentToken),
		HeartbeatURL:     strings.TrimSpace(in.HeartbeatURL),
		InventoryURL:     strings.TrimSpace(in.InventoryURL),
		ChannelBootstrap: cloneAnyMap(in.ChannelBootstrap),
	}
	if !techStackEnrollmentRequested(out) {
		return nil
	}
	return out
}

func techStackEnrollmentRequested(in *TechStackEnrollment) bool {
	if in == nil {
		return false
	}
	return in.TenantID != "" ||
		in.OwnerID != "" ||
		in.StackID != "" ||
		in.LeaseID != "" ||
		in.ServerURL != "" ||
		in.ServerID != "" ||
		in.RuntimeAgentID != "" ||
		in.AgentToken != "" ||
		in.HeartbeatURL != "" ||
		in.InventoryURL != "" ||
		len(in.ChannelBootstrap) > 0
}

func normalizePreviewPolicy(in *PreviewPolicy) *PreviewPolicy {
	if in == nil {
		return nil
	}
	out := &PreviewPolicy{
		Required:          in.Required,
		Runtime:           strings.TrimSpace(in.Runtime),
		Audience:          strings.TrimSpace(in.Audience),
		Visibility:        strings.TrimSpace(in.Visibility),
		TTLSeconds:        in.TTLSeconds,
		StaffOnly:         in.StaffOnly,
		PublicBetaPreview: in.PublicBetaPreview,
	}
	if !out.Required && out.Runtime == "" && out.Audience == "" && out.Visibility == "" && out.TTLSeconds == 0 && !out.StaffOnly && !out.PublicBetaPreview {
		return nil
	}
	return out
}

func cloneAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		if strings.TrimSpace(key) != "" {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeOwnerSpecBootstrap(in *OwnerSpecBootstrap) *OwnerSpecBootstrap {
	if in == nil {
		return nil
	}
	out := &OwnerSpecBootstrap{
		Endpoint:  strings.TrimSpace(in.Endpoint),
		Token:     strings.TrimSpace(in.Token),
		ExpiresAt: strings.TrimSpace(in.ExpiresAt),
		Scopes:    compactStrings(in.Scopes),
	}
	if out.Endpoint == "" || out.Token == "" || out.ExpiresAt == "" {
		return nil
	}
	return out
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func RuntimeActionsFromEnv(base RuntimeActions) (RuntimeActions, RuntimeActionsEnvDiagnostics) {
	actions := base
	diagnostics := RuntimeActionsEnvDiagnostics{}
	hostKeys := NewRuntimeHostKeyStoreWithBackend(runtimeHostKeyStorePath(), base.HostKeyPins)
	if loadErr := hostKeys.Load(); loadErr != nil {
		// The store keeps the error and refuses every verification: a
		// corrupt pin file must never degrade to trust-on-first-use.
		diagnostics.Warnings = append(diagnostics.Warnings, loadErr.Error())
	}
	if actions.StackKitGenerator == nil {
		stackKitsDir := strings.TrimSpace(os.Getenv("TECHSTACK_STACKKITS_DIR"))
		stackKitCLI := strings.TrimSpace(os.Getenv(stackKitCLIEnv))
		if stackKitsDir != "" && stackKitCLI != "" {
			actions.StackKitGenerator = NewBundledStackKitCLIGenerator(stackKitsDir)
			diagnostics.Configured = append(diagnostics.Configured, "Bundled StackKits artifact generator")
		}
	}
	secret := strings.TrimSpace(os.Getenv("SERVICE_AUTH_SECRET"))
	secretNext := strings.TrimSpace(os.Getenv("SERVICE_AUTH_SECRET_NEXT"))
	// StackKits has no HTTP action service: every StackKits operation is a
	// typed command the enrolled node runs with the pinned CLI.
	// Simulate fallback chain (Simulate runs on IONOS Coolify, NOT Render —
	// see PLATFORM-ARCHITECTURE.md §1 and API-FIRST-STANDARD.md §15.5). The
	// canonical key is KOMBIFY_URL_VPS_SIMULATE; KOMBIFY_URL_INTERNAL_SIMULATE
	// is deliberately NOT in this chain because that key is forbidden for
	// IONOS-hosted services and resolves to a stale Render-pattern hostname
	// when present.
	simulateURL := firstRuntimeActionEnv(
		"TECHSTACK_SIMULATE_ACTIONS_URL",
		"TECHSTACK_KOMBISIM_URL",
		"KOMBIFY_URL_VPS_SIMULATE",
		"KOMBIFY_URL_PUBLIC_SIMULATE",
		"KOMBISIM_URL",
	)

	if actions.SimulationGate == nil {
		if simulateURL == "" {
			if truthyRuntimeActionEnv("TECHSTACK_ALLOW_LOCAL_SIMULATION_GATE") {
				actions.SimulationGate = localSimulationGateRunner{}
				diagnostics.Configured = append(diagnostics.Configured, "Local simulation gate")
			} else {
				diagnostics.Warnings = append(diagnostics.Warnings, "Simulate runtime action disabled: set TECHSTACK_SIMULATE_ACTIONS_URL or TECHSTACK_KOMBISIM_URL and SERVICE_AUTH_SECRET to enable the simulation gate")
			}
		} else if !serviceauth.CanSign(secret) {
			diagnostics.Warnings = append(diagnostics.Warnings, "Simulate runtime action disabled: a service auth secret or Ed25519 key is required for servicecall authentication")
		} else {
			runner, err := NewHTTPRuntimeActionRunner(HTTPRuntimeActionRunnerConfig{
				BaseURL:           simulateURL,
				Target:            runtimeActionTargetSimulate,
				Action:            runtimeActionSimulateUpdate,
				Path:              firstNonEmpty(firstRuntimeActionEnv("TECHSTACK_SIMULATION_GATE_PATH"), defaultSimulationGatePath),
				ServiceAuthSecret: secret,
				ServiceAuthNext:   secretNext,
			})
			if err != nil {
				diagnostics.Warnings = append(diagnostics.Warnings, fmt.Sprintf("Simulate runtime action disabled: %v", err))
			} else {
				actions.SimulationGate = runner
				diagnostics.Configured = append(diagnostics.Configured, "Simulate simulation gate")
			}
		}
	}

	if actions.DiagnosticsCollector == nil && !truthyRuntimeActionEnv("TECHSTACK_RUNTIME_DIAGNOSTICS_DISABLED") {
		actions.DiagnosticsCollector = NewSSHRuntimeDiagnosticsCollector(SSHRuntimeDiagnosticsCollectorConfig{HostKeys: hostKeys})
		diagnostics.Configured = append(diagnostics.Configured, "SSH runtime diagnostics collector")
	}
	if actions.TargetBootstrapper == nil && !truthyRuntimeActionEnv("TECHSTACK_RUNTIME_TARGET_BOOTSTRAP_DISABLED") {
		actions.TargetBootstrapper = NewSSHRuntimeTargetBootstrapper(SSHRuntimeTargetBootstrapperConfig{HostKeys: hostKeys})
		diagnostics.Configured = append(diagnostics.Configured, "SSH runtime target bootstrapper")
	}

	return actions, diagnostics
}

// runtimeHostKeyStorePath keeps the managed-target pins in the control-plane
// data directory so they survive restarts and deploys.
func runtimeHostKeyStorePath() string {
	dataDir := strings.TrimSpace(os.Getenv("TECHSTACK_DATA_DIR"))
	if dataDir == "" {
		dataDir = "data"
	}
	return filepath.Join(dataDir, "runtime-host-keys.json")
}

func truthyRuntimeActionEnv(name string) bool {
	const truthyStringTrue = "true"
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", truthyStringTrue, "yes", "on":
		return true
	default:
		return false
	}
}

func normalizeRuntimeActionBaseURL(raw string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	if trimmed == "" {
		return "", fmt.Errorf("runtime action base url required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("runtime action base url must include scheme and host")
	}
	return trimmed, nil
}

func normalizeRuntimeActionPath(raw string) string {
	trimmed := strings.Trim(strings.TrimSpace(raw), "/")
	if trimmed == "" {
		return ""
	}
	return "/" + trimmed
}

func firstRuntimeActionEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func runtimeActionName(r *HTTPRuntimeActionRunner) string {
	if r == nil {
		return ""
	}
	return r.action
}
