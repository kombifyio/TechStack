package openapi

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/contractgen/runtimeinventorygen"
	"gopkg.in/yaml.v3"
)

func TestRuntimeInventoryOpenAPIExactlyMatchesCanonicalSchema(t *testing.T) {
	schemaPath := filepath.Join("..", "..", "contracts", "runtimeinventory", "v1", "schema.json")
	contract, _, err := runtimeinventorygen.Load(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(Spec, &document); err != nil {
		t.Fatalf("OpenAPI YAML is invalid: %v", err)
	}
	components := inventoryContractMap(t, document, "components")
	schemas := inventoryContractMap(t, components, "schemas")
	expectedNames := make(map[string]bool, len(contract.Types))
	for _, typ := range contract.Types {
		openAPIName := "Inventory" + typ.Name
		expectedNames[openAPIName] = true
		published := inventoryContractMap(t, schemas, openAPIName)
		properties := inventoryContractMap(t, published, "properties")
		if len(properties) != len(typ.Fields) {
			t.Errorf("%s publishes %d properties, canonical schema has %d", openAPIName, len(properties), len(typ.Fields))
		}
		expectedRequired := map[string]bool{}
		for _, field := range typ.Fields {
			if !field.OmitEmpty {
				expectedRequired[field.JSON] = true
			}
			property, ok := properties[field.JSON].(map[string]any)
			if !ok {
				t.Errorf("%s is missing canonical JSON field %q", openAPIName, field.JSON)
				continue
			}
			assertRuntimeInventoryOpenAPIType(t, openAPIName+"."+field.JSON, field.GoType, field.OmitEmpty, property)
		}
		actualRequired := inventoryOptionalStringSet(t, published["required"])
		if !reflect.DeepEqual(actualRequired, expectedRequired) {
			t.Errorf("%s required fields = %v, canonical schema requires %v", openAPIName, actualRequired, expectedRequired)
		}
	}
	for name := range schemas {
		if strings.HasPrefix(name, "Inventory") && !expectedNames[name] {
			t.Errorf("OpenAPI publishes non-canonical Runtime Inventory schema %q", name)
		}
	}
}

func assertRuntimeInventoryOpenAPIType(t *testing.T, name, goType string, omitempty bool, property map[string]any) {
	t.Helper()
	base := goType
	pointer := strings.HasPrefix(base, "*")
	array := strings.HasPrefix(base, "[]")
	if pointer {
		base = strings.TrimPrefix(base, "*")
	} else if array {
		base = strings.TrimPrefix(base, "[]")
	}
	nullable := pointer && !omitempty
	shape := property
	if oneOf, exists := property["oneOf"]; exists {
		variants, ok := oneOf.([]any)
		if !ok || len(variants) != 2 {
			t.Errorf("%s oneOf = %#v, want canonical value plus null", name, oneOf)
			return
		}
		var value map[string]any
		seenNull := false
		for _, raw := range variants {
			variant, _ := raw.(map[string]any)
			if variant["type"] == "null" {
				seenNull = true
			} else {
				value = variant
			}
		}
		if !nullable || !seenNull || value == nil {
			t.Errorf("%s nullability does not match canonical Go field %s", name, goType)
			return
		}
		shape = value
	} else if nullable {
		t.Errorf("%s must allow null for required pointer field %s", name, goType)
	}
	if array {
		if shape["type"] != "array" {
			t.Errorf("%s type = %#v, want array", name, shape)
			return
		}
		items, _ := shape["items"].(map[string]any)
		assertRuntimeInventoryOpenAPIBase(t, name+"[]", base, items)
		return
	}
	assertRuntimeInventoryOpenAPIBase(t, name, base, shape)
}

func assertRuntimeInventoryOpenAPIBase(t *testing.T, name, base string, shape map[string]any) {
	t.Helper()
	switch base {
	case "string":
		if shape["type"] != "string" {
			t.Errorf("%s type = %#v, want string", name, shape)
		}
	case "int":
		if shape["type"] != "integer" || shape["format"] != nil {
			t.Errorf("%s type = %#v, want integer without format", name, shape)
		}
	case "int64":
		if shape["type"] != "integer" || shape["format"] != "int64" {
			t.Errorf("%s type = %#v, want integer/int64", name, shape)
		}
	case "bool":
		if shape["type"] != "boolean" {
			t.Errorf("%s type = %#v, want boolean", name, shape)
		}
	case "time.Time":
		if shape["type"] != "string" || shape["format"] != "date-time" {
			t.Errorf("%s type = %#v, want string/date-time", name, shape)
		}
	default:
		want := "#/components/schemas/Inventory" + base
		if shape["$ref"] != want {
			t.Errorf("%s reference = %#v, want %s", name, shape, want)
		}
	}
}

func inventoryOptionalStringSet(t *testing.T, raw any) map[string]bool {
	t.Helper()
	if raw == nil {
		return map[string]bool{}
	}
	values, ok := raw.([]any)
	if !ok {
		t.Fatalf("required = %#v, want array", raw)
	}
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[fmt.Sprint(value)] = true
	}
	return result
}

