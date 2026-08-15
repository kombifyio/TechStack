package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/runtimeaction"
	"github.com/kombifyio/go-common/identity"
	"github.com/kombifyio/go-common/servicecall"
)

const (
	runtimeActionTargetStackKits = runtimeaction.TargetStackKits
	runtimeActionTargetSimulate  = runtimeaction.TargetSimulate

	runtimeActionSimulateUpdate = string(runtimeaction.ActionSimulateUpdate)

	defaultRuntimeActionPathPrefix = runtimeaction.PathPrefix
	defaultSimulationGatePath      = runtimeaction.PathSimulateUpdate
	// Rollout and verify default to the governed Architecture v2 execution
	// surface: the v1 deployment actions are retired on the native v2 line
	// (410 legacy_runtime_action_retired). TECHSTACK_STACKKITS_ROLLOUT_PATH /
	// _VERIFY_PATH still override for older StackKits lines.
	defaultStackKitsRolloutPath = runtimeaction.ArchitectureV2PathStackKitRollout
	defaultStackKitsVerifyPath  = runtimeaction.ArchitectureV2PathStackKitVerify
	defaultRestoreDrillPath     = runtimeaction.PathRestoreDrill
	runtimeActionServiceName    = "techstack"
	runtimeActionHTTPTimeout    = 14*time.Minute + 30*time.Second
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
	client, err := servicecall.NewClient(servicecall.Config{
		ServiceName: runtimeActionServiceName,
		Target:      target,
		Secret:      cfg.ServiceAuthSecret,
		SecretNext:  cfg.ServiceAuthNext,
	})
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
	var payload any
	if strings.HasPrefix(r.path, runtimeaction.ArchitectureV2PathPrefix) {
		v2Payload, v2Err := architectureV2ExecutionPayload(r.action, req)
		if v2Err != nil {
			return nil, fmt.Errorf("runtime action %s v2 envelope: %w", r.action, v2Err)
		}
		payload = v2Payload
	} else {
		payload = RuntimeActionRequest{
			Action:              runtimeaction.NormalizeAction(r.action),
			StackID:             strings.TrimSpace(req.StackID),
			StackName:           strings.TrimSpace(req.StackName),
			StackKit:            strings.TrimSpace(req.StackKit),
			Mode:                strings.TrimSpace(req.Mode),
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
	}
	resp, err := r.client.PostJSON(ctx, r.baseURL+r.path, payload, servicecall.OnBehalfOfIdentity(identity.FromContext(ctx)))
	if err != nil {
		return nil, fmt.Errorf("runtime action %s request failed: %w", r.action, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("runtime action %s returned %d: %s", r.action, resp.StatusCode, strings.TrimSpace(string(body)))
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
		ControlURLs:      compactStrings(in.ControlURLs),
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
		len(in.ControlURLs) > 0 ||
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
	// StackKits fallback chain:
	// dedicated action URL → Administration-managed internal URL → explicit API URL.
	// STACKKITS_PUBLIC_URL is intentionally excluded because it is the public
	// docs/static-site origin in production, not a runtime-action API.
	stackKitsURL := firstRuntimeActionEnv(
		"TECHSTACK_STACKKITS_ACTIONS_URL",
		"TECHSTACK_STACKKITS_INTERNAL_URL",
		"KOMBIFY_URL_INTERNAL_STACKKITS",
		"STACKKITS_INTERNAL_URL",
		"STACKKITS_API_URL",
	)
	// Simulate fallback chain (Simulate runs on IONOS Coolify, NOT Render —
	// see PLATFORM-ARCHITECTURE.md §1 and SERVICE-URL-REGISTRY.md §5). The
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

	if needsStackKitsRuntimeActions(actions) {
		if stackKitsURL == "" {
			diagnostics.Warnings = append(diagnostics.Warnings, "StackKits runtime actions disabled: set TECHSTACK_STACKKITS_ACTIONS_URL and SERVICE_AUTH_SECRET to enable rollout, verification, and restore drill")
		} else if secret == "" {
			diagnostics.Warnings = append(diagnostics.Warnings, "StackKits runtime actions disabled: SERVICE_AUTH_SECRET is required for servicecall authentication")
		} else {
			actions = configureStackKitsRuntimeActions(actions, stackKitsURL, secret, secretNext, &diagnostics)
		}
	}

	if actions.SimulationGate == nil {
		if simulateURL == "" {
			if truthyRuntimeActionEnv("TECHSTACK_ALLOW_LOCAL_SIMULATION_GATE") {
				actions.SimulationGate = localSimulationGateRunner{}
				diagnostics.Configured = append(diagnostics.Configured, "Local simulation gate")
			} else {
				diagnostics.Warnings = append(diagnostics.Warnings, "Simulate runtime action disabled: set TECHSTACK_SIMULATE_ACTIONS_URL or TECHSTACK_KOMBISIM_URL and SERVICE_AUTH_SECRET to enable the simulation gate")
			}
		} else if secret == "" {
			diagnostics.Warnings = append(diagnostics.Warnings, "Simulate runtime action disabled: SERVICE_AUTH_SECRET is required for servicecall authentication")
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
		actions.DiagnosticsCollector = NewSSHRuntimeDiagnosticsCollector(SSHRuntimeDiagnosticsCollectorConfig{})
		diagnostics.Configured = append(diagnostics.Configured, "SSH runtime diagnostics collector")
	}
	if actions.TargetBootstrapper == nil && !truthyRuntimeActionEnv("TECHSTACK_RUNTIME_TARGET_BOOTSTRAP_DISABLED") {
		actions.TargetBootstrapper = NewSSHRuntimeTargetBootstrapper(SSHRuntimeTargetBootstrapperConfig{})
		diagnostics.Configured = append(diagnostics.Configured, "SSH runtime target bootstrapper")
	}

	return actions, diagnostics
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

func configureStackKitsRuntimeActions(actions RuntimeActions, baseURL, secret, secretNext string, diagnostics *RuntimeActionsEnvDiagnostics) RuntimeActions {
	if actions.RolloutRunner == nil {
		if runner, err := newStackKitsRuntimeAction(baseURL, string(runtimeaction.ActionStackKitRollout), firstNonEmpty(firstRuntimeActionEnv("TECHSTACK_STACKKITS_ROLLOUT_PATH"), defaultStackKitsRolloutPath), secret, secretNext); err != nil {
			diagnostics.Warnings = append(diagnostics.Warnings, fmt.Sprintf("StackKits rollout runner disabled: %v", err))
		} else {
			actions.RolloutRunner = runner
			diagnostics.Configured = append(diagnostics.Configured, "StackKits rollout runner")
		}
	}
	if actions.RolloutVerifier == nil {
		if runner, err := newStackKitsRuntimeAction(baseURL, string(runtimeaction.ActionVerifyRollout), firstNonEmpty(firstRuntimeActionEnv("TECHSTACK_STACKKITS_VERIFY_PATH"), defaultStackKitsVerifyPath), secret, secretNext); err != nil {
			diagnostics.Warnings = append(diagnostics.Warnings, fmt.Sprintf("StackKits rollout verifier disabled: %v", err))
		} else {
			actions.RolloutVerifier = runner
			diagnostics.Configured = append(diagnostics.Configured, "StackKits rollout verifier")
		}
	}
	if actions.RestoreDrill == nil {
		if runner, err := newStackKitsRuntimeAction(baseURL, string(runtimeaction.ActionRestoreDrill), firstNonEmpty(firstRuntimeActionEnv("TECHSTACK_STACKKITS_RESTORE_DRILL_PATH"), defaultRestoreDrillPath), secret, secretNext); err != nil {
			diagnostics.Warnings = append(diagnostics.Warnings, fmt.Sprintf("StackKits restore drill disabled: %v", err))
		} else {
			actions.RestoreDrill = runner
			diagnostics.Configured = append(diagnostics.Configured, "StackKits restore drill")
		}
	}
	return actions
}

func newStackKitsRuntimeAction(baseURL, action, path, secret, secretNext string) (*HTTPRuntimeActionRunner, error) {
	return NewHTTPRuntimeActionRunner(HTTPRuntimeActionRunnerConfig{
		BaseURL:           baseURL,
		Target:            runtimeActionTargetStackKits,
		Action:            action,
		Path:              path,
		ServiceAuthSecret: secret,
		ServiceAuthNext:   secretNext,
	})
}

func needsStackKitsRuntimeActions(actions RuntimeActions) bool {
	return actions.RolloutRunner == nil || actions.RolloutVerifier == nil || actions.RestoreDrill == nil
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
