package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func writeCanonicalTemplate(t *testing.T, kit string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, kit)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("create template dir: %v", err)
	}
	template := map[string]interface{}{
		"apiVersion": canonicalStackSpecAPIVersion,
		"kind":       "StackSpec",
		"kit":        map[string]interface{}{"slug": kit},
		"metadata":   map[string]interface{}{"name": "techstack-spec-template"},
		"network": map[string]interface{}{
			"mode":   "public-capable",
			"domain": map[string]interface{}{"base": "template.invalid"},
		},
		"storage": map[string]interface{}{"dataRoot": "/opt/data"},
		// Mirrors what "stackkit init" writes: the plan governs where
		// generation may write, and the CLI refuses any other destination.
		"generation": map[string]interface{}{
			"outputRoot": "deploy",
			"strategy":   "kit-template",
			"target":     "opentofu",
		},
	}
	data, err := json.Marshal(template)
	if err != nil {
		t.Fatalf("marshal template: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stack-spec.yaml"), data, 0o600); err != nil {
		t.Fatalf("write template: %v", err)
	}
	t.Setenv(stackKitSpecTemplateEnv, root)
	return root
}

func writeSpec(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stack-spec.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	return path
}

func readSpecDocument(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	document, err := decodeStackSpecDocument(data)
	if err != nil {
		t.Fatalf("decode spec: %v", err)
	}
	return document
}

func TestCanonicalStackSpecForDerivesTheDocumentBesideTheLegacyHandoff(t *testing.T) {
	writeCanonicalTemplate(t, "cloud-kit")
	legacyBody := "stackkit: cloud-kit\nname: demo-stack\nnetwork:\n  domain: demo.example.test\n"
	path := writeSpec(t, legacyBody)

	canonical, err := canonicalStackSpecFor(path, "cloud-kit", "ignored-fallback")
	if err != nil {
		t.Fatalf("canonicalStackSpecFor: %v", err)
	}
	if !canonical.Derived {
		t.Fatal("legacy v1 handoff did not produce a canonical document")
	}
	if filepath.Base(canonical.Path) != canonicalStackSpecFilename {
		t.Fatalf("canonical path = %q, want %s beside the handoff", canonical.Path, canonicalStackSpecFilename)
	}

	// The routing overlay, the managed-runtime hydration, and the v1 repair all
	// own stack-spec.yaml and write shapes a canonical document rejects.
	// Writing over it would have them corrupt the document the CLI executes.
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read handoff: %v", err)
	}
	if string(current) != legacyBody {
		t.Fatalf("persisted handoff = %q, want the exact original bytes", string(current))
	}

	document := readSpecDocument(t, canonical.Path)
	if got := stringFromInterface(document["apiVersion"]); got != canonicalStackSpecAPIVersion {
		t.Fatalf("apiVersion = %q, want %q", got, canonicalStackSpecAPIVersion)
	}
	if got := stringFromInterface(mapFromInterface(document["metadata"])["name"]); got != "demo-stack" {
		t.Fatalf("metadata.name = %q, want demo-stack", got)
	}
	domain := mapFromInterface(mapFromInterface(document["network"])["domain"])
	if got := stringFromInterface(domain["base"]); got != "demo.example.test" {
		t.Fatalf("network.domain.base = %q, want demo.example.test", got)
	}
	// Everything StackKits authored must survive untouched; only the two init
	// overrides are Techstack's to set.
	if got := stringFromInterface(mapFromInterface(document["storage"])["dataRoot"]); got != "/opt/data" {
		t.Fatalf("storage.dataRoot = %q, want the template value", got)
	}
	if got := stringFromInterface(mapFromInterface(document["network"])["mode"]); got != "public-capable" {
		t.Fatalf("network.mode = %q, want the template value", got)
	}
}

