package guardbootstrap

import (
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/pairingtoken"
)

func TestRenderInstallerCommandUsesServedInstallerAndReadsTheCapabilityFromStdin(t *testing.T) {
	raw, _, err := pairingtoken.Generate("tenant-1")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	command, err := RenderInstallerCommand("https://techstack.kombify.io")
	if err != nil {
		t.Fatalf("RenderInstallerCommand: %v", err)
	}
	for _, want := range []string{"https://techstack.kombify.io/install.sh", "KOMBI_SERVER=", "read -r KOMBI_TOKEN", "bash"} {
		if !strings.Contains(command, want) {
			t.Fatalf("command missing %q: %s", want, command)
		}
	}
	if strings.Contains(command, raw) {
		t.Fatal("the pairing capability is part of the command line")
	}
	stdin, err := InstallerStdin(raw)
	if err != nil || stdin != raw+"\n" {
		t.Fatalf("InstallerStdin = %q, %v", stdin, err)
	}
	if _, err := InstallerStdin("not-a-token"); err == nil {
		t.Fatal("a malformed capability was accepted")
	}
}

func TestRenderInstallerCommandAllowsSelfHostedHTTPOrigin(t *testing.T) {
	command, err := RenderInstallerCommand("http://127.0.0.1:5260")
	if err != nil {
		t.Fatalf("RenderInstallerCommand: %v", err)
	}
	if !strings.Contains(command, "http://127.0.0.1:5260/install.sh") {
		t.Fatalf("expected loopback install URL, got %q", command)
	}
}
