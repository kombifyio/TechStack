package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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
	if !canonical.Derived || canonical.OutputRoot != "deploy" {
		t.Fatalf("legacy v1 handoff canonical result = %+v, want derived document governed by deploy", canonical)
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
	if canonical.Derived || canonical.OutputRoot != "deploy" {
		t.Fatalf("existing canonical result = %+v, want undisturbed document governed by deploy", canonical)
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
	if err == nil {
		t.Fatal("canonicalStackSpecFor accepted a template for another kit")
	}
}

// A malformed native handoff must not be replaced by a template that loses its intent.
func TestCanonicalStackSpecForRejectsMalformedNativeHandoff(t *testing.T) {
	writeCanonicalTemplate(t, "cloud-kit")
	path := writeSpec(t, `{"apiVersion":"stackkit/v2alpha2","kind":"StackSpec","kit":{"slug":"cloud-kit"},"metadata":{"name":"native"},"network":{"domain":{"base":"native.example.test"}},"ssh":{"user":"root"}}`)
	if _, err := canonicalStackSpecFor(path, "cloud-kit", "native"); err == nil {
		t.Fatal("malformed native intent was replaced by a template")
	}
}

func TestCanonicalStackSpecForRequiresTheTemplateRoot(t *testing.T) {
	t.Setenv(stackKitSpecTemplateEnv, "")
	path := writeSpec(t, "stackkit: cloud-kit\nname: demo\nnetwork:\n  domain: demo.example.test\n")

	_, err := canonicalStackSpecFor(path, "cloud-kit", "demo")
	if err == nil {
		t.Fatal("canonicalStackSpecFor accepted a handoff without the template authority")
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

// Domainless managed runtimes inherit the platform address. An unclassified or
// self-hosted handoff must fail closed instead of guessing routes for the wrong host.
func TestCanonicalStackSpecForAppliesMissingDomainAuthority(t *testing.T) {
	tests := []struct {
		name      string
		handoff   string
		wantError bool
	}{
		{"kombify-cloud provisioning", "stackkit: cloud-kit\nname: demo\nmode: easy\nmetadata:\n  server_provisioning_mode: kombify-cloud\n", false},
		{"monthly runtime lane", "stackkit: cloud-kit\nname: demo\nmetadata:\n  runtime_lane: monthly-runtime\n", false},
		{"non-local node provider", "stackkit: cloud-kit\nname: demo\nnodes:\n  - name: main\n    provider: ionos\n", false},
		{"unclassified runtime", "stackkit: cloud-kit\nname: demo\n", true},
		{"self-hosted node", "stackkit: cloud-kit\nname: demo\nnodes:\n  - name: main\n    provider: local\n", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeCanonicalTemplate(t, "cloud-kit")
			path := writeSpec(t, tt.handoff)
			canonical, err := canonicalStackSpecFor(path, "cloud-kit", "demo")
			if tt.wantError {
				if err == nil {
					t.Fatal("canonicalStackSpecFor accepted a domainless unmanaged handoff")
				}
				return
			}
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
