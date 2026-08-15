package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/unifier"
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

// An existing spec may already carry a routing overlay or a resolved
// managed-runtime target that the intent alone does not describe, so it must
// never be overwritten.
func TestAnExistingHandoffSpecIsLeftAlone(t *testing.T) {
	persister := newPersister(t)
	original := []byte("name: already-here\nstackkit: cloud-kit\n")
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
		t.Fatalf("the existing handoff spec was overwritten:\n%s", data)
	}
}

// A stack that is not a StackKits stack has no handoff spec to rebuild, and
// inventing one would be worse than the caller's own error.
func TestANonStackKitIntentRebuildsNothing(t *testing.T) {
	persister := newPersister(t)

	if err := restoreStackSpecFromIntent(persister, []byte("name: legacy\nservices: {}\n")); err != nil {
		t.Fatalf("restoreStackSpecFromIntent: %v", err)
	}
	if persister.StackSpecExists() {
		t.Fatal("a handoff spec was invented for a non-StackKits intent")
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

// Nothing to do without a persister or an intent, and neither may panic.
func TestRestoreIsANoOpWithoutInputs(t *testing.T) {
	if err := restoreStackSpecFromIntent(nil, []byte(managedIntentYAML)); err != nil {
		t.Fatalf("nil persister: %v", err)
	}
	persister := newPersister(t)
	if err := restoreStackSpecFromIntent(persister, nil); err != nil {
		t.Fatalf("empty intent: %v", err)
	}
	if _, err := os.Stat(filepath.Join(persister.BaseDir, unifier.StackSpecFilename)); !os.IsNotExist(err) {
		t.Fatal("an empty intent produced a handoff spec")
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

// core.InputSpec accepts the kit under either name, but the handoff projection
// recognised only "stackkit". An intent written with "kit" projected to nothing,
// no handoff spec was ever persisted, and the rollout failed at artifact
// generation with no way to recover. Live on 2026-07-27 a cloud-kit stack whose
// intent carried kit: cloud-kit could not be rolled out at all.
func TestTheKitAliasStillProducesAHandoffSpec(t *testing.T) {
	persister := newPersister(t)

	if err := restoreStackSpecFromIntent(persister, []byte(kitAliasIntentYAML)); err != nil {
		t.Fatalf("restoreStackSpecFromIntent: %v", err)
	}
	if !persister.StackSpecExists() {
		t.Fatal("an intent using the kit alias produced no handoff spec")
	}
	data, err := os.ReadFile(persister.GetStackSpecPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "stackkit: cloud-kit") {
		t.Fatalf("the handoff spec does not name the kit:\n%s", data)
	}
}

// An explicit stackkit is the authority; the alias must not override it.
func TestAnExplicitStackKitWinsOverTheAlias(t *testing.T) {
	normalized := withStackKitFromKitAlias(map[string]interface{}{
		"stackkit": "cloud-kit",
		"kit":      "basement-kit",
	})
	if normalized["stackkit"] != "cloud-kit" {
		t.Fatalf("stackkit = %v, want the explicit value", normalized["stackkit"])
	}
}

// Normalizing must not mutate the caller's map: the intent is persisted state.
func TestAliasNormalizationDoesNotMutateTheIntent(t *testing.T) {
	intent := map[string]interface{}{"kit": "cloud-kit"}
	withStackKitFromKitAlias(intent)
	if _, exists := intent["stackkit"]; exists {
		t.Fatal("normalizing mutated the caller's intent map")
	}
}

// The alias must be moved, not copied. A v1 StackSpec that also carries the
// v2-only "kit" field is rejected outright by the StackKits CLI:
// "v1 StackSpec contains v2-only top-level fields kit; refusing to discard".
// Live on 2026-07-27 that turned one rollout failure into another, one step
// further along.
func TestTheKitAliasIsMovedNotCopied(t *testing.T) {
	normalized := withStackKitFromKitAlias(map[string]interface{}{
		"name": "demo",
		"kit":  "cloud-kit",
	})
	if normalized["stackkit"] != "cloud-kit" {
		t.Fatalf("stackkit = %v, want cloud-kit", normalized["stackkit"])
	}
	if _, exists := normalized["kit"]; exists {
		t.Fatal("the v2-only kit field survived; the StackKits CLI will refuse this spec")
	}
	if normalized["name"] != "demo" {
		t.Fatal("normalizing dropped an unrelated field")
	}
}

// The written handoff spec is what the CLI actually reads, so assert on it.
func TestTheWrittenHandoffSpecCarriesNoKitField(t *testing.T) {
	persister := newPersister(t)
	if err := restoreStackSpecFromIntent(persister, []byte(kitAliasIntentYAML)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(persister.GetStackSpecPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "\nkit:") || strings.HasPrefix(string(data), "kit:") {
		t.Fatalf("the handoff spec still carries a top-level kit field:\n%s", data)
	}
}

// An earlier release wrote a spec carrying both kit and stackkit by copying the
// alias instead of moving it. The StackKits CLI refuses that shape outright, and
// the bad spec then survived every later rollout because nothing would overwrite
// it. Live on 2026-07-27 the fix for new specs changed nothing for the stack
// that already had one.
func TestAnExistingSpecCarryingTheV2KitFieldIsRepaired(t *testing.T) {
	persister := newPersister(t)
	broken := []byte("name: demo\nkit: cloud-kit\nstackkit: cloud-kit\nmode: easy\n")
	if _, _, err := persister.SaveStackSpecBytes(broken); err != nil {
		t.Fatal(err)
	}

	if err := restoreStackSpecFromIntent(persister, []byte(kitAliasIntentYAML)); err != nil {
		t.Fatalf("restoreStackSpecFromIntent: %v", err)
	}
	data, err := os.ReadFile(persister.GetStackSpecPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "\nkit:") || strings.HasPrefix(string(data), "kit:") {
		t.Fatalf("the refused kit field survived:\n%s", data)
	}
	if !strings.Contains(string(data), "stackkit: cloud-kit") {
		t.Fatalf("the repair lost the kit selection:\n%s", data)
	}
	if !strings.Contains(string(data), "mode: easy") {
		t.Fatalf("the repair dropped unrelated persisted fields:\n%s", data)
	}
}

// A healthy spec must still be left exactly as it is: it may carry a routing
// overlay or a resolved managed-runtime target the intent does not describe.
func TestAHealthySpecIsStillNotRewritten(t *testing.T) {
	persister := newPersister(t)
	original := []byte("name: already-here\nstackkit: cloud-kit\nnodes:\n  - name: main\n    ip: 10.0.0.1\n")
	if _, _, err := persister.SaveStackSpecBytes(original); err != nil {
		t.Fatal(err)
	}

	if err := restoreStackSpecFromIntent(persister, []byte(kitAliasIntentYAML)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(persister.GetStackSpecPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(original) {
		t.Fatalf("a healthy spec was rewritten:\n%s", data)
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
// v1 StackSpec decoder accepts only a mapping and fails the whole rollout on a
// sequence. Live on 2026-07-27:
//
//	cannot decode v1 StackSpec: yaml: unmarshal errors:
//	  line 37: cannot unmarshal !!seq into map[string]interface {}
func TestListShapedServicesBecomeAMap(t *testing.T) {
	normalized := withStackKitSpecShape(map[string]interface{}{
		"kit": "cloud-kit",
		"services": []interface{}{
			map[string]interface{}{"name": "homepage", "type": "homepage"},
			map[string]interface{}{"name": "whoami", "enabled": false},
		},
	})

	services, ok := normalized["services"].(map[string]interface{})
	if !ok {
		t.Fatalf("services = %T, want a map", normalized["services"])
	}
	homepage := mapFromInterface(services["homepage"])
	if homepage["enabled"] != true {
		t.Fatalf("homepage = %+v, want enabled by its presence in the list", homepage)
	}
	if homepage["type"] != "homepage" {
		t.Fatalf("homepage lost its other fields: %+v", homepage)
	}
	// An explicit flag is an owner decision and must survive.
	if whoami := mapFromInterface(services["whoami"]); whoami["enabled"] != false {
		t.Fatalf("whoami = %+v, want the stated enabled:false", whoami)
	}
	if _, exists := normalized["name"]; exists {
		t.Fatal("normalizing invented a field")
	}
}

// A map-shaped services block is already what the StackSpec wants.
func TestMapShapedServicesAreLeftAlone(t *testing.T) {
	original := map[string]interface{}{"homepage": map[string]interface{}{"enabled": true}}
	normalized := withServicesAsMap(map[string]interface{}{"services": original})
	if got, ok := normalized["services"].(map[string]interface{}); !ok || len(got) != 1 {
		t.Fatalf("services = %+v, want the original map", normalized["services"])
	}
}

// The persisted intent must never be mutated.
func TestServiceNormalizationDoesNotMutateTheIntent(t *testing.T) {
	intent := map[string]interface{}{
		"services": []interface{}{map[string]interface{}{"name": "homepage"}},
	}
	withServicesAsMap(intent)
	if _, stillAList := intent["services"].([]interface{}); !stillAList {
		t.Fatal("normalizing rewrote the caller's intent map")
	}
}

// End to end on the file the CLI actually reads.
func TestTheWrittenHandoffSpecUsesAServicesMap(t *testing.T) {
	persister := newPersister(t)
	if err := restoreStackSpecFromIntent(persister, []byte(listServicesIntentYAML)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(persister.GetStackSpecPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "- name: homepage") {
		t.Fatalf("the handoff spec still lists services as a sequence:\n%s", data)
	}
	if !strings.Contains(string(data), "homepage:") {
		t.Fatalf("the handoff spec lost the service:\n%s", data)
	}
}

// Repairing must not key on one field. Gating on "kit" meant that once the
// v2-only field was removed the repair returned before looking at anything
// else, so a spec that was also list-shaped stayed broken. Live on 2026-07-27
// the rollout kept failing on the identical decode error after the kit repair
// went out.
func TestAPersistedSpecWithListServicesIsRepairedEvenWithoutTheKitField(t *testing.T) {
	persister := newPersister(t)
	broken := []byte("name: demo\nstackkit: cloud-kit\nservices:\n  - name: homepage\n    type: homepage\n")
	if _, _, err := persister.SaveStackSpecBytes(broken); err != nil {
		t.Fatal(err)
	}

	if err := restoreStackSpecFromIntent(persister, []byte(kitAliasIntentYAML)); err != nil {
		t.Fatalf("restoreStackSpecFromIntent: %v", err)
	}
	data, err := os.ReadFile(persister.GetStackSpecPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "- name: homepage") {
		t.Fatalf("the list-shaped services block survived:\n%s", data)
	}
	if !strings.Contains(string(data), "homepage:") {
		t.Fatalf("the repair lost the service:\n%s", data)
	}
	if !strings.Contains(string(data), "stackkit: cloud-kit") {
		t.Fatalf("the repair lost the kit selection:\n%s", data)
	}
}

// A spec that already has an acceptable shape must keep its exact bytes, so
// nothing churns on every rollout and an overlay is never disturbed.
func TestAnAcceptableSpecKeepsItsExactBytes(t *testing.T) {
	persister := newPersister(t)
	original := []byte("name: already-here\nstackkit: cloud-kit\nservices:\n  homepage:\n    enabled: true\n")
	if _, _, err := persister.SaveStackSpecBytes(original); err != nil {
		t.Fatal(err)
	}

	if err := restoreStackSpecFromIntent(persister, []byte(kitAliasIntentYAML)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(persister.GetStackSpecPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(original) {
		t.Fatalf("an acceptable spec was rewritten:\n%s", data)
	}
}
