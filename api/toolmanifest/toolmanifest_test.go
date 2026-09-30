package toolmanifest

import (
	"net/http"
	"testing"

	"github.com/kombifyio/techstack/internal/gocommon/apisurface"

	"github.com/kombifyio/techstack/api/surface"
)

// The manifest is generated from the OpenAPI contract, so tool order, count and
// schema detail follow the spec. The agent boundary stays fixed: no tool
// accepts caller identity, target hosts or credentials as arguments, only GET
// operations claim to be read-only, every cost-bearing tool requires
// techstack.inventory.provision and nothing else does, and every other
// destructive tool requires the confirmation-gated techstack.inventory.operate
// capability. The Gateway catalog refuses a capability that mixes classes.
func TestManifestKeepsTheAgentToolBoundary(t *testing.T) {
	manifest, err := Parse()
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s, err := apisurface.Parse(surface.Raw)
	if err != nil {
		t.Fatalf("surface: %v", err)
	}
	costBearing := map[string]bool{}
	for _, op := range s.Operations {
		if op.MCP != nil {
			costBearing[op.OperationID] = op.MCP.CostBearing
		}
	}
	forbidden := []string{"tenant", "tenant_id", "owner_id", "subject_id", "user_id", "host", "token", "password", "secret", "credential", "ssh_key", "private_key", "api_key"}
	for _, tool := range manifest.Tools {
		properties, _ := tool.InputSchema["properties"].(map[string]any)
		for _, key := range forbidden {
			if _, exists := properties[key]; exists {
				t.Fatalf("tool %s accepts forbidden argument %q", tool.Name, key)
			}
		}
		readOnly := tool.Annotations["readOnlyHint"] == true
		if readOnly != (tool.HTTP.Method == http.MethodGet) {
			t.Fatalf("tool %s (%s) readOnlyHint = %v", tool.Name, tool.HTTP.Method, readOnly)
		}
		provision := tool.RequiredCapability == "techstack.inventory.provision"
		if costBearing[tool.OperationID] != provision {
			t.Fatalf("tool %s (costBearing %v) requires %s", tool.Name, costBearing[tool.OperationID], tool.RequiredCapability)
		}
		if tool.Annotations["destructiveHint"] == true && !provision && tool.RequiredCapability != "techstack.inventory.operate" {
			t.Fatalf("destructive tool %s requires %s", tool.Name, tool.RequiredCapability)
		}
	}
}
