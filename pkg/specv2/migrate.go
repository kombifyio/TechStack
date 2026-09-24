package specv2

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	migrationResultAPIVersion = "stackkit.migration-result/v1"
	migrationResultKind       = "StackSpecMigrationResult"
	migrationCompletionAPI    = "stackkit.migration-completion/v1"
	migrationCompletionKind   = "StackSpecV2Completion"
	migrationCompletedStatus  = "completed-v2"
	migrationBlockedStatus    = "blocked"
	migrationReadyStatus      = "ready-for-shadow-resolution"
	maxMigrationDocumentBytes = 1 << 20
)

// SpecMigrator is the governed v1-to-v2 completion boundary. Implementations
// must return a canonical spec only after the pinned StackKits authority has
// completed and validated it.
type SpecMigrator interface {
	CompleteMigration(ctx context.Context, legacy, candidate map[string]any, targetKit string) (*MigrationResult, error)
}

type MigrationBlocker struct {
	Code                 string   `json:"code"`
	Field                string   `json:"field"`
	Message              string   `json:"message"`
	RequiredInputs       []string `json:"requiredInputs"`
	SuggestedKitProfiles []string `json:"suggestedKitProfiles,omitempty"`
}

type MigrationManualAction struct {
	Code     string   `json:"code"`
	Fields   []string `json:"fields"`
	Message  string   `json:"message"`
	Required bool     `json:"required"`
}

// MigrationResult keeps non-success migration decisions typed while making a
// canonical StackSpec available only for a completed, CUE-valid result.
type MigrationResult struct {
	Status            string
	CanonicalSpec     map[string]any
	Blockers          []MigrationBlocker
	ManualActions     []MigrationManualAction
	GeneratorEligible bool
}

func (result *MigrationResult) Completed() bool {
	return result != nil && result.Status == migrationCompletedStatus && len(result.CanonicalSpec) > 0
}

type migrationCLIEnvelope struct {
	APIVersion             string                  `json:"apiVersion"`
	Kind                   string                  `json:"kind"`
	Status                 string                  `json:"status"`
	Source                 json.RawMessage         `json:"source"`
	RequestedTargetKit     string                  `json:"requestedTargetKit,omitempty"`
	Report                 json.RawMessage         `json:"report"`
	ArchitectureProjection json.RawMessage         `json:"architectureProjection,omitempty"`
	Completion             *migrationCLICompletion `json:"completion,omitempty"`
	Safety                 migrationCLISafety      `json:"safety"`
}

type migrationCLICompletion struct {
	APIVersion               string          `json:"apiVersion"`
	Kind                     string          `json:"kind"`
	Status                   string          `json:"status"`
	Source                   json.RawMessage `json:"source"`
	CanonicalStackSpec       map[string]any  `json:"canonicalStackSpec"`
	CanonicalStackSpecSHA256 string          `json:"canonicalStackSpecSHA256"`
	CandidateIntentSHA256    string          `json:"candidateIntentSHA256"`
	MigrationDecisionRecord  map[string]any  `json:"migrationDecisionRecord"`
	MigrationReportSHA256    string          `json:"migrationReportSHA256"`
	ResolvedPlanHash         string          `json:"resolvedPlanHash"`
}

type migrationCLISafety struct {
	CUEValidV2        bool   `json:"cueValidV2"`
	GeneratorEligible bool   `json:"generatorEligible"`
	Notice            string `json:"notice"`
}

type migrationCLIReport struct {
	Blockers      []MigrationBlocker      `json:"blockers"`
	ManualActions []MigrationManualAction `json:"manualActions"`
}