func TestInventoryContractPublishesFiveMCPMappedReadOperations(t *testing.T) {
	var document map[string]any
	if err := yaml.Unmarshal(Spec, &document); err != nil {
		t.Fatalf("OpenAPI YAML is invalid: %v", err)
	}
	paths, _ := document["paths"].(map[string]any)
	for path, expected := range map[string]struct {
		operationID string
		capability  string
		toolName    string
	}{
		"/api/v1/inventory/servers":                           {"listInventoryServers", "techstack.inventory.read", "list_servers"},
		"/api/v1/inventory/servers/{serverId}/health":         {"getInventoryServerHealth", "techstack.inventory.read", "server_health"},
		"/api/v1/inventory/services":                          {"listInventoryServices", "techstack.inventory.read", "list_services"},
		"/api/v1/inventory/servers/{serverId}/access-context": {"getInventoryServerAccessContext", "techstack.inventory.operate", "server_access_context"},
		"/api/v1/stacks/{id}/operations":                      {"getStackOperations", "techstack.inventory.read", "get_stack_operations"},
	} {
		pathItem, ok := paths[path].(map[string]any)
		if !ok {
			t.Fatalf("missing inventory path %q", path)
		}
		operation, ok := pathItem["get"].(map[string]any)
		if !ok || operation["operationId"] != expected.operationID {
			t.Fatalf("path %q operation = %#v", path, operation)
		}
		mcp, ok := operation["x-kombify-mcp"].(map[string]any)
		annotations, _ := mcp["annotations"].(map[string]any)
		if !ok || mcp["toolName"] != expected.toolName || mcp["requiredCapability"] != expected.capability || annotations["readOnlyHint"] != true || annotations["idempotentHint"] != true || annotations["destructiveHint"] != false || annotations["openWorldHint"] != false {
			t.Fatalf("path %q MCP annotations = %#v", path, mcp)
		}
	}
	components, _ := document["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	for _, name := range []string{"InventoryLifecycleProjection", "InventoryDesiredProjection", "InventoryLeaseProjection", "InventoryCleanupProjection"} {
		if schemas[name] == nil {
			t.Fatalf("missing inventory projection schema %q", name)
		}
	}
	for _, name := range []string{"StackReadiness", "StackOperationsResponse", "StackOperationsEnvelope"} {
		if schemas[name] == nil {
			t.Fatalf("missing stack operations schema %q", name)
		}
	}
	readiness, _ := schemas["StackReadiness"].(map[string]any)
	readinessProperties, _ := readiness["properties"].(map[string]any)
	for _, field := range []string{"status", "can_start", "required_servers", "approved_servers", "connected_servers", "pending_servers", "assigned_servers", "available_servers", "unassigned_servers", "message", "review_required"} {
		if readinessProperties[field] == nil {
			t.Fatalf("StackReadiness is missing %s", field)
		}
	}
	for _, name := range []string{"InventoryServerList", "InventoryServiceList"} {
		schema, _ := schemas[name].(map[string]any)
		properties, _ := schema["properties"].(map[string]any)
		for _, field := range []string{"collection_cursor", "next_cursor", "page_size"} {
			if properties[field] == nil {
				t.Fatalf("schema %s is missing %s", name, field)
			}
		}
	}
}

