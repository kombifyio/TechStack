package specv2

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
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
	goalWorkloads, goalBindings, err := loadGoalMetadata(manifest, "")
	if err != nil {
		t.Fatalf("load release compatibility: %v", err)
	}
	validator := &CLIValidator{Binary: binary, goalWorkloads: goalWorkloads, goalBindings: goalBindings}
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
				// The release ships Smart Home through the wizard adapter too.
				if len(projection.UnmappedGoals) != 0 || workloads["smart-home"] == nil {
					t.Fatalf("unmapped goals = %#v, workloads = %#v", projection.UnmappedGoals, workloads)
				}
			})
		}
	})
}

// TestWizardInstallChoicesValidateWithStackKitsCLI proves that an installing
// alternative and an installing add-on setting reach the StackSpec the pinned
// release accepts. It needs the release CLI, compatibility manifest and
// use-case catalog, and skips otherwise.
func TestWizardInstallChoicesValidateWithStackKitsCLI(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv(StackKitCLIEnv))
	manifest := strings.TrimSpace(os.Getenv(CompatibilityManifestEnv))
	if binary == "" || manifest == "" || strings.TrimSpace(os.Getenv(stackkitrelease.UseCaseCatalogEnv)) == "" {
		t.Skipf("set %s, %s and %s to validate install choices", StackKitCLIEnv, CompatibilityManifestEnv, stackkitrelease.UseCaseCatalogEnv)
	}
	goalWorkloads, goalBindings, err := loadGoalMetadata(manifest, "")
	if err != nil {
		t.Fatalf("load release compatibility: %v", err)
	}
	validator := &CLIValidator{Binary: binary, goalWorkloads: goalWorkloads, goalBindings: goalBindings}

	// The native seed the image authors at build time.
	seedRoot := t.TempDir()
	seedDir := filepath.Join(seedRoot, KitSlugBasement)
	if err := os.Mkdir(seedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(binary, "--no-log", "--chdir", seedDir, "init", KitSlugBasement, "--non-interactive",
		"--name", "techstack-spec-template", "--owner-source=local", "--owner-email", "owner@smoke.stackkit.cc",
		"--owner-username", "owner", "--api-version", NativeSpecAPIVersion, "--catalog-defaults",
		"--domain", "template.invalid").CombinedOutput()
	if err != nil {
		t.Fatalf("author native seed: %v: %s", err, output)
	}
	seed, err := (&TemplateSeedSource{Root: seedRoot}).Seed(KitSlugBasement)
	if err != nil {
		t.Fatalf("read native seed: %v", err)
	}

	intent := foundIntent("Install Choices", "files")
	intent.UseCaseSettings = map[string]map[string]any{"files": {"backend": "nextcloud", "office-editing": true}}
	projection, err := NewReleaseProjector(validator).Project(context.Background(), seed, intent, "hl-install-choices")
	if err != nil {
		t.Fatalf("project install choices: %v", err)
	}
	if err := validator.ValidateSpec(context.Background(), projection.Spec); err != nil {
		t.Fatalf("pinned CLI rejected the install choices: %v", err)
	}
	workloads, _ := projection.Spec["workloads"].(map[string]any)
	modules, _ := projection.Spec["modules"].(map[string]any)
	files, _ := workloads["files"].(map[string]any)
	office, _ := workloads["files-office"].(map[string]any)
	if files["alternative"] != "nextcloud" || office["alternative"] != "euro-office" ||
		modules["stackkits-nextcloud-runtime"] == nil || modules["stackkits-euro-office-runtime"] == nil {
		t.Fatalf("install choices missing from the StackSpec: workloads=%v modules=%v", workloads, modules)
	}
}
