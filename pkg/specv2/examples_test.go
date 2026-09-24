package specv2

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const examplesDir = "../../docs/examples"

var projectionExampleCases = []struct {
	name       string
	intentFile string
	seedKit    string
}{
	{
		name:       "first-run-basement",
		intentFile: "wizard-first-run.intent.json",
		seedKit:    "basement-kit",
	},
	{
		name:       "expansion-join-basement",
		intentFile: "wizard-expansion-join.intent.json",
		seedKit:    "basement-kit",
	},
}

func loadExampleIntent(t *testing.T, file string) WizardIntent {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(examplesDir, file))
	if err != nil {
		t.Fatalf("read intent %s: %v", file, err)
	}
	var intent WizardIntent
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&intent); err != nil {
		t.Fatalf("decode intent %s: %v", file, err)
	}
	return intent
}

func projectExampleCase(t *testing.T, intentFile, seedKit string) (*Projection, WizardIntent) {
	return projectExampleCaseWith(t, NewReleaseProjector(releaseGoalAuthorFixture{}), intentFile, seedKit)
}

func projectExampleCaseWith(t *testing.T, projector Projector, intentFile, seedKit string) (*Projection, WizardIntent) {
	t.Helper()
	seeds := &TemplateSeedSource{Root: filepath.Join(examplesDir, "seeds")}
	seed, err := seeds.Seed(seedKit)
	if err != nil {
		t.Fatalf("load seed %s: %v", seedKit, err)
	}
	intent := loadExampleIntent(t, intentFile)
	projection, err := projector.Project(context.Background(), seed, intent, intent.HomelabID)
	if err != nil {
		t.Fatalf("project %s: %v", intentFile, err)
	}
	return projection, intent
}

func TestProjectionExamplesPreserveStackSpecContract(t *testing.T) {
	for _, tc := range projectionExampleCases {
		t.Run(tc.name, func(t *testing.T) {
			projection, intent := projectExampleCase(t, tc.intentFile, tc.seedKit)
			if projection.Spec["apiVersion"] != "stackkit/v2alpha1" || projection.Spec["kind"] != "StackSpec" {
				t.Fatalf("projection schema identity = %v/%v", projection.Spec["apiVersion"], projection.Spec["kind"])
			}
			kit, ok := projection.Spec["kit"].(map[string]any)
			if !ok || kit["slug"] != tc.seedKit {
				t.Fatalf("projection kit = %#v", projection.Spec["kit"])
			}
			metadata, ok := projection.Spec["metadata"].(map[string]any)
			if !ok || metadata["fleetRef"] != intent.HomelabID || metadata["stackId"] != "golden-homelab" {
				t.Fatalf("projection metadata = %#v", projection.Spec["metadata"])
			}
		})
	}
}

// TestProjectionExamplesValidateWithStackKitsCLI proves the examples against the
// real acceptance authority. It runs wherever a StackKits CLI is available
// (TECHSTACK_STACKKIT_CLI) and skips otherwise, so plain unit runs stay
// hermetic.
func TestProjectionExamplesValidateWithStackKitsCLI(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv(StackKitCLIEnv))
	if binary == "" {
		t.Skipf("set %s to validate examples against the real CLI", StackKitCLIEnv)
	}
	manifest := strings.TrimSpace(os.Getenv(CompatibilityManifestEnv))
	if manifest == "" {
		t.Skipf("set %s to project examples with the release manifest", CompatibilityManifestEnv)
	}
	goalWorkloads, err := loadGoalWorkloads(manifest, "")
	if err != nil {
		t.Fatalf("load release compatibility: %v", err)
	}
	validator := &CLIValidator{Binary: binary, goalWorkloads: goalWorkloads}
	projector := NewReleaseProjector(validator)
	for _, tc := range projectionExampleCases {
		t.Run(tc.name, func(t *testing.T) {
			projection, _ := projectExampleCaseWith(t, projector, tc.intentFile, tc.seedKit)
			if err := validator.ValidateSpec(context.Background(), projection.Spec); err != nil {
				t.Fatalf("pinned CLI rejected the %s projection: %v", tc.name, err)
			}
		})
	}
	t.Run("published-goal-delivery", func(t *testing.T) {
		for _, kitSlug := range []string{KitSlugBasement, KitSlugCloud, KitSlugModern} {
			t.Run(kitSlug, func(t *testing.T) {
				seed, err := (&TemplateSeedSource{Root: filepath.Join(examplesDir, "seeds")}).Seed(kitSlug)
				if err != nil {
					t.Fatalf("load release-authored seed: %v", err)
				}
				intent := foundIntent("Release Goals", "files", "photos", "vault", "smart-home")
				intent.KitAssignment.KitSlug = kitSlug
				projection, err := projector.Project(context.Background(), seed, intent, "hl-release-goals")
				if err != nil {
					t.Fatalf("project published goals: %v", err)
				}
				if err := validator.ValidateSpec(context.Background(), projection.Spec); err != nil {
					t.Fatalf("pinned CLI rejected its published goal projection: %v", err)
				}
				workloads := projection.Spec["workloads"].(map[string]any)
				if workloads["files"].(map[string]any)["alternative"] != "cloudreve" ||
					workloads["photos"].(map[string]any)["alternative"] != "immich" ||
					workloads["vault"].(map[string]any)["alternative"] != "vaultwarden" {
					t.Fatalf("published goal workloads = %#v", workloads)
				}
				if len(projection.UnmappedGoals) != 1 || projection.UnmappedGoals[0] != "smart-home" {
					t.Fatalf("unmapped goals = %#v", projection.UnmappedGoals)
				}
			})
		}
	})
}
