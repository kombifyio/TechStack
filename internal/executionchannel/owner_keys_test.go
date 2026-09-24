package executionchannel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	channelKeyLine = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIChannelKeyBlobForTests kombify-channel"
	ownerKeyLine   = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOwnerKeyBlobForTests owner@laptop"
	channelBlob    = "AAAAC3NzaC1lZDI1NTE5AAAAIChannelKeyBlobForTests"
)

func runOwnerKeyScript(t *testing.T, home, script, stdin string) (string, error) {
	t.Helper()
	command := exec.Command("sh", "-c", script)
	command.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	command.Stdin = strings.NewReader(stdin)
	output, err := command.CombinedOutput()
	return string(output), err
}

func writeAuthorizedKeys(t *testing.T, content string) (string, string) {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, ".ssh", "authorized_keys")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return home, file
}

// Owner SSH revocation is an access-control boundary on a node the control
// plane must keep managing: it removes every owner key, keeps the execution
// channel's own key, and refuses any rewrite that would lock the channel out.
func TestOwnerKeyRevocationNeverLocksOutTheExecutionChannel(t *testing.T) {
	home, file := writeAuthorizedKeys(t, channelKeyLine+"\n"+ownerKeyLine+"\n")
	if _, err := runOwnerKeyScript(t, home, disableOwnerKeysScript, channelBlob+"\n"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	content, _ := os.ReadFile(file)
	if !strings.Contains(string(content), channelKeyLine) || strings.Contains(string(content), ownerKeyLine) {
		t.Fatalf("after disable authorized_keys = %q", content)
	}

	output, err := runOwnerKeyScript(t, home, enableOwnerKeysScript, "channel "+channelBlob+"\nowner "+ownerKeyLine+"\n")
	if err != nil || parseCounts(output)["installed"] != 1 || parseCounts(output)["present"] != 1 {
		t.Fatalf("enable output=%q err=%v", output, err)
	}
	content, _ = os.ReadFile(file)
	if !strings.Contains(string(content), channelKeyLine) || !strings.Contains(string(content), ownerKeyLine) {
		t.Fatalf("after enable authorized_keys = %q", content)
	}

	ownerOnlyHome, ownerOnlyFile := writeAuthorizedKeys(t, ownerKeyLine+"\n")
	if _, err := runOwnerKeyScript(t, ownerOnlyHome, disableOwnerKeysScript, channelBlob+"\n"); err == nil {
		t.Fatal("a rewrite without the channel key was accepted")
	}
	if content, _ := os.ReadFile(ownerOnlyFile); string(content) != ownerKeyLine+"\n" {
		t.Fatalf("refused rewrite still changed authorized_keys: %q", content)
	}
}
