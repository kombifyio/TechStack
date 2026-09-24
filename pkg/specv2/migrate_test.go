package specv2

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCLIValidatorCompletesMigrationThroughPinnedAuthority(t *testing.T) {
	candidate := migrationCandidate()
	var migrationCalls, validationCalls int
	validator := &CLIValidator{
		Binary: "pinned-stackkit", Timeout: time.Second,
		commandRunner: func(_ context.Context, binary string, args []string, workDir string) ([]byte, error) {
			if binary != "pinned-stackkit" {
				t.Fatalf("migration escaped pinned binary: %q", binary)
			}
			if migrationCommand(args) {
				migrationCalls++
				writeMigrationFixture(t, workDir, completedMigrationFixture(candidate), candidate)
				return nil, nil
			}
			validationCalls++
			return nil, nil
		},
	}

	result, err := validator.CompleteMigration(t.Context(), migrationLegacy(), candidate, KitSlugBasement)
	if err != nil {
		t.Fatalf("CompleteMigration: %v", err)
	}
	if !result.Completed() || result.CanonicalSpec["apiVersion"] != SpecAPIVersion || migrationCalls != 1 || validationCalls != 1 {
		t.Fatalf("completed migration did not converge through pinned authority: result=%#v migration=%d validation=%d", result, migrationCalls, validationCalls)
	}
}

func TestCLIValidatorReturnsBlockedMigrationWithoutCanonicalSpec(t *testing.T) {
	validator := &CLIValidator{
		Binary: "pinned-stackkit", Timeout: time.Second,
		commandRunner: func(_ context.Context, _ string, _ []string, workDir string) ([]byte, error) {
			blocked := migrationEnvelopeFixture(migrationBlockedStatus, false, nil)
			blocked["report"] = map[string]any{
				"status": migrationBlockedStatus,
				"blockers": []any{map[string]any{
					"code": "migration.explicit-intent-required", "field": "kit", "message": "Select the target intent explicitly.",
					"requiredInputs": []any{"kit.slug"}, "suggestedKitProfiles": []any{KitSlugBasement},
				}},
				"manualActions": []any{},
			}
			writeJSONFixture(t, filepath.Join(workDir, "migration-result.json"), blocked)
			return []byte("migration blocked"), errors.New("exit status 1")
		},
	}

	result, err := validator.CompleteMigration(t.Context(), migrationLegacy(), migrationCandidate(), KitSlugBasement)
	if err != nil {
		t.Fatalf("blocked migration returned infrastructure error: %v", err)
	}
	if result == nil || result.Completed() || result.Status != migrationBlockedStatus || len(result.Blockers) == 0 ||
		result.Blockers[0].Field != "kit" || len(result.Blockers[0].RequiredInputs) != 1 || result.CanonicalSpec != nil {
		t.Fatalf("blocked migration was not preserved as typed non-success: %#v", result)
	}
}

func TestCLIValidatorRejectsMalformedMigrationDecision(t *testing.T) {
	validator := &CLIValidator{
		Binary: "pinned-stackkit", Timeout: time.Second,
		commandRunner: func(_ context.Context, _ string, _ []string, workDir string) ([]byte, error) {
			if err := os.WriteFile(filepath.Join(workDir, "migration-result.json"), []byte("{"), 0o600); err != nil {
				t.Fatalf("write malformed migration fixture: %v", err)
			}
			return nil, nil
		},
	}

	result, err := validator.CompleteMigration(t.Context(), migrationLegacy(), migrationCandidate(), KitSlugBasement)
	if err == nil || result != nil {
		t.Fatalf("malformed migration decision was accepted: result=%#v err=%v", result, err)
	}
}

func migrationCommand(args []string) bool {
	for _, arg := range args {
		if arg == "migrate" {
			return true
		}
	}
	return false
}

func migrationLegacy() map[string]any {
	return map[string]any{
		"version": "1", "name": "legacy-homelab", "provider": "homelab",
	}
}

func migrationCandidate() map[string]any {
	return map[string]any{
		"apiVersion": SpecAPIVersion,
		"kind":       "StackSpec",
		"metadata":   map[string]any{"name": "legacy-homelab", "stackId": "legacy-homelab"},
		"kit":        map[string]any{"slug": KitSlugBasement},
		"nodes": []any{map[string]any{
			"id": "main", "siteRef": "home", "roles": []any{"controller", "worker"}, "enabled": true,
		}},
		"sites": []any{map[string]any{"id": "home", "kind": "home"}},
	}
}

func completedMigrationFixture(candidate map[string]any) map[string]any {
	envelope := migrationEnvelopeFixture(migrationCompletedStatus, true, candidate)
	envelope["report"] = map[string]any{
		"status": migrationCompletedStatus, "blockers": []any{}, "manualActions": []any{},
	}
	return envelope
}

func migrationEnvelopeFixture(status string, cueValid bool, candidate map[string]any) map[string]any {
	envelope := map[string]any{
		"apiVersion":         migrationResultAPIVersion,
		"kind":               migrationResultKind,
		"status":             status,
		"requestedTargetKit": KitSlugBasement,
		"source":             map[string]any{"ref": "legacy-stack-spec.json", "sha256": "sha256:fixture", "unknownV1Fields": []any{}},
		"safety": map[string]any{
			"cueValidV2": cueValid, "generatorEligible": cueValid, "notice": "fixture",
		},
	}
	if candidate != nil {
		envelope["completion"] = map[string]any{
			"apiVersion": "stackkit.migration-completion/v1", "kind": "StackSpecV2Completion", "status": migrationCompletedStatus,
			"source":             map[string]any{"ref": "candidate-stack-spec-v2.json", "sha256": "sha256:candidate"},
			"canonicalStackSpec": candidate, "canonicalStackSpecSHA256": "sha256:canonical",
			"candidateIntentSHA256": "sha256:intent", "migrationDecisionRecord": map[string]any{"status": migrationCompletedStatus},
			"migrationReportSHA256": "sha256:report", "resolvedPlanHash": "sha256:plan",
		}
	}
	return envelope
}

func writeMigrationFixture(t *testing.T, workDir string, envelope, candidate map[string]any) {
	t.Helper()
	writeJSONFixture(t, filepath.Join(workDir, "migration-result.json"), envelope)
	writeJSONFixture(t, filepath.Join(workDir, "migrated-stack-spec-v2.json"), candidate)
}

func writeJSONFixture(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal migration fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write migration fixture: %v", err)
	}
}
