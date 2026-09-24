package jobs

import (
	"os"
	"testing"

	"github.com/kombifyio/techstack/pkg/unifier"
	"gopkg.in/yaml.v3"
)

const managedIntentYAML = `name: demo-ionos
stackkit: cloud-kit
mode: easy
metadata:
  server_provisioning_mode: kombify-cloud
services:
  homepage:
    enabled: true
`

func newPersister(t *testing.T) *unifier.SpecPersister {
	t.Helper()
	persister, err := unifier.NewSpecPersisterWithPath(t.TempDir())
	if err != nil {
		t.Fatalf("NewSpecPersister: %v", err)
	}
	return persister
}

// The handoff spec is written to the instance's local disk, and that disk is
// replaced on every deploy. A stack created before the running container had an
// intent but no spec, and the rollout failed at artifact generation with
// "StackKits CLI artifact generation requires persisted stack-spec.yaml" --
// permanently, because nothing regenerated it. Observed live on 2026-07-27.
func TestAMissingHandoffSpecIsRebuiltFromTheIntent(t *testing.T) {
	persister := newPersister(t)
	if persister.StackSpecExists() {
		t.Fatal("fixture already has a handoff spec")
	}

	if err := restoreStackSpecFromIntent(persister, []byte(managedIntentYAML)); err != nil {
		t.Fatalf("restoreStackSpecFromIntent: %v", err)
	}
	if !persister.StackSpecExists() {
		t.Fatal("the handoff spec was not rebuilt; the rollout still cannot generate artifacts")
	}
	data, err := os.ReadFile(persister.GetStackSpecPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("the rebuilt handoff spec is empty")
	}
}

// An existing valid spec may already carry routing, service, or resolved
// managed-runtime details that the intent alone does not describe, so it must
// keep its exact bytes.
func TestAnExistingHandoffSpecKeepsItsExactBytes(t *testing.T) {
	persister := newPersister(t)
	original := []byte("name: already-here\nstackkit: cloud-kit\nnodes:\n  - name: main\n    ip: 10.0.0.1\nservices:\n  homepage:\n    enabled: true\n")
	if _, _, err := persister.SaveStackSpecBytes(original); err != nil {
		t.Fatal(err)
	}

	if err := restoreStackSpecFromIntent(persister, []byte(managedIntentYAML)); err != nil {
		t.Fatalf("restoreStackSpecFromIntent: %v", err)
	}
	data, err := os.ReadFile(persister.GetStackSpecPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(original) {
		t.Fatalf("the existing handoff spec was rewritten:\n%s", data)
	}
}

// Unreadable intent must be reported, not silently skipped: a rollout that
// proceeds without a handoff spec fails later and further from the cause.
func TestUnreadableIntentIsReported(t *testing.T) {
	persister := newPersister(t)

	if err := restoreStackSpecFromIntent(persister, []byte("\tthis: [is not yaml\n")); err == nil {
		t.Fatal("malformed intent was accepted")
	}
}

// Missing inputs and non-StackKit intent are valid no-op boundaries: none may
// invent a persisted handoff or panic.
func TestRestoreStackSpecNoOpCasesDoNotPersistHandoff(t *testing.T) {
	tests := []struct {
		name      string
		persister *unifier.SpecPersister
		intent    []byte
	}{
		{"nil persister", nil, []byte(managedIntentYAML)},
		{"empty intent", newPersister(t), nil},
		{"non-StackKit intent", newPersister(t), []byte("name: legacy\nservices: {}\n")},
	}

	for _, tt := range tests {
		if err := restoreStackSpecFromIntent(tt.persister, tt.intent); err != nil {
			t.Fatalf("%s: restoreStackSpecFromIntent: %v", tt.name, err)
		}
		if tt.persister != nil && tt.persister.StackSpecExists() {
			t.Fatalf("%s persisted a handoff spec", tt.name)
		}
	}
}

const kitAliasIntentYAML = `name: demo-ionos
kit: cloud-kit
mode: easy
metadata:
  server_provisioning_mode: kombify-cloud
services:
  homepage:
    enabled: true
`

// The persisted handoff normalizes a v2 kit alias fallback while retaining an
// explicit stackkit as authority when both names are present.
func TestRestoreStackSpecCanonicalizesKitAuthority(t *testing.T) {
	tests := []struct {
		name             string
		intent           string
		wantAliasRemoved bool
	}{
		{"kit alias fallback", kitAliasIntentYAML, true},
		{"explicit stackkit wins", "name: demo-ionos\nstackkit: cloud-kit\nkit: basement-kit\nservices: {}\n", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			persister := newPersister(t)
			if err := restoreStackSpecFromIntent(persister, []byte(tt.intent)); err != nil {
				t.Fatalf("restoreStackSpecFromIntent: %v", err)
			}
			data, err := os.ReadFile(persister.GetStackSpecPath())
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]interface{}
			if err := yaml.Unmarshal(data, &document); err != nil {
				t.Fatalf("decode persisted handoff spec: %v", err)
			}
			if document["stackkit"] != "cloud-kit" || document["name"] != "demo-ionos" {
				t.Fatalf("persisted handoff identity = %#v", document)
			}
			if _, exists := document["kit"]; tt.wantAliasRemoved && exists {
				t.Fatalf("persisted handoff retained the v2-only kit alias: %#v", document)
			}
		})
	}
}