// A later routing change must reach the CLI, so the document is re-derived from
// the persisted handoff on every rollout rather than cached.
func TestCanonicalStackSpecForTracksALaterDomainChange(t *testing.T) {
	writeCanonicalTemplate(t, "cloud-kit")
	path := writeSpec(t, "stackkit: cloud-kit\nname: demo-stack\nnetwork:\n  domain: first.example.test\n")

	if _, err := canonicalStackSpecFor(path, "cloud-kit", "demo"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	updated := "stackkit: cloud-kit\nname: demo-stack\nnetwork:\n  domain: second.example.test\n"
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatalf("rewrite handoff: %v", err)
	}

	canonical, err := canonicalStackSpecFor(path, "cloud-kit", "demo")
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	domain := mapFromInterface(mapFromInterface(readSpecDocument(t, canonical.Path)["network"])["domain"])
	if got := stringFromInterface(domain["base"]); got != "second.example.test" {
		t.Fatalf("network.domain.base = %q, want the updated domain", got)
	}
}

func TestCanonicalStackSpecForLeavesAnExistingCanonicalDocumentAlone(t *testing.T) {
	writeCanonicalTemplate(t, "cloud-kit")
	body := `{"apiVersion":"stackkit/v2alpha1","kind":"StackSpec","kit":{"slug":"cloud-kit"},"metadata":{"name":"already-canonical"},"generation":{"outputRoot":"deploy"}}`
	path := writeSpec(t, body)

	canonical, err := canonicalStackSpecFor(path, "cloud-kit", "demo")
	if err != nil {
		t.Fatalf("canonicalStackSpecFor: %v", err)
	}
	if canonical.Derived {
		t.Fatal("a canonical document must not be re-derived")
	}
	if canonical.Path != path {
		t.Fatalf("canonical path = %q, want the handoff itself", canonical.Path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	if string(data) != body {
		t.Fatalf("spec = %q, want the exact original bytes", string(data))
	}
}

// Guessing a domain would silently generate routes for the wrong host.
func TestCanonicalStackSpecForFailsClosedWithoutAResolvableDomain(t *testing.T) {
	writeCanonicalTemplate(t, "cloud-kit")
	path := writeSpec(t, "stackkit: cloud-kit\nname: demo-stack\n")

	_, err := canonicalStackSpecFor(path, "cloud-kit", "demo")
	if err == nil {
		t.Fatal("canonicalStackSpecFor accepted a handoff with no routing domain")
	}
	if !strings.Contains(err.Error(), "routing domain") {
		t.Fatalf("error = %v, want it to name the missing routing domain", err)
	}
}

func TestCanonicalStackSpecForRejectsATemplateForAnotherKit(t *testing.T) {
	root := writeCanonicalTemplate(t, "cloud-kit")
	// Present the cloud-kit document under the basement-kit directory.
	basement := filepath.Join(root, "basement-kit")
	if err := os.MkdirAll(basement, 0o750); err != nil {
		t.Fatalf("create dir: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "cloud-kit", "stack-spec.yaml"))
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	if err := os.WriteFile(filepath.Join(basement, "stack-spec.yaml"), data, 0o600); err != nil {
		t.Fatalf("write template: %v", err)
	}
	path := writeSpec(t, "stackkit: basement-kit\nname: demo\nnetwork:\n  domain: demo.example.test\n")

	_, err = canonicalStackSpecFor(path, "basement-kit", "demo")
	if err == nil || !strings.Contains(err.Error(), "declares kit") {
		t.Fatalf("error = %v, want a kit mismatch rejection", err)
	}
}

func TestCanonicalStackSpecForRequiresTheTemplateRoot(t *testing.T) {
	t.Setenv(stackKitSpecTemplateEnv, "")
	path := writeSpec(t, "stackkit: cloud-kit\nname: demo\nnetwork:\n  domain: demo.example.test\n")

	_, err := canonicalStackSpecFor(path, "cloud-kit", "demo")
	if err == nil || !strings.Contains(err.Error(), stackKitSpecTemplateEnv) {
		t.Fatalf("error = %v, want it to name %s", err, stackKitSpecTemplateEnv)
	}
}

// The resolution chain must match stackKitNetworkToKombinationNetwork, which is
// how the rest of Techstack reads a domain out of the same document.
func TestCanonicalStackSpecForMirrorsTheProductDomainResolution(t *testing.T) {
	for name, expectation := range map[string]struct {
		handoff string
		want    string
	}{
		"explicit top-level domain": {
			handoff: "stackkit: cloud-kit\nname: demo\ndomain: top.example.test\n",
			want:    "top.example.test",
		},
		"network domain outranks the top level": {
			handoff: "stackkit: cloud-kit\nname: demo\ndomain: top.example.test\nnetwork:\n  domain: net.example.test\n",
			want:    "top.example.test",
		},
		"network mode that is really a domain": {
			handoff: "stackkit: cloud-kit\nname: demo\nnetwork:\n  mode: legacy.example.test\n",
			want:    "legacy.example.test",
		},
		"cloud-context kombify.me address mode": {
			handoff: "stackkit: cloud-kit\nname: demo\ncontext: cloud\nmetadata:\n  address_mode: kombify-me\n",
			want:    addressModeKombifyMeDomain,
		},
		"cloud-context requested address mode": {
			handoff: "stackkit: cloud-kit\nname: demo\ncontext: cloud\nmetadata:\n  requested_address_mode: kombify-me\n",
			want:    addressModeKombifyMeDomain,
		},
	} {
		expectation := expectation
		t.Run(name, func(t *testing.T) {
			writeCanonicalTemplate(t, "cloud-kit")
			path := writeSpec(t, expectation.handoff)
			canonical, err := canonicalStackSpecFor(path, "cloud-kit", "demo")
			if err != nil {
				t.Fatalf("canonicalStackSpecFor: %v", err)
			}
			domain := mapFromInterface(mapFromInterface(readSpecDocument(t, canonical.Path)["network"])["domain"])
			if got := stringFromInterface(domain["base"]); got != expectation.want {
				t.Fatalf("network.domain.base = %q, want %q", got, expectation.want)
			}
		})
	}
}

// A rejection has to say what the handoff actually carried, not restate the
// rule. A kombify.me address mode outside a cloud context on a self-hosted
// stack resolves nothing.
func TestCanonicalStackSpecForRejectionNamesTheObservedKeys(t *testing.T) {
	writeCanonicalTemplate(t, "cloud-kit")
	path := writeSpec(t, "stackkit: cloud-kit\nname: demo\nmode: easy\nprovider: local\nmetadata:\n  address_mode: kombify-me\n  owner_source: local\n")

	_, err := canonicalStackSpecFor(path, "cloud-kit", "demo")
	if err == nil {
		t.Fatal("canonicalStackSpecFor accepted an address mode outside a cloud context")
	}
	for _, expected := range []string{"document keys:", "metadata keys:", "address_mode", "owner_source", "stackkit"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("error = %v, want it to name %q", err, expected)
		}
	}
}

// A managed cloud runtime has no operator-chosen domain. The live demo stack's
// handoff carries no network block at all, which is why nothing in the explicit
// chain resolved:
//
//	document keys: metadata,mode,name,nodes,services,ssh,stackkit
func TestCanonicalStackSpecForUsesTheManagedRuntimeAddressWhenNothingIsChosen(t *testing.T) {
	for name, handoff := range map[string]string{
		"kombify-cloud provisioning": "stackkit: cloud-kit\nname: demo\nmode: easy\nmetadata:\n  server_provisioning_mode: kombify-cloud\n",
		"monthly runtime lane":       "stackkit: cloud-kit\nname: demo\nmetadata:\n  runtime_lane: monthly-runtime\n",
		"non-local node provider":    "stackkit: cloud-kit\nname: demo\nnodes:\n  - name: main\n    provider: ionos\n",
	} {
		handoff := handoff
		t.Run(name, func(t *testing.T) {
			writeCanonicalTemplate(t, "cloud-kit")
			path := writeSpec(t, handoff)
			canonical, err := canonicalStackSpecFor(path, "cloud-kit", "demo")
			if err != nil {
				t.Fatalf("canonicalStackSpecFor: %v", err)
			}
			domain := mapFromInterface(mapFromInterface(readSpecDocument(t, canonical.Path)["network"])["domain"])
			if got := stringFromInterface(domain["base"]); got != addressModeKombifyMeDomain {
				t.Fatalf("network.domain.base = %q, want %q", got, addressModeKombifyMeDomain)
			}
		})
	}
}

// A self-hosted stack is not a managed runtime, so it still has to say where it
// lives instead of silently inheriting the platform address.
func TestCanonicalStackSpecForStillFailsClosedForASelfHostedHandoff(t *testing.T) {
	writeCanonicalTemplate(t, "cloud-kit")
	path := writeSpec(t, "stackkit: cloud-kit\nname: demo\nnodes:\n  - name: main\n    provider: local\n")

	_, err := canonicalStackSpecFor(path, "cloud-kit", "demo")
	if err == nil || !strings.Contains(err.Error(), "routing domain") {
		t.Fatalf("error = %v, want a fail-closed routing domain rejection", err)
	}
}

// The destination belongs to the resolved plan. Generating into Techstack's own
// tofu/ directory was refused outright:
//
//	Error: architecture v2 --output must resolve to governed ResolvedPlan
//	outputRoot deploy
func TestCanonicalStackSpecForReportsTheGovernedOutputRoot(t *testing.T) {
	writeCanonicalTemplate(t, "cloud-kit")
	path := writeSpec(t, "stackkit: cloud-kit\nname: demo\nnetwork:\n  domain: demo.example.test\n")

	canonical, err := canonicalStackSpecFor(path, "cloud-kit", "demo")
	if err != nil {
		t.Fatalf("canonicalStackSpecFor: %v", err)
	}
	if canonical.OutputRoot != "deploy" {
		t.Fatalf("OutputRoot = %q, want deploy", canonical.OutputRoot)
	}
}

// An already canonical document still has to report where it may generate.
func TestCanonicalStackSpecForReportsTheOutputRootOfAnExistingDocument(t *testing.T) {
	writeCanonicalTemplate(t, "cloud-kit")
	path := writeSpec(t, `{"apiVersion":"stackkit/v2alpha1","kind":"StackSpec","kit":{"slug":"cloud-kit"},"metadata":{"name":"x"},"generation":{"outputRoot":"deploy"}}`)

	canonical, err := canonicalStackSpecFor(path, "cloud-kit", "demo")
	if err != nil {
		t.Fatalf("canonicalStackSpecFor: %v", err)
	}
	if canonical.Derived || canonical.OutputRoot != "deploy" {
		t.Fatalf("canonical = %+v, want an undisturbed document generating into deploy", canonical)
	}
}

// A traversal in the governed root would write outside the work directory.
func TestCanonicalStackSpecForRejectsAnUnsafeOutputRoot(t *testing.T) {
	for name, root := range map[string]string{
		"parent":   "../escape",
		"absolute": "/tmp/escape",
		"nested":   "deploy/nested",
		"dot":      ".",
	} {
		root := root
		t.Run(name, func(t *testing.T) {
			path := writeSpec(t, `{"apiVersion":"stackkit/v2alpha1","kind":"StackSpec","kit":{"slug":"cloud-kit"},"metadata":{"name":"x"},"generation":{"outputRoot":`+strconv.Quote(root)+`}}`)
			if _, err := canonicalStackSpecFor(path, "cloud-kit", "demo"); err == nil {
				t.Fatalf("canonicalStackSpecFor accepted outputRoot %q", root)
			}
		})
	}
}
