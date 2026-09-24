package specv2

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"gopkg.in/yaml.v3"
)

const (
	// StackKitCLIEnv names the development-only CLI override; production
	// resolves the binary through the pinned-release admission instead.
	StackKitCLIEnv = "TECHSTACK_STACKKIT_CLI"
	// CompatibilityManifestEnv points at the SHA-verified compatibility
	// manifest published beside the exact StackKits release artifact.
	CompatibilityManifestEnv = "TECHSTACK_STACKKIT_COMPATIBILITY_MANIFEST"

	releasePinEnv   = "TECHSTACK_STACKKIT_RELEASE_PIN"
	releaseCacheEnv = "TECHSTACK_STACKKIT_RELEASE_CACHE"

	defaultValidateTimeout = 2 * time.Minute
	validateOutputTail     = 4000
	wizardRuntimeAdapter   = "standalone-compose"
	maxCompatibilityBytes  = 1 << 20
	// Existing legacy seeds retain their compute-tier authoring contract.
	authoringComputeTier = "standard"
)

// SpecValidator is the acceptance authority for projected specs: the pinned
// StackKits CLI's own `validate`, never a Techstack reimplementation.
type SpecValidator interface {
	ValidateSpec(ctx context.Context, spec map[string]any) error
}

// CLIValidator runs `stackkit --no-log --chdir <dir> --spec stack-spec.yaml
// validate` against a scratch workspace holding the projected document.
type CLIValidator struct {
	Binary  string
	Timeout time.Duration
	// ReleaseVersion is informational provenance when the binary came from
	// the pinned-release admission.
	ReleaseVersion string
	goalWorkloads  map[string]string
	goalBindings   map[string]workloadModuleBinding
	commandRunner  func(context.Context, string, []string, string) ([]byte, error)
}

// NewCLIValidatorFromEnv resolves the validator binary: the fail-closed
// pinned-release admission when the pin is configured, else the explicit
// development override. It returns (nil, nil) when neither is configured so
// callers can fail closed with a precise reason instead of guessing at PATH.
func NewCLIValidatorFromEnv() (*CLIValidator, error) {
	pinPath := strings.TrimSpace(os.Getenv(releasePinEnv))
	cacheRoot := strings.TrimSpace(os.Getenv(releaseCacheEnv))
	switch {
	case pinPath != "" && cacheRoot != "":
		release, err := (stackkitrelease.Cache{Root: cacheRoot}).ResolvePin(pinPath)
		if err != nil {
			return nil, fmt.Errorf("specv2: admit pinned published StackKits release: %w", err)
		}
		goalWorkloads, goalBindings, err := loadGoalMetadata(strings.TrimSpace(os.Getenv(CompatibilityManifestEnv)), release.Receipt().Version)
		if err != nil {
			return nil, err
		}
		return &CLIValidator{
			Binary:         release.BinaryPath(),
			Timeout:        defaultValidateTimeout,
			ReleaseVersion: release.Receipt().Version,
			goalWorkloads:  goalWorkloads,
			goalBindings:   goalBindings,
		}, nil
	case pinPath != "" || cacheRoot != "":
		return nil, fmt.Errorf("specv2: %s and %s must be configured together", releasePinEnv, releaseCacheEnv)
	}
	if binary := strings.TrimSpace(os.Getenv(StackKitCLIEnv)); binary != "" {
		goalWorkloads, goalBindings, err := loadGoalMetadata(strings.TrimSpace(os.Getenv(CompatibilityManifestEnv)), "")
		if err != nil {
			return nil, err
		}
		return &CLIValidator{Binary: binary, Timeout: defaultValidateTimeout, goalWorkloads: goalWorkloads, goalBindings: goalBindings}, nil
	}
	return nil, nil
}

type compatibilityManifest struct {
	SchemaVersion string `json:"schemaVersion"`
	Release       struct {
		Tag     string `json:"tag"`
		Version string `json:"version"`
	} `json:"release"`
	Compatibility struct {
		ApplicationDelivery []struct {
			UseCaseRef            string `json:"useCaseRef"`
			WorkloadRef           string `json:"workloadRef"`
			AdapterRef            string `json:"adapterRef"`
			Status                string `json:"status"`
			DefaultAlternativeRef string `json:"defaultAlternativeRef"`
			DefaultModuleRef      string `json:"defaultModuleRef"`
		} `json:"applicationDelivery"`
	} `json:"compatibility"`
}