// Existing handoffs may carry pre-fix aliases or list-shaped services. The
// repair must normalize the persisted document the StackKits CLI reads while
// retaining unrelated operator choices.
func TestRestoreStackSpecRepairsPersistedHandoffShapes(t *testing.T) {
	tests := []struct {
		name            string
		broken          string
		wantMode        string
		wantHomepageMap bool
	}{
		{"removes v2 kit alias", "name: demo\nkit: cloud-kit\nstackkit: cloud-kit\nmode: easy\n", "easy", false},
		{"normalizes list services without kit alias", "name: demo\nstackkit: cloud-kit\nservices:\n  - name: homepage\n    type: homepage\n", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			persister := newPersister(t)
			if _, _, err := persister.SaveStackSpecBytes([]byte(tt.broken)); err != nil {
				t.Fatal(err)
			}
			if err := restoreStackSpecFromIntent(persister, []byte(kitAliasIntentYAML)); err != nil {
				t.Fatalf("restoreStackSpecFromIntent: %v", err)
			}
			data, err := os.ReadFile(persister.GetStackSpecPath())
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]interface{}
			if err := yaml.Unmarshal(data, &document); err != nil {
				t.Fatalf("decode repaired handoff spec: %v", err)
			}
			if document["stackkit"] != "cloud-kit" {
				t.Fatalf("repaired handoff lost kit selection: %#v", document)
			}
			if _, exists := document["kit"]; exists {
				t.Fatalf("repaired handoff retained v2-only kit alias: %#v", document)
			}
			if tt.wantMode != "" && document["mode"] != tt.wantMode {
				t.Fatalf("repaired handoff lost mode: %#v", document)
			}
			if tt.wantHomepageMap {
				services, ok := document["services"].(map[string]interface{})
				homepage := mapFromInterface(services["homepage"])
				if !ok || homepage["type"] != "homepage" {
					t.Fatalf("repaired services = %#v, want homepage map", document["services"])
				}
			}
		})
	}
}

const listServicesIntentYAML = `name: demo-ionos
kit: cloud-kit
mode: easy
metadata:
  server_provisioning_mode: kombify-cloud
services:
  - name: homepage
    type: homepage
  - name: whoami
    type: whoami
    enabled: false
`

// core.InputServiceSpecs accepts services as a list or a map, but the StackKits
// v1 decoder accepts only a mapping. Prove the conversion on the persisted file
// the CLI actually reads, including explicit owner choices and preserved fields.
func TestTheWrittenHandoffSpecUsesAServicesMap(t *testing.T) {
	persister := newPersister(t)
	if err := restoreStackSpecFromIntent(persister, []byte(listServicesIntentYAML)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(persister.GetStackSpecPath())
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]interface{}
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode persisted handoff spec: %v", err)
	}
	services, ok := document["services"].(map[string]interface{})
	if !ok {
		t.Fatalf("persisted services = %T, want a map", document["services"])
	}
	homepage := mapFromInterface(services["homepage"])
	if homepage["enabled"] != true || homepage["type"] != "homepage" {
		t.Fatalf("persisted homepage = %#v", homepage)
	}
	if whoami := mapFromInterface(services["whoami"]); whoami["enabled"] != false || whoami["type"] != "whoami" {
		t.Fatalf("persisted whoami = %#v", whoami)
	}
}
