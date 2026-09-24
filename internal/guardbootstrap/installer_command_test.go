package guardbootstrap

import (
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/pairingtoken"
)

func TestRenderInstallerOneLinerUsesServedInstaller(t *testing.T) {
	tenantID := "tenant-1"
	raw, _, err := pairingtoken.Generate(tenantID)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	command, err := RenderInstallerOneLiner("https://techstack.kombify.io", raw)
	if err != nil {
		t.Fatalf("RenderInstallerOneLiner: %v", err)
	}
	for _, want := range []string{
		"https://techstack.kombify.io/install.sh",
		"KOMBI_SERVER=",
		"KOMBI_TOKEN=",
		"bash",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("command missing %q: %s", want, command)
		}
	}
}

func TestRenderInstallerOneLinerAllowsSelfHostedHTTPOrigin(t *testing.T) {
	raw, _, err := pairingtoken.Generate("tenant-local")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	command, err := RenderInstallerOneLiner("http://127.0.0.1:5260", raw)
	if err != nil {
		t.Fatalf("RenderInstallerOneLiner: %v", err)
	}
	if !strings.Contains(command, "http://127.0.0.1:5260/install.sh") {
		t.Fatalf("expected loopback install URL, got %q", command)
	}
}
