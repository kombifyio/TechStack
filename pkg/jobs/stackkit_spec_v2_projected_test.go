package jobs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

func TestStackKitSpecBytesForPayloadWritesArchitectureV2Handoff(t *testing.T) {
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
	if handoff["apiVersion"] != canonicalStackSpecAPIVersion {
		t.Fatalf("handoff apiVersion = %v, want Architecture v2", handoff["apiVersion"])
	}
	if _, exists := payload[payloadKeyStackSpecV2]; !exists {
		t.Fatal("input payload was mutated")
	}
}

func TestPersistProjectedStackSpecTracksProjectionLifecycle(t *testing.T) {
	persister := newProjectedTestPersister(t)

	path, err := persistProjectedStackSpec(persister, map[string]interface{}{"stackkit": "basement-kit"})
	if err != nil || path != "" {
		t.Fatalf("persist without projection = (%q, %v), want no sibling document", path, err)
	}

	path, err = persistProjectedStackSpec(persister, map[string]interface{}{
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

	stale := path
	path, err = persistProjectedStackSpec(persister, map[string]interface{}{"stackkit": "basement-kit"})
	if err != nil || path != "" {
		t.Fatalf("retire projection = (%q, %v), want empty path", path, err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale projected doc must be removed, stat err = %v", err)
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

// Regression: a Wizard join updated config_json but a later bare deploy kept
// executing the pre-join process-local projection written by provision.
func TestDeployPrepareRematerializesJoinedProjectionFromSnapshot(t *testing.T) {
	baseDir := t.TempDir()
	stackID := "stack-joined"
	persistDeployFixture(t, baseDir, stackID)
	persister, err := unifier.NewSpecPersisterWithPath(filepath.Join(baseDir, stackID))
	if err != nil {
		t.Fatalf("create persister: %v", err)
	}
	stale := projectedTestSpec()
	stale["nodes"] = stale["nodes"].([]interface{})[:1]
	if _, err := persistProjectedStackSpec(persister, map[string]interface{}{payloadKeyStackSpecV2: stale}); err != nil {
		t.Fatalf("seed pre-join projection: %v", err)
	}

	job := &Job{ID: "deploy-joined", Type: JobTypeDeploy, TargetID: stackID, Payload: map[string]interface{}{
		payloadKeyStackSpecV2: projectedTestSpec(),
	}}
	queue := &Queue{jobs: map[string]*Job{job.ID: job}}
	if _, err := deployPrepare(context.Background(), &ProvisionConfig{SpecBaseDir: baseDir}, job, queue); err != nil {
		t.Fatalf("deployPrepare: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(baseDir, stackID, projectedStackSpecFilename)) // #nosec G304 -- test temp dir
	if err != nil {
		t.Fatalf("read re-materialized projection: %v", err)
	}
	var projected map[string]interface{}
	if err := json.Unmarshal(data, &projected); err != nil {
		t.Fatalf("parse re-materialized projection: %v", err)
	}
	nodes, _ := projected["nodes"].([]interface{})
	if len(nodes) != 2 {
		t.Fatalf("bare deploy kept the stale pre-join projection: %#v", nodes)
	}
}

func TestCanonicalStackSpecForPreservesProjectedDeltasAndResolvesDomain(t *testing.T) {
	tests := []struct {
		name          string
		handoffDomain string
		wantDomain    string
	}{
		{"handoff domain refreshes projection", "wizard.example", "wizard.example"},
		{"missing handoff domain keeps projection", "", "example.homelab"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			stackSpecPath := filepath.Join(dir, "stack-spec.yaml")
			handoff := map[string]interface{}{"stackkit": "basement-kit", "name": "my-homelab"}
			if tt.handoffDomain != "" {
				handoff["domain"] = tt.handoffDomain
			}
			handoffBytes, err := yaml.Marshal(handoff)
			if err != nil {
				t.Fatalf("marshal handoff: %v", err)
			}
			if err := os.WriteFile(stackSpecPath, handoffBytes, 0o600); err != nil {
				t.Fatalf("write handoff: %v", err)
			}
			projectedBytes, err := json.Marshal(projectedTestSpec())
			if err != nil {
				t.Fatalf("marshal projected: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dir, projectedStackSpecFilename), projectedBytes, 0o600); err != nil {
				t.Fatalf("write projected: %v", err)
			}
			// The empty template env proves the persisted projection short-circuits template authoring.
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
			metadata, _ := doc["metadata"].(map[string]interface{})
			network, _ := doc["network"].(map[string]interface{})
			domain, _ := network["domain"].(map[string]interface{})
			if len(nodes) != 2 || metadata["fleetRef"] != "hl-1" || domain["base"] != tt.wantDomain {
				t.Fatalf("canonical projection lost deltas or domain precedence: %#v", doc)
			}
		})
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
