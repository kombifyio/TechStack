package routes

import "testing"

func TestCapabilitiesPublishAgentNativeStackOperations(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		"get_stack_routing":          "GET",
		"ensure_stack_routing":       "PUT",
		"list_stack_routing_targets": "GET",
		"resume_stack_enrollment":    "POST",
		"retry_stack_rollout":        "POST",
		"add_managed_runtime_server": "POST",
	}
	for _, capability := range capabilities() {
		method, ok := want[capability.Name]
		if !ok {
			continue
		}
		wantEndpoint := "/api/v1/stacks/{id}/routing"
		switch capability.Name {
		case "list_stack_routing_targets":
			wantEndpoint = "/api/v1/stacks/{id}/routing/targets"
		case "resume_stack_enrollment":
			wantEndpoint = "/api/v1/stacks/{id}/resume-enrollment"
		case "retry_stack_rollout":
			wantEndpoint = "/api/v1/stacks/{id}/retry-rollout"
		case "add_managed_runtime_server":
			wantEndpoint = "/api/v1/stacks/{id}/managed-runtimes"
		}
		if capability.Endpoint != wantEndpoint || capability.Method != method || !capability.Auth || !capability.Idempotent ||
			((capability.Name == "resume_stack_enrollment" || capability.Name == "retry_stack_rollout" || capability.Name == "add_managed_runtime_server") && !capability.Async) {
			t.Fatalf("capability %s = %#v", capability.Name, capability)
		}
		delete(want, capability.Name)
	}
	if len(want) != 0 {
		t.Fatalf("missing routing capabilities: %#v", want)
	}
}