func TestRILServerSummaryPublishesFailClosedPresenceContract(t *testing.T) {
	var document map[string]any
	if err := yaml.Unmarshal(Spec, &document); err != nil {
		t.Fatalf("OpenAPI YAML is invalid: %v", err)
	}
	paths := inventoryContractMap(t, document, "paths")
	for path, target := range map[string]string{
		"/v1/ril/servers":         "#/paths/~1api~1v1~1inventory~1servers",
		"/v1/ril/servers/summary": "#/paths/~1api~1v1~1servers~1summary",
	} {
		item := inventoryContractMap(t, paths, path)
		if item["$ref"] != target {
			t.Errorf("%s reference = %#v, want %q", path, item["$ref"], target)
		}
	}
	summaryPath := inventoryContractMap(t, paths, "/api/v1/servers/summary")
	summary := inventoryContractMap(t, summaryPath, "get")
	if summary["operationId"] != "getRILServerSummary" {
		t.Fatalf("summary operationId = %#v", summary["operationId"])
	}
	description := strings.ToLower(fmt.Sprint(summary["description"]))
	for _, phrase := range []string{"must exactly match", "rejected before any store read", "no server, tenant, or subject identifiers"} {
		if !strings.Contains(description, phrase) {
			t.Errorf("summary contract missing %q: %q", phrase, description)
		}
	}
	components := inventoryContractMap(t, document, "components")
	schemas := inventoryContractMap(t, components, "schemas")
	schema := inventoryContractMap(t, schemas, "RILServerSummary")
	required := inventoryContractStringSet(t, schema, "required")
	for _, field := range []string{"count", "has_servers", "observed_at"} {
		if !required[field] {
			t.Errorf("summary schema does not require %q: %#v", field, schema["required"])
		}
	}
}

