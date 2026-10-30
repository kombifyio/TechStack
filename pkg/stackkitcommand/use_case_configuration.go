package stackkitcommand

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

const MaxUseCaseConfigurationBytes = 64 << 10

var configurationKeyPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)

// UseCaseConfiguration is public catalog intent only. The receiving Guard
// selects every file and argument; callers cannot replace an active StackSpec.
type UseCaseConfiguration struct {
	WorkloadSlug       string                     `json:"workload_slug"`
	ModuleRef          string                     `json:"module_ref,omitempty"`
	ExpectedSpecHash   string                     `json:"expected_spec_hash,omitempty"`
	ComputeProfile     string                     `json:"compute_profile,omitempty"`
	StorageProfile     string                     `json:"storage_profile,omitempty"`
	AcceleratorProfile string                     `json:"accelerator_profile,omitempty"`
	Settings           map[string]json.RawMessage `json:"settings,omitempty"`
	SecretRefs         map[string]string          `json:"secret_refs,omitempty"`
}

func (c UseCaseConfiguration) ReadOnly() bool {
	return c.ComputeProfile == "" && c.StorageProfile == "" && c.AcceleratorProfile == "" && len(c.Settings) == 0 && len(c.SecretRefs) == 0
}

func DecodeUseCaseConfiguration(raw []byte) (UseCaseConfiguration, error) {
	var c UseCaseConfiguration
	if len(raw) == 0 || len(raw) > MaxUseCaseConfigurationBytes {
		return c, fmt.Errorf("use-case configuration is not bounded")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return c, fmt.Errorf("invalid typed use-case configuration: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return c, fmt.Errorf("use-case configuration requires one object")
	}
	if !serviceKeyPattern.MatchString(c.WorkloadSlug) || strings.TrimSpace(c.WorkloadSlug) != c.WorkloadSlug {
		return c, fmt.Errorf("catalog workload slug is required")
	}
	if c.ModuleRef != "" && !serviceKeyPattern.MatchString(c.ModuleRef) {
		return c, fmt.Errorf("catalog module reference is invalid")
	}
	if c.ExpectedSpecHash == "" && !c.ReadOnly() || c.ExpectedSpecHash != "" && !specHashPattern.MatchString(c.ExpectedSpecHash) {
		return c, fmt.Errorf("configuration requires the native expected spec hash")
	}
	for _, profile := range []string{c.ComputeProfile, c.StorageProfile, c.AcceleratorProfile} {
		if profile != "" && !serviceKeyPattern.MatchString(profile) {
			return c, fmt.Errorf("module profile is invalid")
		}
	}
	if len(c.Settings) > 64 || len(c.SecretRefs) > 64 {
		return c, fmt.Errorf("too many configuration fields")
	}
	for key, raw := range c.Settings {
		var value any
		if !configurationKeyPattern.MatchString(key) || json.Unmarshal(raw, &value) != nil {
			return c, fmt.Errorf("public setting is invalid")
		}
		switch value.(type) {
		case string, bool, float64:
		default:
			return c, fmt.Errorf("public settings require catalog scalar values")
		}
	}
	for key, ref := range c.SecretRefs {
		if !configurationKeyPattern.MatchString(key) || !strings.HasPrefix(ref, "secret://") || len(ref) > 512 || ref != strings.TrimSpace(ref) {
			return c, fmt.Errorf("secret input requires an existing reference")
		}
	}
	return c, nil
}

func ValidateUseCaseConfigurationFields(c *agentpb.StackKitCommand) error {
	if c == nil {
		return fmt.Errorf("configuration command is required")
	}
	if c.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_USE_CASE_CONFIGURE {
		if len(c.UseCaseConfigurationJson) != 0 {
			return fmt.Errorf("use-case configuration belongs only to its typed operation")
		}
		return nil
	}
	if c.StackkitInstanceId == "" || c.SpecPath == "" || len(c.CandidateSpecJson) != 0 || len(c.InventoryJson) != 0 || c.DryRun || c.Offline || c.TargetRelease != "" || c.OwnerApproved {
		return fmt.Errorf("configuration authors a candidate on its exact instance without execution approval")
	}
	configuration, err := DecodeUseCaseConfiguration(c.UseCaseConfigurationJson)
	if err == nil && configuration.ModuleRef == "" {
		return fmt.Errorf("configuration requires the selected native catalog module")
	}
	return err
}

type UseCaseConfigurationEvidence = NativeIntentCandidateEvidence

// ParseUseCaseConfigurationEvidence consumes only a validated Guard result of
// the pinned compiler. The hash remains the native compiler's hash; this does
// not introduce a Techstack canonicalization or hashing algorithm.
func ParseUseCaseConfigurationEvidence(c *agentpb.StackKitCommand, result *agentpb.StackKitResult) (UseCaseConfigurationEvidence, error) {
	var evidence UseCaseConfigurationEvidence
	config, err := DecodeUseCaseConfiguration(c.UseCaseConfigurationJson)
	if err != nil {
		return evidence, err
	}
	if config.ModuleRef == "" {
		return evidence, fmt.Errorf("configuration evidence requires the selected native catalog module")
	}
	evidence, err = ParseNativeIntentCandidateEvidence(c, result, config.ExpectedSpecHash)
	if err != nil {
		return evidence, err
	}
	if err := validateConfigurationDelta(evidence.PreviousSpec, evidence.CandidateSpec, config); err != nil {
		return evidence, err
	}
	if config.ReadOnly() && (evidence.SpecHash != evidence.PreviousSpecHash || !reflect.DeepEqual(evidence.PreviousSpec, evidence.CandidateSpec)) {
		return evidence, fmt.Errorf("configuration read changed desired intent")
	}
	return evidence, nil
}

// Preserve the full canonical before/after and allow changes only within the
// selected workload's named inputs and its already-selected module profiles.
func validateConfigurationDelta(before, after map[string]any, c UseCaseConfiguration) error {
	oldWorkloads, _ := before["workloads"].(map[string]any)
	newWorkloads, _ := after["workloads"].(map[string]any)
	oldEntry, ok := oldWorkloads[c.WorkloadSlug].(map[string]any)
	if !ok {
		return fmt.Errorf("selected workload is not installed")
	}
	newEntry, ok := newWorkloads[c.WorkloadSlug].(map[string]any)
	if !ok {
		return fmt.Errorf("configuration removed the selected workload")
	}
	expected := cloneConfigurationObject(before)
	expectedWorkloads := expected["workloads"].(map[string]any)
	expectedEntry := cloneConfigurationObject(oldEntry)
	for field, changes := range map[string]map[string]json.RawMessage{"settings": c.Settings} {
		if len(changes) == 0 {
			continue
		}
		values, _ := expectedEntry[field].(map[string]any)
		if values == nil {
			values = map[string]any{}
		}
		for key, raw := range changes {
			var value any
			_ = json.Unmarshal(raw, &value)
			values[key] = value
		}
		expectedEntry[field] = values
	}
	if len(c.SecretRefs) > 0 {
		values, _ := expectedEntry["secretRefs"].(map[string]any)
		if values == nil {
			values = map[string]any{}
		}
		for key, ref := range c.SecretRefs {
			values[key] = ref
		}
		expectedEntry["secretRefs"] = values
	}
	expectedWorkloads[c.WorkloadSlug] = expectedEntry
	if !reflect.DeepEqual(expectedEntry, newEntry) {
		return fmt.Errorf("configuration changed unrelated workload identity, placement, routes, data or inputs")
	}
	// The selected catalog module is checked by the caller before this result is
	// dispatched. Here every changed module may change only requested profiles.
	oldModules, _ := before["modules"].(map[string]any)
	newModules, _ := after["modules"].(map[string]any)
	expectedModules, _ := expected["modules"].(map[string]any)
	for module, value := range newModules {
		if reflect.DeepEqual(oldModules[module], value) {
			continue
		}
		if module != c.ModuleRef || c.ComputeProfile == "" && c.StorageProfile == "" && c.AcceleratorProfile == "" {
			return fmt.Errorf("configuration changed a module outside the requested catalog profiles")
		}
		original, exists := oldModules[module]
		originalModule, ok := original.(map[string]any)
		if exists && !ok {
			return fmt.Errorf("configuration baseline module is invalid")
		}
		if !exists {
			// The native canonical schema materializes enabled:true when an
			// explicit profile first creates the already-selected module entry.
			originalModule = map[string]any{"enabled": true}
		}
		allowed := cloneConfigurationObject(originalModule)
		for key, value := range map[string]string{"computeProfile": c.ComputeProfile, "storageProfile": c.StorageProfile, "acceleratorProfile": c.AcceleratorProfile} {
			if value != "" {
				allowed[key] = value
			}
		}
		if !reflect.DeepEqual(allowed, value) {
			return fmt.Errorf("configuration changed unrelated module intent")
		}
		if expectedModules == nil {
			expectedModules = map[string]any{}
			expected["modules"] = expectedModules
		}
		expectedModules[module] = allowed
	}
	if !reflect.DeepEqual(expected, after) {
		return fmt.Errorf("configuration changed unrelated canonical intent")
	}
	return nil
}

func cloneConfigurationObject(source map[string]any) map[string]any {
	raw, _ := json.Marshal(source)
	var result map[string]any
	_ = json.Unmarshal(raw, &result)
	return result
}

// ValidateUseCaseConfigurationModule binds profile changes to the one module
// named by the selected workload alternative in the exact release catalog.
func ValidateUseCaseConfigurationModule(e UseCaseConfigurationEvidence, moduleRef string) error {
	before, _ := e.PreviousSpec["modules"].(map[string]any)
	after, _ := e.CandidateSpec["modules"].(map[string]any)
	for id, value := range after {
		if id != moduleRef && !reflect.DeepEqual(before[id], value) {
			return fmt.Errorf("configuration changed an unrelated module")
		}
	}
	for id, value := range before {
		if id != moduleRef && !reflect.DeepEqual(after[id], value) {
			return fmt.Errorf("configuration removed an unrelated module")
		}
	}
	return nil
}

func NativeSpecHashValid(value string) bool { return specHashPattern.MatchString(value) }