// DeliverableGoals reports which use cases the exact pinned release can
// actually deliver through the wizard's runtime adapter, keyed by use-case
// slug. It reads the same compatibility manifest that AuthorGoals gates on, so
// a surface asking "can I have this?" and the authoring path answering it can
// never disagree.
//
// Returns (nil, false, nil) when no manifest is configured: a caller that
// cannot establish deliverability must say nothing rather than guess, and an
// empty map would read as "nothing is deliverable".
func DeliverableGoals() (map[string]string, bool, error) {
	path := strings.TrimSpace(os.Getenv(CompatibilityManifestEnv))
	if path == "" {
		return nil, false, nil
	}
	goals, err := loadGoalWorkloads(path, "")
	if err != nil {
		return nil, true, err
	}
	return goals, true, nil
}

func loadGoalWorkloads(path, releaseVersion string) (map[string]string, error) {
	workloads, _, err := loadGoalMetadata(path, releaseVersion)
	return workloads, err
}

type workloadModuleBinding struct {
	AlternativeRef string
	ModuleRef      string
}

func loadGoalMetadata(path, releaseVersion string) (map[string]string, map[string]workloadModuleBinding, error) {
	manifest, err := readCompatibilityManifest(path)
	if err != nil {
		return nil, nil, err
	}
	manifestVersion := strings.TrimPrefix(strings.TrimSpace(manifest.Release.Version), "v")
	if manifestVersion == "" || strings.TrimSpace(manifest.Release.Tag) != "v"+manifestVersion {
		return nil, nil, fmt.Errorf("specv2: StackKits compatibility manifest has inconsistent release identity")
	}
	if expected := strings.TrimPrefix(strings.TrimSpace(releaseVersion), "v"); expected != "" && manifestVersion != expected {
		return nil, nil, fmt.Errorf("specv2: StackKits compatibility release v%s does not match admitted release v%s", manifestVersion, expected)
	}
	goals := map[string]string{}
	bindings := map[string]workloadModuleBinding{}
	for _, delivery := range manifest.Compatibility.ApplicationDelivery {
		goal := strings.TrimSpace(delivery.UseCaseRef)
		workload := strings.TrimSpace(delivery.WorkloadRef)
		if delivery.AdapterRef != wizardRuntimeAdapter || delivery.Status != "supported" {
			continue
		}
		if goal == "" || workload == "" || contractID(goal) != goal || contractID(workload) != workload {
			return nil, nil, fmt.Errorf("specv2: StackKits compatibility manifest contains an invalid use-case delivery identity")
		}
		if existing, duplicate := goals[goal]; duplicate && existing != workload {
			return nil, nil, fmt.Errorf("specv2: StackKits compatibility manifest maps use case %q to multiple workloads", goal)
		}
		goals[goal] = workload
		binding := workloadModuleBinding{AlternativeRef: delivery.DefaultAlternativeRef, ModuleRef: delivery.DefaultModuleRef}
		if binding.AlternativeRef != "" || binding.ModuleRef != "" {
			if contractID(binding.AlternativeRef) != binding.AlternativeRef || contractID(binding.ModuleRef) != binding.ModuleRef {
				return nil, nil, fmt.Errorf("specv2: invalid native module binding for workload %q", workload)
			}
			if existing, ok := bindings[workload]; ok && existing != binding {
				return nil, nil, fmt.Errorf("specv2: conflicting native module binding for workload %q", workload)
			}
			bindings[workload] = binding
		}
	}
	return goals, bindings, nil
}

func readCompatibilityManifest(path string) (compatibilityManifest, error) {
	if strings.TrimSpace(path) == "" {
		return compatibilityManifest{}, fmt.Errorf("specv2: %s is required with the StackKits CLI", CompatibilityManifestEnv)
	}
	manifestPath := filepath.Clean(path)
	info, err := os.Lstat(manifestPath) // #nosec G304 -- operator/image configuration selects the immutable manifest.
	if err != nil {
		return compatibilityManifest{}, fmt.Errorf("specv2: inspect pinned StackKits compatibility manifest: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maxCompatibilityBytes {
		return compatibilityManifest{}, fmt.Errorf("specv2: pinned StackKits compatibility manifest must be a bounded regular non-symlink file")
	}
	file, err := os.Open(manifestPath) // #nosec G304 -- path passed the bounded regular-file check above.
	if err != nil {
		return compatibilityManifest{}, fmt.Errorf("specv2: open pinned StackKits compatibility manifest: %w", err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, info.Size()+1))
	if err != nil {
		return compatibilityManifest{}, fmt.Errorf("specv2: read pinned StackKits compatibility manifest: %w", err)
	}
	if int64(len(data)) != info.Size() {
		return compatibilityManifest{}, fmt.Errorf("specv2: pinned StackKits compatibility manifest changed while it was read")
	}
	var manifest compatibilityManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return compatibilityManifest{}, fmt.Errorf("specv2: decode pinned StackKits compatibility manifest: %w", err)
	}
	if manifest.SchemaVersion != "stackkits-compatibility/v1" {
		return compatibilityManifest{}, fmt.Errorf("specv2: unsupported StackKits compatibility schema %q", manifest.SchemaVersion)
	}
	return manifest, nil
}

