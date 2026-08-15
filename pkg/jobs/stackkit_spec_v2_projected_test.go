package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/unifier"
	"gopkg.in/yaml.v3"
)

func projectedTestSpec() map[string]interface{} {
	return map[string]interface{}{
		"apiVersion": canonicalStackSpecAPIVersion,
		"kind":       "StackSpec",
		"kit":        map[string]interface{}{"slug": "basement-kit"},
		"metadata":   map[string]interface{}{"name": "my-homelab", "fleetRef": "hl-1"},
		"generation": map[string]interface{}{"outputRoot": "deploy"},
		"network":    map[string]interface{}{"domain": map[string]interface{}{"base": "example.homelab"}},
		"nodes": []interface{}{
			map[string]interface{}{"id": "main", "roles": []interface{}{"controller", "worker"}, "siteRef": "home"},
			map[string]interface{}{"id": "worker-1", "roles": []interface{}{"worker"}, "siteRef": "home"},
		},
	}
}

func newProjectedTestPersister(t *testing.T) *unifier.SpecPersister {
	t.Helper()
	persister, err := unifier.NewSpecPersisterWithPath(t.TempDir())
	if err != nil {
		t.Fatalf("NewSpecPersisterWithPath: %v", err)
	}
	return persister
}

func TestStackKitSpecBytesForPayloadStripsProjectedSpec(t *testing.T) {
	payload := map[string]interface{}{
		"stackkit":            "basement-kit",
		"name":                "my-homelab",
		payloadKeyStackSpecV2: projectedTestSpec(),
	}
	bytes, err := stackKitSpecBytesForPayload(payload)
	if err != nil {
		t.Fatalf("stackKitSpecBytesForPayload: %v", err)
	}
	var handoff map[string]interface{}
	if err := yaml.Unmarshal(bytes, &handoff); err != nil {
		t.Fatalf("parse handoff: %v", err)
	}
	if _, exists := handoff[payloadKeyStackSpecV2]; exists {
		t.Fatal("projected spec leaked into the v1 handoff; the v1 decoder refuses v2-only top-level fields")
	}
	if handoff["stackkit"] != "basement-kit" {
		t.Fatalf("handoff lost its stackkit: %#v", handoff)
	}
	// The input payload itself must stay untouched (the job result still
	// carries the projection).
	if _, exists := payload[payloadKeyStackSpecV2]; !exists {
		t.Fatal("input payload was mutated")
	}
}

