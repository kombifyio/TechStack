package specv2

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// updateGolden rewrites the checked-in projection goldens:
//
//	go test ./pkg/specv2 -run Golden -update
var updateGolden = flag.Bool("update", false, "rewrite golden projection files")

const examplesDir = "../../docs/examples"

// goldenCases pins the wizard->v2 parity surface: checked-in kit seeds
// (authored by the real `stackkit init`) projected by checked-in intents must
// produce byte-identical specs. This replaces regex fixture checks for the
// native-v2 lane; the pinned CLI remains the semantic authority via
// TestProjectionGoldenValidatesWithStackKitsCLI.
var goldenCases = []struct {
	name       string
	intentFile string
	seedKit    string
	goldenFile string
}{
	{
		name:       "first-run-basement",
		intentFile: "wizard-first-run.intent.json",
		seedKit:    "basement-kit",
		goldenFile: "wizard-first-run.golden.json",
	},
	{
		name:       "expansion-join-basement",
		intentFile: "wizard-expansion-join.intent.json",
		seedKit:    "basement-kit",
		goldenFile: "wizard-expansion-join.golden.json",
	},
}

func loadGoldenIntent(t *testing.T, file string) WizardIntent {
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

func projectGoldenCase(t *testing.T, intentFile, seedKit string) *Projection {
	t.Helper()
	seeds := &TemplateSeedSource{Root: filepath.Join(examplesDir, "seeds")}
	seed, err := seeds.Seed(seedKit)
	if err != nil {
		t.Fatalf("load seed %s: %v", seedKit, err)
	}
	intent := loadGoldenIntent(t, intentFile)
	projection, err := Project(seed, intent, intent.HomelabID)
	if err != nil {
		t.Fatalf("project %s: %v", intentFile, err)
	}
	return projection
}

func TestProjectionGolden(t *testing.T) {
	for _, tc := range goldenCases {
		t.Run(tc.name, func(t *testing.T) {
			projection := projectGoldenCase(t, tc.intentFile, tc.seedKit)
			got, err := json.MarshalIndent(projection.Spec, "", "  ")
			if err != nil {
				t.Fatalf("marshal projection: %v", err)
			}
			got = append(got, '\n')

			goldenPath := filepath.Join(examplesDir, tc.goldenFile)
			if *updateGolden {
				if err := os.WriteFile(goldenPath, got, 0o600); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden %s (regenerate with -update): %v", tc.goldenFile, err)
			}
			if string(got) != string(want) {
				t.Fatalf("projection drifted from %s — inspect the diff and regenerate with -update if intended.\ngot:\n%s", tc.goldenFile, got)
			}
		})
	}
}

// TestProjectionGoldenValidatesWithStackKitsCLI proves the goldens against the
// real acceptance authority. It runs wherever a StackKits CLI is available
// (TECHSTACK_STACKKIT_CLI) and skips otherwise, so plain unit runs stay
// hermetic.
func TestProjectionGoldenValidatesWithStackKitsCLI(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv(StackKitCLIEnv))
	if binary == "" {
		t.Skipf("set %s to validate goldens against the real CLI", StackKitCLIEnv)
	}
	validator := &CLIValidator{Binary: binary}
	for _, tc := range goldenCases {
		t.Run(tc.name, func(t *testing.T) {
			projection := projectGoldenCase(t, tc.intentFile, tc.seedKit)
			if err := validator.ValidateSpec(context.Background(), projection.Spec); err != nil {
				t.Fatalf("pinned CLI rejected the %s projection: %v", tc.name, err)
			}
		})
	}
}