// AuthorGoals lets the published manifest decide which selected use cases are
// shipped, then invokes that same release's CLI to author defaults, placement,
// adapter selection and required secret references.
func (v *CLIValidator) AuthorGoals(ctx context.Context, kitSlug, name, domainBase, apiVersion string, goals []string) (GoalAuthoring, error) {
	apiVersion = strings.TrimSpace(apiVersion)
	if v == nil || strings.TrimSpace(v.Binary) == "" || v.goalWorkloads == nil {
		return GoalAuthoring{}, fmt.Errorf("StackKits release goal author is not configured")
	}
	if !IsKnownKitSlug(kitSlug) {
		return GoalAuthoring{}, fmt.Errorf("StackKits release goal author rejected kit %q", kitSlug)
	}
	if !isCanonicalAPIVersion(apiVersion) {
		return GoalAuthoring{}, fmt.Errorf("unsupported StackKits authoring API version %q", apiVersion)
	}
	mappedGoals, unmapped, workloadIDs := selectGoalWorkloads(goals, v.goalWorkloads)
	if len(mappedGoals) == 0 {
		return GoalAuthoring{UnmappedGoals: unmapped}, nil
	}

	workDir, err := os.MkdirTemp("", "specv2-author-")
	if err != nil {
		return GoalAuthoring{}, fmt.Errorf("create StackKits authoring workspace: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()
	timeout := v.Timeout
	if timeout <= 0 {
		timeout = defaultValidateTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Init itself may select use cases; the kit-authoring overlay Techstack
	// applies later is still only metadata.name and network.domain.base.
	// --owner-source=local matches the image-authored seeds.
	//
	// Native seeds accept CUE-owned catalog defaults as explicit initial
	// authoring intent. Existing legacy seeds retain their API and semantics;
	// adding a goal never silently migrates the installed stack.
	args := []string{
		"--no-log", "--chdir", workDir, "init", kitSlug,
		"--non-interactive", "--name", contractID(name),
		"--owner-source", "local",
		"--use-case", strings.Join(mappedGoals, ","),
		"--platform", wizardRuntimeAdapter,
		"--api-version", apiVersion,
	}
	if apiVersion == NativeSpecAPIVersion {
		args = append(args, "--catalog-defaults")
	} else {
		args = append(args, "--compute-tier", authoringComputeTier)
	}
	if strings.TrimSpace(domainBase) != "" {
		args = append(args, "--domain", strings.TrimSpace(domainBase))
	}
	cmd := exec.CommandContext(runCtx, v.Binary, args...) // #nosec G204 -- binary is release-admitted and arguments are closed validated IDs.
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		if runCtx.Err() != nil {
			return GoalAuthoring{}, fmt.Errorf("StackKits CLI goal authoring timed out after %s: %w", timeout, runCtx.Err())
		}
		return GoalAuthoring{}, fmt.Errorf("StackKits CLI goal authoring failed: %w: %s", err, tailText(output, validateOutputTail))
	}
	document, err := os.ReadFile(filepath.Join(workDir, "stack-spec.yaml"))
	if err != nil {
		return GoalAuthoring{}, fmt.Errorf("read StackKits-authored spec: %w", err)
	}
	return goalAuthoringFromDocument(document, kitSlug, mappedGoals, unmapped, workloadIDs, v.goalBindings)
}

func selectGoalWorkloads(goals []string, goalWorkloads map[string]string) ([]string, []string, map[string]bool) {
	seen := map[string]bool{}
	mappedGoals := make([]string, 0, len(goals))
	unmapped := make([]string, 0, len(goals))
	workloadIDs := map[string]bool{}
	for _, raw := range goals {
		goal := strings.ToLower(strings.TrimSpace(raw))
		if goal == "" || seen[goal] {
			continue
		}
		seen[goal] = true
		workload, supported := goalWorkloads[goal]
		if !supported {
			unmapped = append(unmapped, goal)
			continue
		}
		mappedGoals = append(mappedGoals, goal)
		workloadIDs[workload] = true
	}
	sort.Strings(unmapped)
	return mappedGoals, unmapped, workloadIDs
}

func goalAuthoringFromDocument(document []byte, kitSlug string, mappedGoals, unmapped []string, workloadIDs map[string]bool, bindings map[string]workloadModuleBinding) (GoalAuthoring, error) {
	var authoredSpec map[string]any
	if err := yaml.Unmarshal(document, &authoredSpec); err != nil {
		return GoalAuthoring{}, fmt.Errorf("decode StackKits-authored spec: %w", err)
	}
	if err := RequireCanonicalV2(authoredSpec); err != nil {
		return GoalAuthoring{}, fmt.Errorf("StackKits CLI authored a non-v2 StackSpec: %w", err)
	}
	kit, _ := authoredSpec["kit"].(map[string]any)
	if authoredKit, _ := kit["slug"].(string); authoredKit != kitSlug {
		return GoalAuthoring{}, fmt.Errorf("StackKits CLI authored kit %q, want %q", authoredKit, kitSlug)
	}
	authoredWorkloads, _ := authoredSpec["workloads"].(map[string]any)
	selected := make(map[string]any, len(workloadIDs))
	modules, _ := authoredSpec["modules"].(map[string]any)
	profiles := map[string]map[string]any{}
	for workloadID := range workloadIDs {
		entry, exists := authoredWorkloads[workloadID]
		if !exists {
			return GoalAuthoring{}, fmt.Errorf("StackKits CLI omitted compatibility workload %q", workloadID)
		}
		selected[workloadID] = entry
		if authoredSpec["apiVersion"] == NativeSpecAPIVersion {
			binding, exists := bindings[workloadID]
			workload, _ := entry.(map[string]any)
			profile, profileExists := modules[binding.ModuleRef]
			if !exists || !profileExists || binding.AlternativeRef != workload["alternative"] {
				return GoalAuthoring{}, fmt.Errorf("StackKits native workload %q has no matching release-owned module profile binding", workloadID)
			}
			profiles[workloadID] = map[string]any{binding.ModuleRef: profile}
		}
	}
	return GoalAuthoring{
		UseCases:       authoredUseCases(authoredSpec, mappedGoals),
		Workloads:      selected,
		ModuleProfiles: profiles,
		UnmappedGoals:  unmapped,
	}, nil
}

func authoredUseCases(spec map[string]any, mappedGoals []string) []string {
	ids := make([]string, 0, len(mappedGoals))
	if raw, ok := spec["useCases"].([]any); ok {
		for _, value := range raw {
			if id, ok := value.(string); ok {
				id = strings.ToLower(strings.TrimSpace(id))
				if id != "" {
					ids = append(ids, id)
				}
			}
		}
	}
	if len(ids) == 0 {
		ids = append(ids, mappedGoals...)
	}
	sort.Strings(ids)
	return ids
}

func (v *CLIValidator) ValidateSpec(ctx context.Context, spec map[string]any) error {
	if err := RequireCanonicalV2(spec); err != nil {
		return err
	}
	if v == nil || strings.TrimSpace(v.Binary) == "" {
		return fmt.Errorf("specv2: validator not configured")
	}
	workDir, err := os.MkdirTemp("", "specv2-validate-")
	if err != nil {
		return fmt.Errorf("specv2: create validate workspace: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	handoff := deepCopyValue(spec).(map[string]any)
	DropBindingRejectedFields(handoff)
	// JSON is valid YAML; the seed templates are authored the same way.
	document, err := json.Marshal(handoff)
	if err != nil {
		return fmt.Errorf("specv2: encode projected spec: %w", err)
	}
	specPath := filepath.Join(workDir, "stack-spec.yaml")
	if writeErr := os.WriteFile(specPath, document, 0o600); writeErr != nil {
		return fmt.Errorf("specv2: write projected spec: %w", writeErr)
	}

	timeout := v.Timeout
	if timeout <= 0 {
		timeout = defaultValidateTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"--no-log", "--chdir", workDir, "--spec", filepath.Base(specPath), "validate"}
	output, err := v.runCLI(runCtx, args, workDir)
	if err != nil {
		if runCtx.Err() != nil {
			return fmt.Errorf("specv2: StackKits CLI validate timed out after %s: %w", timeout, runCtx.Err())
		}
		return fmt.Errorf("specv2: StackKits CLI validate failed: %w: %s", err, tailText(output, validateOutputTail))
	}
	return nil
}

func (v *CLIValidator) runCLI(ctx context.Context, args []string, workDir string) ([]byte, error) {
	if v.commandRunner != nil {
		return v.commandRunner(ctx, v.Binary, append([]string(nil), args...), workDir)
	}
	cmd := exec.CommandContext(ctx, v.Binary, args...) // #nosec G204 -- binary comes from pinned-release admission or the operator env; callers provide closed arguments.
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	return cmd.CombinedOutput()
}

func tailText(output []byte, limit int) string {
	text := strings.TrimSpace(string(output))
	if len(text) <= limit {
		return text
	}
	return text[len(text)-limit:]
}

var _ SpecValidator = (*CLIValidator)(nil)
var _ GoalAuthor = (*CLIValidator)(nil)