func TestWorkerInventoryIngestPublishesGuardSnapshotContract(t *testing.T) {
	var document map[string]any
	if err := yaml.Unmarshal(Spec, &document); err != nil {
		t.Fatalf("OpenAPI YAML is invalid: %v", err)
	}

	paths := inventoryContractMap(t, document, "paths")
	path := inventoryContractMap(t, paths, "/api/v1/workers/{id}/inventory")
	post := inventoryContractMap(t, path, "post")
	if post["operationId"] != "ingestWorkerInventory" {
		t.Fatalf("worker inventory operationId = %#v", post["operationId"])
	}
	security, ok := post["security"].([]any)
	if !ok || len(security) != 1 {
		t.Fatalf("worker inventory security = %#v, want runtime-agent bearer", post["security"])
	}
	runtimeAgentSecurity, ok := security[0].(map[string]any)
	if !ok || runtimeAgentSecurity["runtimeAgentAuth"] == nil {
		t.Fatalf("worker inventory security = %#v, want runtimeAgentAuth", security)
	}
	requestBody := inventoryContractMap(t, post, "requestBody")
	content := inventoryContractMap(t, requestBody, "content")
	mediaType := inventoryContractMap(t, content, "application/json")
	requestRef := inventoryContractMap(t, mediaType, "schema")
	if requestRef["$ref"] != "#/components/schemas/WorkerInventoryRequest" {
		t.Fatalf("worker inventory request schema = %#v", requestRef)
	}

	components := inventoryContractMap(t, document, "components")
	securitySchemes := inventoryContractMap(t, components, "securitySchemes")
	runtimeAgentAuth := inventoryContractMap(t, securitySchemes, "runtimeAgentAuth")
	if runtimeAgentAuth["type"] != "http" || runtimeAgentAuth["scheme"] != "bearer" {
		t.Errorf("runtimeAgentAuth = %#v", runtimeAgentAuth)
	}
	schemas := inventoryContractMap(t, components, "schemas")
	request := inventoryContractMap(t, schemas, "WorkerInventoryRequest")
	required := inventoryContractStringSet(t, request, "required")
	for _, field := range []string{"source_epoch", "source_sequence", "observed_at", "server_id", "runtime_agent_id"} {
		if !required[field] {
			t.Errorf("WorkerInventoryRequest does not require %q: %#v", field, request["required"])
		}
	}
	requestProperties := inventoryContractMap(t, request, "properties")
	for _, field := range []string{"lease_id", "manifest_observed", "stackkit", "stackkit_version", "stackkit_mode", "domain"} {
		if requestProperties[field] == nil {
			t.Errorf("WorkerInventoryRequest is missing %q", field)
		}
	}
	sourceEpoch := inventoryContractMap(t, requestProperties, "source_epoch")
	if sourceEpoch["type"] != "string" || sourceEpoch["minLength"] != 1 || sourceEpoch["maxLength"] != 128 {
		t.Errorf("source_epoch constraints = %#v", sourceEpoch)
	}
	sourceSequence := inventoryContractMap(t, requestProperties, "source_sequence")
	if sourceSequence["type"] != "integer" || sourceSequence["format"] != "int64" || sourceSequence["minimum"] != 1 {
		t.Errorf("source_sequence constraints = %#v", sourceSequence)
	}
	observedAt := inventoryContractMap(t, requestProperties, "observed_at")
	if observedAt["type"] != "string" || observedAt["format"] != "date-time" {
		t.Errorf("observed_at constraints = %#v", observedAt)
	}
	manifestObserved := inventoryContractMap(t, requestProperties, "manifest_observed")
	if manifestObserved["type"] != "boolean" {
		t.Errorf("manifest_observed constraints = %#v", manifestObserved)
	}

	host := inventoryContractMap(t, schemas, "WorkerInventoryHost")
	hostProperties := inventoryContractMap(t, host, "properties")
	if hostProperties["os_version"] == nil {
		t.Error("WorkerInventoryHost is missing os_version")
	}
	for field, classification := range map[string]string{
		"public_ip":  "public",
		"private_ip": "private",
		"local_ip":   "local",
	} {
		property := inventoryContractMap(t, hostProperties, field)
		if property["$ref"] != "#/components/schemas/WorkerInventoryIPAddress" ||
			property["x-kombify-ip-classification"] != classification {
			t.Errorf("WorkerInventoryHost.%s classification = %#v", field, property)
		}
	}
	ipAddress := inventoryContractMap(t, schemas, "WorkerInventoryIPAddress")
	if ipAddress["type"] != "string" {
		t.Errorf("WorkerInventoryIPAddress = %#v", ipAddress)
	}
	addressFormats, ok := ipAddress["anyOf"].([]any)
	if !ok || len(addressFormats) != 2 {
		t.Fatalf("WorkerInventoryIPAddress formats = %#v", ipAddress["anyOf"])
	}
	gotFormats := make(map[string]bool, len(addressFormats))
	for _, candidate := range addressFormats {
		format, _ := candidate.(map[string]any)
		gotFormats[fmt.Sprint(format["format"])] = true
	}
	if !gotFormats["ipv4"] || !gotFormats["ipv6"] {
		t.Errorf("WorkerInventoryIPAddress formats = %#v", gotFormats)
	}

	endpoint := inventoryContractMap(t, schemas, "WorkerInventoryEndpoint")
	endpointProperties := inventoryContractMap(t, endpoint, "properties")
	for _, field := range []string{"target_type", "route_id", "access_profile_ref"} {
		if endpointProperties[field] == nil {
			t.Errorf("WorkerInventoryEndpoint is missing %q", field)
		}
	}
	endpointURL := inventoryContractMap(t, endpointProperties, "url")
	if endpointURL["format"] != "uri" {
		t.Errorf("WorkerInventoryEndpoint.url constraints = %#v", endpointURL)
	}
	targetType := inventoryContractStringSet(t, inventoryContractMap(t, endpointProperties, "target_type"), "enum")
	if !targetType["direct"] || !targetType["tunnel"] {
		t.Errorf("WorkerInventoryEndpoint.target_type enum = %#v", targetType)
	}
	provenance := inventoryContractStringSet(t, inventoryContractMap(t, endpointProperties, "provenance"), "enum")
	if !provenance["stackkit-access-manifest"] {
		t.Errorf("WorkerInventoryEndpoint.provenance does not publish trusted StackKit observations: %#v", provenance)
	}
	endpointDescription := strings.ToLower(fmt.Sprint(endpoint["description"]))
	for _, phrase := range []string{"target_type=direct", "stackkit-access-manifest", "healthy", "reachable", "ok"} {
		if !strings.Contains(endpointDescription, phrase) {
			t.Errorf("WorkerInventoryEndpoint trust description is missing %q: %q", phrase, endpointDescription)
		}
	}

	service := inventoryContractMap(t, schemas, "WorkerInventoryService")
	serviceProperties := inventoryContractMap(t, service, "properties")
	for _, field := range []string{"instance", "stackkit_version", "actions"} {
		if serviceProperties[field] == nil {
			t.Errorf("WorkerInventoryService is missing %q", field)
		}
	}
	actions := inventoryContractMap(t, serviceProperties, "actions")
	actionItems := inventoryContractMap(t, actions, "items")
	allowedActions := inventoryContractStringSet(t, actionItems, "enum")
	for _, action := range []string{"start", "stop", "restart"} {
		if !allowedActions[action] {
			t.Errorf("WorkerInventoryService.actions does not allow %q: %#v", action, allowedActions)
		}
	}

	responses := inventoryContractMap(t, post, "responses")
	for _, status := range []string{"200", "202", "400", "401", "403", "409"} {
		if responses[status] == nil {
			t.Errorf("worker inventory response %s is missing: %#v", status, responses)
		}
	}
}

