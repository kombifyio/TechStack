package stacks

import (
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"

	"github.com/kombifyio/techstack/pkg/config"
)

func TestBuildStackSpecExport_PreservesStackKitsShape(t *testing.T) {
	app := newOwnerSpecTestApp(t)
	defer app.Cleanup()

	collection, err := app.FindCollectionByNameOrId("stacks")
	if err != nil {
		t.Fatalf("find stacks collection: %v", err)
	}
	record := core.NewRecord(collection)
	record.Set("name", "Configured Stack")
	record.Set("user_config", map[string]any{
		"name":     "configured-stack",
		"stackkit": "basement-kit",
		"mode":     "simple",
		"runtime":  "docker",
		"nodes": []any{
			map[string]any{"name": "main", "role": "standalone", "ip": "192.168.1.100"},
		},
		"services": map[string]any{
			"homepage": map[string]any{"enabled": true},
		},
	})
	if saveErr := app.Save(record); saveErr != nil {
		t.Fatalf("save stack: %v", saveErr)
	}
	saved, err := app.FindRecordById("stacks", record.Id)
	if err != nil {
		t.Fatalf("find saved stack: %v", err)
	}

	got := buildStackSpecExport(saved)
	if got["stackkit"] != "basement-kit" {
		t.Fatalf("expected stackkit to be preserved, got %v", got)
	}
	if got["version"] != nil {
		t.Fatalf("did not expect legacy version injection for stack-spec export, got %v", got["version"])
	}
	if got["metadata"] != nil {
		t.Fatalf("did not expect TechStack metadata injection for stack-spec export, got %v", got["metadata"])
	}
	if services, ok := got["services"].(map[string]any); !ok || services["homepage"] == nil {
		t.Fatalf("expected StackKits services map to be preserved, got %v", got["services"])
	}
}

func TestMarshalToYAML_UsesStackSpecHeader(t *testing.T) {
	got, err := marshalToYAML(map[string]any{"name": "configured-stack", "stackkit": "basement-kit"})
	if err != nil {
		t.Fatalf("marshalToYAML() error = %v", err)
	}
	if !strings.Contains(string(got), "Stack Spec Export") {
		t.Fatalf("expected Stack Spec export header, got %s", string(got))
	}
}

func TestImportStackSpecRejectsPlaintextRecoveryPassphrase(t *testing.T) {
	req, msg := importCreateStackRequestFromBody([]byte(`
name: imported-stack
metadata:
  owner_bootstrap_mode: auto
  owner_source: local
identity:
  recovery:
    passphrase: correct horse battery staple
`), "application/yaml")
	if msg != "" {
		t.Fatalf("importCreateStackRequestFromBody() message = %q, want empty parse", msg)
	}

	_, msg = normalizeCreateStackRequest(req)
	if !strings.Contains(msg, "Plaintext recovery passphrases are not accepted") {
		t.Fatalf("normalizeCreateStackRequest() message = %q, want plaintext recovery rejection", msg)
	}
}

func TestImportStackSpecRedactsRecoveryHashBeforeStorage(t *testing.T) {
	req, msg := importCreateStackRequestFromBody([]byte(`{
		"name": "imported-stack",
		"metadata": {
			"owner_bootstrap_mode": "custom",
			"owner_source": "local"
		},
		"identity": {
			"owner": {"email": "owner@example.com"},
			"recovery": {"passphrase_hash": "`+testRecoveryPassphraseHash+`"}
		}
	}`), "application/json")
	if msg != "" {
		t.Fatalf("importCreateStackRequestFromBody() message = %q, want empty parse", msg)
	}
	normalized, msg := normalizeCreateStackRequest(req)
	if msg != "" {
		t.Fatalf("normalizeCreateStackRequest() message = %q, want empty", msg)
	}

	redacted := redactUserConfigForStorage(normalized.UserConfig)
	if strings.Contains(strings.TrimSpace(stringFromAny(redacted["identity"])), "$argon2id$") {
		t.Fatalf("did not expect recovery hash in redacted identity: %v", redacted["identity"])
	}
	identity := redacted["identity"].(map[string]interface{})
	recovery := identity["recovery"].(map[string]interface{})
	if recovery["passphrase_hash"] != nil {
		t.Fatalf("expected imported recovery hash to be redacted before storage, got %v", recovery)
	}
}

func TestImportStackSpecAppliesDeploymentLanePolicy(t *testing.T) {
	req, msg := importCreateStackRequestFromBody([]byte(`
name: managed-import
metadata:
  server_provisioning_mode: kombify-cloud
  server_mode: monthly-runtime
  runtime_lane: monthly-runtime
  billing_mode: subscription
  provider_id: centron
`), "application/yaml")
	if msg != "" {
		t.Fatalf("importCreateStackRequestFromBody() message = %q, want empty parse", msg)
	}
	normalized, msg := normalizeCreateStackRequest(req)
	if msg != "" {
		t.Fatalf("normalizeCreateStackRequest() message = %q, want empty", msg)
	}

	msg = validateDeploymentLane(normalized, config.ModeSelfHosted)
	if !strings.Contains(msg, "Managed monthly runtime can only be created from kombify Cloud mode") {
		t.Fatalf("validateDeploymentLane() message = %q, want managed runtime rejection", msg)
	}
}
