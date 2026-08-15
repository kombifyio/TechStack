package openapi

import (
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPublicSpecExcludesInternalVMLeaseRoutes(t *testing.T) {
	if strings.Contains(string(Spec), "/api/v1/internal/vm-leases") {
		t.Fatal("public OpenAPI spec must not expose internal VM lease routes")
	}
}

func TestPublicSpecExcludesServicecallAuthScheme(t *testing.T) {
	raw := string(Spec)
	if strings.Contains(raw, "servicecallAuth") || strings.Contains(raw, "X-Kombify-Service-Auth") {
		t.Fatal("public OpenAPI spec must not expose internal servicecall auth")
	}
}

func TestInternalRuntimeLeaseSpecPublishesOnlyGovernedCloudSurfaces(t *testing.T) {
	var document map[string]any
	if err := yaml.Unmarshal(InternalSpec, &document); err != nil {
		t.Fatalf("internal OpenAPI YAML is invalid: %v", err)
	}
	paths, ok := document["paths"].(map[string]any)
	if !ok {
		t.Fatalf("paths = %#v", document["paths"])
	}
	for path, method := range map[string]string{
		"/api/v1/internal/vm-leases/{lease_id}": "get",
	} {
		item, ok := paths[path].(map[string]any)
		if !ok || item[method] == nil {
			t.Fatalf("internal path %q does not publish %s: %#v", path, method, paths[path])
		}
	}
	if _, published := paths["/api/v1/internal/vm-leases"]; published {
		t.Fatal("internal VM-lease create must remain unpublished until native atomic admission exists")
	}
	for _, forbidden := range []string{"/validate", "/desired-spec", "/executor-status", "/executor-commands"} {
		for path := range paths {
			if strings.Contains(path, forbidden) {
				t.Fatalf("Cloud internal contract must not publish Simulate-only path %q", path)
			}
		}
	}
	raw := string(InternalSpec)
	for _, required := range []string{"X-Kombify-Service-Auth", "Idempotency-Key", "on_behalf_of"} {
		if !strings.Contains(raw, required) {
			t.Fatalf("internal spec is missing %q", required)
		}
	}
}

func TestRoutingContractsPublishExactTargetsAndPrivateCloudDelegate(t *testing.T) {
	var publicDocument map[string]any
	if err := yaml.Unmarshal(Spec, &publicDocument); err != nil {
		t.Fatalf("public OpenAPI YAML is invalid: %v", err)
	}
	publicPaths, _ := publicDocument["paths"].(map[string]any)
	routing, ok := publicPaths["/api/v1/stacks/{id}/routing"].(map[string]any)
	if !ok || routing["get"] == nil || routing["put"] == nil {
		t.Fatalf("public routing GET/PUT missing: %#v", routing)
	}
	targets, ok := publicPaths["/api/v1/stacks/{id}/routing/targets"].(map[string]any)
	if !ok || targets["get"] == nil {
		t.Fatalf("read-only routing target discovery missing: %#v", targets)
	}
	publicRaw := string(Spec)
	for _, required := range []string{"server_id", "lease_id", "Idempotency-Key", "If-Match", "idempotent_replay", "rollout_job_id", "listStackRoutingTargets", "StackRoutingViewEnvelope", "completed", "failed"} {
		if !strings.Contains(publicRaw, required) {
			t.Fatalf("public routing contract is missing %q", required)
		}
	}

	var internalDocument map[string]any
	if err := yaml.Unmarshal(InternalSpec, &internalDocument); err != nil {
		t.Fatalf("internal OpenAPI YAML is invalid: %v", err)
	}
	internalPaths, _ := internalDocument["paths"].(map[string]any)
	delegate, ok := internalPaths["/api/internal/stacks/domain-attach"].(map[string]any)
	if !ok || delegate["post"] == nil {
		t.Fatalf("internal domain-routing delegate missing: %#v", delegate)
	}
	ingress, ok := internalPaths["/api/internal/stacks/{stack_id}/ingress"].(map[string]any)
	if !ok || ingress["get"] == nil {
		t.Fatalf("exact internal ingress route missing: %#v", ingress)
	}
	internalRaw := string(InternalSpec)
	for _, required := range []string{"cf_zone_id", "getInternalStackIngress", "ingress_ipv4_pending", "server_id", "lease_id"} {
		if !strings.Contains(internalRaw, required) {
			t.Fatalf("internal routing contract is missing %q", required)
		}
	}
}

func TestAddManagedRuntimeServerPublishesCrashSafeIdempotencyContract(t *testing.T) {
	var document map[string]any
	if err := yaml.Unmarshal(Spec, &document); err != nil {
		t.Fatalf("OpenAPI YAML is invalid: %v", err)
	}
	paths, _ := document["paths"].(map[string]any)
	path, ok := paths["/api/v1/stacks/{id}/managed-runtimes"].(map[string]any)
	if !ok {
		t.Fatalf("Add-Server path missing: %#v", paths["/api/v1/stacks/{id}/managed-runtimes"])
	}
	post, ok := path["post"].(map[string]any)
	if !ok || post["operationId"] != "addManagedRuntimeServer" {
		t.Fatalf("Add-Server POST contract missing: %#v", path)
	}
	parameters, _ := post["parameters"].([]any)
	foundRequiredKey := false
	for _, candidate := range parameters {
		parameter, _ := candidate.(map[string]any)
		if parameter["name"] == "Idempotency-Key" && parameter["in"] == "header" && parameter["required"] == true {
			foundRequiredKey = true
		}
	}
	if !foundRequiredKey {
		t.Fatalf("Add-Server must require the standard Idempotency-Key: %#v", parameters)
	}
	responses, _ := post["responses"].(map[string]any)
	for _, status := range []string{"202", "400", "401", "403", "404", "409", "422", "503"} {
		if responses[status] == nil {
			t.Fatalf("Add-Server response %s missing: %#v", status, responses)
		}
	}
	components, _ := document["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	for _, schema := range []string{"AddManagedRuntimeServerRequest", "AddManagedRuntimeServer", "AddManagedRuntimeServerEnvelope"} {
		if schemas[schema] == nil {
			t.Fatalf("Add-Server schema %q missing", schema)
		}
	}
	requestSchema, _ := schemas["AddManagedRuntimeServerRequest"].(map[string]any)
	properties, _ := requestSchema["properties"].(map[string]any)
	nodeRole, _ := properties["node_role"].(map[string]any)
	roles, _ := nodeRole["enum"].([]any)
	if fmt.Sprint(roles) != "[foundation worker storage]" {
		t.Fatalf("Add-Server node_role enum = %#v", roles)
	}
	services, _ := properties["services"].(map[string]any)
	serviceItems, _ := services["items"].(map[string]any)
	if services["maxItems"] != 128 || serviceItems["minLength"] != 1 || serviceItems["maxLength"] != 128 ||
		serviceItems["pattern"] != "^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$" {
		t.Fatalf("Add-Server service constraints = %#v", services)
	}
	raw := string(Spec)
	for _, required := range []string{"resource_generation_id", "operation_id", "lease_pending", "idempotent_replay", "raw key is never persisted"} {
		if !strings.Contains(raw, required) {
			t.Fatalf("Add-Server contract is missing %q", required)
		}
	}
}

func TestManagedRuntimeAuthorityPreflightPublishesReadOnlyGatewayBudgetContract(t *testing.T) {
	var document map[string]any
	if err := yaml.Unmarshal(Spec, &document); err != nil {
		t.Fatalf("OpenAPI YAML is invalid: %v", err)
	}
	paths, _ := document["paths"].(map[string]any)
	path, ok := paths["/api/v1/managed-runtimes/authority"].(map[string]any)
	if !ok {
		t.Fatalf("Managed runtime authority path missing: %#v", paths["/api/v1/managed-runtimes/authority"])
	}
	get, ok := path["get"].(map[string]any)
	if !ok || get["operationId"] != "preflightManagedRuntimeAuthority" {
		t.Fatalf("Managed runtime authority GET contract missing: %#v", path)
	}
	parameters, _ := get["parameters"].([]any)
	foundProviderID := false
	for _, candidate := range parameters {
		parameter, _ := candidate.(map[string]any)
		if parameter["name"] == "provider_id" && parameter["in"] == "query" && parameter["required"] == true {
			foundProviderID = true
		}
	}
	if !foundProviderID {
		t.Fatalf("Managed runtime authority preflight must require provider_id: %#v", parameters)
	}
	responses, _ := get["responses"].(map[string]any)
	for _, status := range []string{"200", "400", "401", "403", "503"} {
		if responses[status] == nil {
			t.Fatalf("Managed runtime authority preflight response %s missing: %#v", status, responses)
		}
	}
	components, _ := document["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	for _, schema := range []string{"ManagedRuntimeAuthorityPreflight", "ManagedRuntimeAuthorityPreflightEnvelope"} {
		if schemas[schema] == nil {
			t.Fatalf("Managed runtime authority preflight schema %q missing", schema)
		}
	}
}

func TestMonthlyRuntimeCleanupReadbackPublishesRedactedReadOnlyContract(t *testing.T) {
	var document map[string]any
	if err := yaml.Unmarshal(Spec, &document); err != nil {
		t.Fatalf("OpenAPI YAML is invalid: %v", err)
	}
	paths, _ := document["paths"].(map[string]any)
	path, ok := paths["/api/v1/monthly-runtimes/{lease_id}/cleanup-readback"].(map[string]any)
	if !ok {
		t.Fatalf("Cleanup readback path missing: %#v", paths["/api/v1/monthly-runtimes/{lease_id}/cleanup-readback"])
	}
	get, ok := path["get"].(map[string]any)
	if !ok || get["operationId"] != "getMonthlyRuntimeCleanupReadback" {
		t.Fatalf("Cleanup readback GET contract missing: %#v", path)
	}
	responses, _ := get["responses"].(map[string]any)
	for _, status := range []string{"200", "401", "403", "404", "409", "503"} {
		if responses[status] == nil {
			t.Fatalf("Cleanup readback response %s missing: %#v", status, responses)
		}
	}
	components, _ := document["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	cleanup, ok := schemas["MonthlyRuntimeCleanupReadback"].(map[string]any)
	if !ok || cleanup["additionalProperties"] != false {
		t.Fatalf("Cleanup readback schema must be closed: %#v", cleanup)
	}
	properties, _ := cleanup["properties"].(map[string]any)
	for _, required := range []string{"lease_id", "lease", "server", "provider_operation"} {
		if properties[required] == nil {
			t.Fatalf("Cleanup readback property %q missing: %#v", required, properties)
		}
	}
	for _, forbidden := range []string{"operation_id", "native_ref", "credential", "command_json", "evidence_json"} {
		if properties[forbidden] != nil {
			t.Fatalf("Cleanup readback contract leaks %q", forbidden)
		}
	}
	raw := string(Spec)
	for _, required := range []string{"desired_terminal", "observed_terminal", "capacity_released", "provider-evidence://"} {
		if !strings.Contains(raw, required) {
			t.Fatalf("Cleanup readback contract is missing %q", required)
		}
	}
}

func TestClientPairingContractIsPublishedWithoutReplacingWorkerPairing(t *testing.T) {
	var document map[string]any
	if err := yaml.Unmarshal(Spec, &document); err != nil {
		t.Fatalf("OpenAPI YAML is invalid: %v", err)
	}
	paths, ok := document["paths"].(map[string]any)
	if !ok {
		t.Fatalf("paths = %#v", document["paths"])
	}
	for _, path := range []string{
		"/api/v1/client-pairing/issue",
		"/api/v1/client-pairing/redeem",
		"/api/v1/trust/pairing-tokens",
	} {
		item, ok := paths[path].(map[string]any)
		if !ok || item["post"] == nil {
			t.Fatalf("OpenAPI path %q does not publish POST: %#v", path, paths[path])
		}
	}

	components, ok := document["components"].(map[string]any)
	if !ok {
		t.Fatalf("components = %#v", document["components"])
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		t.Fatalf("schemas = %#v", components["schemas"])
	}
	for _, schema := range []string{"ClientPairingEnvelope", "ClientPairingRedeemRequest", "ClientPairingRedeemResponse", "ClientErrorEnvelope"} {
		if schemas[schema] == nil {
			t.Fatalf("OpenAPI schema %q is missing", schema)
		}
	}
}