func TestWorkerCredentialEnrollmentPublishesIdempotencyAndGenerationContract(t *testing.T) {
	var document map[string]any
	if err := yaml.Unmarshal(Spec, &document); err != nil {
		t.Fatalf("OpenAPI YAML is invalid: %v", err)
	}

	paths := inventoryContractMap(t, document, "paths")
	connectPath := inventoryContractMap(t, paths, "/v1/ril/servers/connect")
	connect := inventoryContractMap(t, connectPath, "post")
	if connect["operationId"] != "connectRILServer" {
		t.Fatalf("connect operationId = %#v", connect["operationId"])
	}
	connectParameters, ok := connect["parameters"].([]any)
	if !ok || len(connectParameters) != 1 {
		t.Fatalf("connect parameters = %#v", connect["parameters"])
	}
	assertCredentialIdempotencyHeader(t, connectParameters[0])
	connectRequestBody := inventoryContractMap(t, connect, "requestBody")
	connectContent := inventoryContractMap(t, connectRequestBody, "content")
	connectMediaType := inventoryContractMap(t, connectContent, "application/json")
	connectRequestSchema := inventoryContractMap(t, connectMediaType, "schema")
	if connectRequestSchema["$ref"] != "#/components/schemas/WorkerConnectRequest" {
		t.Errorf("connect request schema = %#v", connectRequestSchema)
	}
	assertCredentialResponses(t, connect, []string{"200", "400", "401", "403", "409"})

	rotatePath := inventoryContractMap(t, paths, "/v1/ril/servers/{id}/credential/rotate")
	rotate := inventoryContractMap(t, rotatePath, "post")
	if rotate["operationId"] != "rotateRILServerCredential" {
		t.Fatalf("rotate operationId = %#v", rotate["operationId"])
	}
	rotateParameters, ok := rotate["parameters"].([]any)
	if !ok || len(rotateParameters) != 2 {
		t.Fatalf("rotate parameters = %#v", rotate["parameters"])
	}
	resourceParameter, ok := rotateParameters[0].(map[string]any)
	if !ok || resourceParameter["$ref"] != "#/components/parameters/resourceId" {
		t.Errorf("rotate resource parameter = %#v", rotateParameters[0])
	}
	assertCredentialIdempotencyHeader(t, rotateParameters[1])
	rotateRequestBody := inventoryContractMap(t, rotate, "requestBody")
	if rotateRequestBody["required"] != true {
		t.Errorf("rotate requestBody.required = %#v", rotateRequestBody["required"])
	}
	rotateContent := inventoryContractMap(t, rotateRequestBody, "content")
	rotateMediaType := inventoryContractMap(t, rotateContent, "application/json")
	rotateRequestSchema := inventoryContractMap(t, rotateMediaType, "schema")
	if rotateRequestSchema["$ref"] != "#/components/schemas/WorkerCredentialRotateRequest" {
		t.Errorf("rotate request schema = %#v", rotateRequestSchema)
	}
	assertCredentialResponses(t, rotate, []string{"200", "400", "401", "403", "404", "409"})

	components := inventoryContractMap(t, document, "components")
	schemas := inventoryContractMap(t, components, "schemas")
	enrollment := inventoryContractMap(t, schemas, "WorkerEnrollmentResponse")
	enrollmentRequired := inventoryContractStringSet(t, enrollment, "required")
	if !enrollmentRequired["credential_generation"] {
		t.Errorf("WorkerEnrollmentResponse required = %#v", enrollment["required"])
	}
	enrollmentProperties := inventoryContractMap(t, enrollment, "properties")
	generation := inventoryContractMap(t, enrollmentProperties, "credential_generation")
	if generation["type"] != "integer" || generation["format"] != "int64" || generation["minimum"] != 0 {
		t.Errorf("credential_generation constraints = %#v", generation)
	}

	rotateRequest := inventoryContractMap(t, schemas, "WorkerCredentialRotateRequest")
	rotateRequired := inventoryContractStringSet(t, rotateRequest, "required")
	if !rotateRequired["expected_credential_generation"] {
		t.Errorf("WorkerCredentialRotateRequest required = %#v", rotateRequest["required"])
	}
}