func TestPersistProjectedStackSpecWritesSiblingDocument(t *testing.T) {
	persister := newProjectedTestPersister(t)

	path, err := persistProjectedStackSpec(persister, map[string]interface{}{
		"stackkit":            "basement-kit",
		payloadKeyStackSpecV2: projectedTestSpec(),
	})
	if err != nil {
		t.Fatalf("persistProjectedStackSpec: %v", err)
	}
	if filepath.Base(path) != projectedStackSpecFilename {
		t.Fatalf("unexpected projected path: %s", path)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- test temp dir
	if err != nil {
		t.Fatalf("read projected doc: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse projected doc: %v", err)
	}
	if doc["apiVersion"] != canonicalStackSpecAPIVersion {
		t.Fatalf("projected doc apiVersion = %v", doc["apiVersion"])
	}
}

func TestPersistProjectedStackSpecIsNoopWithoutProjection(t *testing.T) {
	persister := newProjectedTestPersister(t)
	path, err := persistProjectedStackSpec(persister, map[string]interface{}{"stackkit": "basement-kit"})
	if err != nil {
		t.Fatalf("persistProjectedStackSpec: %v", err)
	}
	if path != "" {
		t.Fatalf("expected no projected doc, got %s", path)
	}
}

func TestPersistProjectedStackSpecRejectsWrongAPIVersion(t *testing.T) {
	persister := newProjectedTestPersister(t)
	if _, err := persistProjectedStackSpec(persister, map[string]interface{}{
		payloadKeyStackSpecV2: map[string]interface{}{"apiVersion": "stackkit/v1"},
	}); err == nil {
		t.Fatal("expected apiVersion rejection")
	}
}

func TestCanonicalStackSpecForPrefersProjectedDocumentOverTemplate(t *testing.T) {
	dir := t.TempDir()
	stackSpecPath := filepath.Join(dir, "stack-spec.yaml")
	// v1-shaped handoff (the wizard's synthesized wire shape).
	handoff := map[string]interface{}{
		"stackkit": "basement-kit",
		"name":     "my-homelab",
		"domain":   "wizard.example",
	}
	handoffBytes, err := yaml.Marshal(handoff)
	if err != nil {
		t.Fatalf("marshal handoff: %v", err)
	}
	if writeErr := os.WriteFile(stackSpecPath, handoffBytes, 0o600); writeErr != nil {
		t.Fatalf("write handoff: %v", writeErr)
	}
	projectedBytes, err := json.Marshal(projectedTestSpec())
	if err != nil {
		t.Fatalf("marshal projected: %v", err)
	}
	if writeErr := os.WriteFile(filepath.Join(dir, projectedStackSpecFilename), projectedBytes, 0o600); writeErr != nil {
		t.Fatalf("write projected: %v", writeErr)
	}
	// No template env: the template path would fail, proving the projected
	// document short-circuits it.
	t.Setenv(stackKitSpecTemplateEnv, "")

	canonical, err := canonicalStackSpecFor(stackSpecPath, "basement-kit", "my-homelab")
	if err != nil {
		t.Fatalf("canonicalStackSpecFor: %v", err)
	}
	if !canonical.Derived || canonical.OutputRoot != "deploy" {
		t.Fatalf("unexpected canonical result: %#v", canonical)
	}
	data, err := os.ReadFile(canonical.Path) // #nosec G304 -- test temp dir
	if err != nil {
		t.Fatalf("read canonical doc: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse canonical doc: %v", err)
	}
	nodes, _ := doc["nodes"].([]interface{})
	if len(nodes) != 2 {
		t.Fatalf("projection deltas lost: nodes = %d, want 2", len(nodes))
	}
	metadata, _ := doc["metadata"].(map[string]interface{})
	if metadata["fleetRef"] != "hl-1" {
		t.Fatalf("fleetRef lost: %#v", metadata)
	}
	// The routing domain is refreshed from the handoff chain.
	network, _ := doc["network"].(map[string]interface{})
	domain, _ := network["domain"].(map[string]interface{})
	if domain["base"] != "wizard.example" {
		t.Fatalf("domain not refreshed from handoff: %#v", domain)
	}
}

func TestCanonicalStackSpecForKeepsProjectedDomainWhenHandoffResolvesNone(t *testing.T) {
	dir := t.TempDir()
	stackSpecPath := filepath.Join(dir, "stack-spec.yaml")
	handoffBytes, err := yaml.Marshal(map[string]interface{}{
		"stackkit": "basement-kit",
		"name":     "my-homelab",
	})
	if err != nil {
		t.Fatalf("marshal handoff: %v", err)
	}
	if writeErr := os.WriteFile(stackSpecPath, handoffBytes, 0o600); writeErr != nil {
		t.Fatalf("write handoff: %v", writeErr)
	}
	projectedBytes, err := json.Marshal(projectedTestSpec())
	if err != nil {
		t.Fatalf("marshal projected: %v", err)
	}
	if writeErr := os.WriteFile(filepath.Join(dir, projectedStackSpecFilename), projectedBytes, 0o600); writeErr != nil {
		t.Fatalf("write projected: %v", writeErr)
	}
	t.Setenv(stackKitSpecTemplateEnv, "")

	canonical, err := canonicalStackSpecFor(stackSpecPath, "basement-kit", "my-homelab")
	if err != nil {
		t.Fatalf("canonicalStackSpecFor: %v", err)
	}
	data, err := os.ReadFile(canonical.Path) // #nosec G304 -- test temp dir
	if err != nil {
		t.Fatalf("read canonical doc: %v", err)
	}
	if !strings.Contains(string(data), "example.homelab") {
		t.Fatalf("projected domain must survive when the handoff resolves none: %s", data)
	}
}

func TestCanonicalStackSpecForRejectsCorruptProjectedDocument(t *testing.T) {
	dir := t.TempDir()
	stackSpecPath := filepath.Join(dir, "stack-spec.yaml")
	if err := os.WriteFile(stackSpecPath, []byte("stackkit: basement-kit\n"), 0o600); err != nil {
		t.Fatalf("write handoff: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, projectedStackSpecFilename), []byte(`{"apiVersion":"nope"}`), 0o600); err != nil {
		t.Fatalf("write projected: %v", err)
	}
	t.Setenv(stackKitSpecTemplateEnv, "")

	if _, err := canonicalStackSpecFor(stackSpecPath, "basement-kit", "my-homelab"); err == nil {
		t.Fatal("corrupt projected document must fail the rollout, not silently degrade")
	}
}

func TestPersistProjectedStackSpecRemovesStaleSiblingWithoutProjection(t *testing.T) {
	persister := newProjectedTestPersister(t)

	if _, err := persistProjectedStackSpec(persister, map[string]interface{}{
		"stackkit":            "basement-kit",
		payloadKeyStackSpecV2: projectedTestSpec(),
	}); err != nil {
		t.Fatalf("seed projected doc: %v", err)
	}
	stale := filepath.Join(filepath.Dir(persister.GetStackSpecPath()), projectedStackSpecFilename)
	if _, err := os.Stat(stale); err != nil {
		t.Fatalf("projected doc missing after seed: %v", err)
	}

	// A later provision WITHOUT a projection (explicit body spec) must retire
	// the stale sibling so it cannot override the operator's explicit spec.
	if _, err := persistProjectedStackSpec(persister, map[string]interface{}{"stackkit": "basement-kit"}); err != nil {
		t.Fatalf("persist without projection: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale projected doc must be removed, stat err = %v", err)
	}
}