// CompleteMigration invokes the exact admitted StackKits binary. Techstack
// supplies explicit source and candidate documents but never translates v1
// fields itself. The CLI's completed canonical output is read back from both
// governed outputs, compared, and validated once more through that same CLI.
func (v *CLIValidator) CompleteMigration(ctx context.Context, legacy, candidate map[string]any, targetKit string) (*MigrationResult, error) {
	if v == nil || strings.TrimSpace(v.Binary) == "" {
		return nil, fmt.Errorf("specv2: StackKits migration authority is not configured")
	}
	targetKit = strings.TrimSpace(targetKit)
	if !IsKnownKitSlug(targetKit) {
		return nil, fmt.Errorf("specv2: migration target is not an installable kit")
	}
	workDir, workspaceErr := os.MkdirTemp("", "specv2-migrate-")
	if workspaceErr != nil {
		return nil, fmt.Errorf("specv2: create migration workspace: %w", workspaceErr)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	legacyName := "legacy-stack-spec.json"
	candidateName := "candidate-stack-spec-v2.json"
	resultName := "migration-result.json"
	specName := "migrated-stack-spec-v2.json"
	if err := writeMigrationDocument(filepath.Join(workDir, legacyName), legacy); err != nil {
		return nil, fmt.Errorf("specv2: write legacy migration source: %w", err)
	}
	if err := writeMigrationDocument(filepath.Join(workDir, candidateName), candidate); err != nil {
		return nil, fmt.Errorf("specv2: write explicit migration candidate: %w", err)
	}

	timeout := v.Timeout
	if timeout <= 0 {
		timeout = defaultValidateTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{
		"--no-log", "--chdir", workDir,
		"migrate", legacyName,
		"--target-kit", targetKit,
		"--complete-with", candidateName,
		"--format", "json",
		"--output", resultName,
		"--spec-output", specName,
	}
	output, commandErr := v.runCLI(runCtx, args, workDir)
	if runCtx.Err() != nil {
		return nil, fmt.Errorf("specv2: StackKits CLI migration timed out after %s: %w", timeout, runCtx.Err())
	}

	resultData, readErr := readBoundedMigrationFile(filepath.Join(workDir, resultName))
	if readErr != nil {
		if commandErr != nil {
			return nil, fmt.Errorf("specv2: StackKits CLI migration failed before emitting its decision: %w: %s", commandErr, tailText(output, validateOutputTail))
		}
		return nil, fmt.Errorf("specv2: read StackKits migration decision: %w", readErr)
	}
	envelope, report, decodeErr := decodeMigrationDecision(resultData, targetKit)
	if decodeErr != nil {
		return nil, decodeErr
	}
	result, completed, decisionErr := migrationResultFromDecision(envelope, report, commandErr, output)
	if decisionErr != nil || !completed {
		return result, decisionErr
	}
	canonical, completionErr := v.validateCompletedMigration(runCtx, filepath.Join(workDir, specName), targetKit, envelope.Completion)
	if completionErr != nil {
		return nil, completionErr
	}
	result.CanonicalSpec = canonical
	return result, nil
}

func decodeMigrationDecision(data []byte, targetKit string) (migrationCLIEnvelope, migrationCLIReport, error) {
	var envelope migrationCLIEnvelope
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&envelope); decodeErr != nil {
		return migrationCLIEnvelope{}, migrationCLIReport{}, fmt.Errorf("specv2: decode StackKits migration decision: %w", decodeErr)
	}
	if eofErr := requireJSONEOF(decoder); eofErr != nil {
		return migrationCLIEnvelope{}, migrationCLIReport{}, fmt.Errorf("specv2: decode StackKits migration decision: %w", eofErr)
	}
	if envelope.APIVersion != migrationResultAPIVersion || envelope.Kind != migrationResultKind {
		return migrationCLIEnvelope{}, migrationCLIReport{}, fmt.Errorf("specv2: unsupported StackKits migration decision contract")
	}
	if strings.TrimSpace(envelope.RequestedTargetKit) != targetKit || len(envelope.Report) == 0 {
		return migrationCLIEnvelope{}, migrationCLIReport{}, fmt.Errorf("specv2: StackKits migration decision lost its target or report")
	}
	var report migrationCLIReport
	if reportErr := json.Unmarshal(envelope.Report, &report); reportErr != nil {
		return migrationCLIEnvelope{}, migrationCLIReport{}, fmt.Errorf("specv2: decode StackKits migration report: %w", reportErr)
	}
	return envelope, report, nil
}

func migrationResultFromDecision(envelope migrationCLIEnvelope, report migrationCLIReport, commandErr error, output []byte) (*MigrationResult, bool, error) {
	result := &MigrationResult{
		Status:            strings.TrimSpace(envelope.Status),
		Blockers:          append([]MigrationBlocker(nil), report.Blockers...),
		ManualActions:     append([]MigrationManualAction(nil), report.ManualActions...),
		GeneratorEligible: envelope.Safety.GeneratorEligible,
	}
	switch result.Status {
	case migrationBlockedStatus:
		return result, false, nil
	case migrationReadyStatus:
		if commandErr != nil {
			return nil, false, fmt.Errorf("specv2: ready StackKits migration exited unsuccessfully: %w: %s", commandErr, tailText(output, validateOutputTail))
		}
		return result, false, nil
	case migrationCompletedStatus:
	default:
		return nil, false, fmt.Errorf("specv2: unsupported StackKits migration status")
	}
	if commandErr != nil {
		return nil, false, fmt.Errorf("specv2: completed StackKits migration exited unsuccessfully: %w: %s", commandErr, tailText(output, validateOutputTail))
	}
	if !envelope.Safety.CUEValidV2 || envelope.Completion == nil ||
		envelope.Completion.APIVersion != migrationCompletionAPI || envelope.Completion.Kind != migrationCompletionKind ||
		envelope.Completion.Status != migrationCompletedStatus || len(envelope.Completion.CanonicalStackSpec) == 0 {
		return nil, false, fmt.Errorf("specv2: completed StackKits migration omitted its CUE-valid canonical spec")
	}
	return result, true, nil
}

func (v *CLIValidator) validateCompletedMigration(ctx context.Context, specPath, targetKit string, completion *migrationCLICompletion) (map[string]any, error) {
	specData, readErr := readBoundedMigrationFile(specPath)
	if readErr != nil {
		return nil, fmt.Errorf("specv2: read completed StackSpec v2: %w", readErr)
	}
	canonical, decodeErr := decodeMigrationSpec(specData)
	if decodeErr != nil {
		return nil, fmt.Errorf("specv2: decode completed StackSpec v2: %w", decodeErr)
	}
	completionJSON, _ := json.Marshal(completion.CanonicalStackSpec)
	canonicalJSON, _ := json.Marshal(canonical)
	if !bytes.Equal(completionJSON, canonicalJSON) {
		return nil, fmt.Errorf("specv2: StackKits migration outputs disagree on the canonical spec")
	}
	apiVersion, _ := canonical["apiVersion"].(string)
	kit, _ := canonical["kit"].(map[string]any)
	kitSlug, _ := kit["slug"].(string)
	if apiVersion != SpecAPIVersion || strings.TrimSpace(kitSlug) != targetKit {
		return nil, fmt.Errorf("specv2: completed StackKits migration escaped the requested v2 kit contract")
	}
	if validateErr := v.ValidateSpec(ctx, canonical); validateErr != nil {
		return nil, fmt.Errorf("specv2: validate completed StackKits migration: %w", validateErr)
	}
	return canonical, nil
}

func writeMigrationDocument(path string, value map[string]any) error {
	if len(value) == 0 {
		return fmt.Errorf("migration document is empty")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) == 0 || len(data) > maxMigrationDocumentBytes {
		return fmt.Errorf("migration document exceeds the bounded input contract")
	}
	return os.WriteFile(path, data, 0o600)
}

func readBoundedMigrationFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maxMigrationDocumentBytes {
		return nil, fmt.Errorf("migration output must be a bounded regular non-symlink file")
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is fixed beneath the private migration workspace.
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != info.Size() {
		return nil, fmt.Errorf("migration output changed while it was read")
	}
	return data, nil
}

func decodeMigrationSpec(data []byte) (map[string]any, error) {
	var spec map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&spec); err != nil {
		return nil, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}
	if len(spec) == 0 {
		return nil, fmt.Errorf("canonical spec is empty")
	}
	return spec, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("JSON contains trailing values")
		}
		return err
	}
	return nil
}

var _ SpecMigrator = (*CLIValidator)(nil)
