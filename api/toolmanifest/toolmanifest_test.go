package toolmanifest

import "testing"

func TestManifestPublishesOnlyReadOnlyInventoryTools(t *testing.T) {
	manifest, err := Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"list_servers", "server_health", "list_services", "server_access_context", "get_stack_operations"}
	wantCapability := []string{"techstack.inventory.read", "techstack.inventory.read", "techstack.inventory.read", "techstack.inventory.operate", "techstack.inventory.read"}
	if manifest.Product != "techstack" || manifest.Capability != "techstack.inventory.read" || len(manifest.Tools) != len(want) {
		t.Fatalf("manifest identity/tools = %#v", manifest)
	}
	for i, tool := range manifest.Tools {
		if tool.Name != want[i] {
			t.Fatalf("tool[%d] = %q, want %q", i, tool.Name, want[i])
		}
		if tool.RequiredCapability != wantCapability[i] {
			t.Fatalf("tool %s capability = %q, want %q", tool.Name, tool.RequiredCapability, wantCapability[i])
		}
		for _, key := range []string{"tenant_id", "owner_id", "host", "token", "credential"} {
			if _, exists := tool.InputSchema["properties"].(map[string]any)[key]; exists {
				t.Fatalf("tool %s accepts forbidden argument %q", tool.Name, key)
			}
		}
		if tool.Annotations["readOnlyHint"] != true || tool.Annotations["idempotentHint"] != true || tool.Annotations["destructiveHint"] != false || tool.Annotations["openWorldHint"] != false {
			t.Fatalf("tool %s annotations = %#v", tool.Name, tool.Annotations)
		}
		if tool.Name == "get_stack_operations" {
			if tool.OperationID != "getStackOperations" || tool.HTTP.Method != "GET" || tool.HTTP.Path != "/api/v1/stacks/{id}/operations" {
				t.Fatalf("get_stack_operations binding = %#v", tool)
			}
			inputProperties, _ := tool.InputSchema["properties"].(map[string]any)
			if inputProperties["stack_id"] == nil {
				t.Fatal("get_stack_operations input is missing stack_id")
			}
			defs, _ := tool.OutputSchema["$defs"].(map[string]any)
			readiness, _ := defs["readiness"].(map[string]any)
			properties, _ := readiness["properties"].(map[string]any)
			for _, field := range []string{"status", "can_start", "required_servers", "approved_servers", "connected_servers", "pending_servers", "assigned_servers", "available_servers", "unassigned_servers", "message", "review_required"} {
				if properties[field] == nil {
					t.Fatalf("get_stack_operations readiness is missing %s", field)
				}
			}
		}
	}
}
