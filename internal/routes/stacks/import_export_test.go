package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"

	"github.com/kombifyio/techstack/pkg/config"
)

// Regression: native wizard deployments live in the control-plane store, so
// the export boundary must return their validated v2 projection instead of
// looking only in the retired PocketBase projection and answering 404.
func TestExportStackReadsNativeV2ControlPlaneAuthority(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(context.Background(), controlplane.CreateStackRequest{
		ID: "deployment-1", TenantID: "auth0|user-1", OwnerSubjectID: "auth0|user-1", Name: "My Homelab",
		Config: map[string]any{
			"user_config": map[string]any{"stackkit": "cloud-kit", "services": []any{}},
			stackConfigKeySpecV2: map[string]any{
				"apiVersion": "stackkit/v2alpha1", "kind": "StackSpec",
				"metadata":  map[string]any{"name": "my-homelab", "stackId": "my-homelab"},
				"workloads": map[string]any{"photos": map[string]any{"alternative": "immich"}},
			},
		},
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}

	event, recorder := stackStoreRequestEvent("auth0|user-1", "")
	event.Request.Method = http.MethodGet
	event.Request.SetPathValue("id", "deployment-1")
	h := importExportRouteHandlers{create: crudRouteHandlers{stackStore: store}}
	if err := h.exportStack(event); err != nil {
		t.Fatalf("exportStack: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}

	var response struct {
		Data struct {
			KitDeploymentID string         `json:"kit_deployment_id"`
			StackSpec       map[string]any `json:"stack_spec"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	workloads, _ := response.Data.StackSpec["workloads"].(map[string]any)
	if response.Data.KitDeploymentID != "deployment-1" || response.Data.StackSpec["apiVersion"] != "stackkit/v2alpha1" || workloads["photos"] == nil {
		t.Fatalf("exported stack_spec = %#v, want persisted v2 workloads", response.Data.StackSpec)
	}
}

func TestExportStackFailsClosedWithoutCanonicalStore(t *testing.T) {
	event, recorder := stackStoreRequestEvent("owner-1", "owner-1")
	_ = (importExportRouteHandlers{}).exportStack(event)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
}

func TestValidateImportStopsAfterEmptyBodyRejection(t *testing.T) {
	event, recorder := stackStoreRequestEvent("owner-1", "")
	event.Request.URL.Path = "/api/v1/stacks/import/validate"
	err := (importExportRouteHandlers{}).validateImport(event)
	if !errors.Is(err, httpx.ErrResponseWritten) || recorder.Code != http.StatusBadRequest {
		t.Fatalf("err=%v status=%d body=%s, want terminal 400", err, recorder.Code, recorder.Body.String())
	}
	decoder := json.NewDecoder(recorder.Body)
	var envelope map[string]any
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("decode rejection: %v", err)
	}
	if err := decoder.Decode(&map[string]any{}); !errors.Is(err, io.EOF) {
		t.Fatalf("second response appended after rejection: %v", err)
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