func assertCredentialIdempotencyHeader(t *testing.T, raw any) {
	t.Helper()
	parameter, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("idempotency parameter = %#v", raw)
	}
	if parameter["name"] != "Idempotency-Key" || parameter["in"] != "header" || parameter["required"] != true {
		t.Errorf("idempotency parameter = %#v", parameter)
	}
	schema := inventoryContractMap(t, parameter, "schema")
	if schema["type"] != "string" || schema["minLength"] != 8 || schema["maxLength"] != 256 || schema["pattern"] != `^[\x21-\x7e]+$` {
		t.Errorf("Idempotency-Key constraints = %#v", schema)
	}
}

func assertCredentialResponses(t *testing.T, operation map[string]any, statuses []string) {
	t.Helper()
	responses := inventoryContractMap(t, operation, "responses")
	for _, status := range statuses {
		if responses[status] == nil {
			t.Errorf("response %s is missing: %#v", status, responses)
		}
	}
}

func inventoryContractMap(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("%q = %#v, want object", key, parent[key])
	}
	return value
}

func inventoryContractStringSet(t *testing.T, parent map[string]any, key string) map[string]bool {
	t.Helper()
	values, ok := parent[key].([]any)
	if !ok {
		t.Fatalf("%q = %#v, want array", key, parent[key])
	}
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[fmt.Sprint(value)] = true
	}
	return result
}
