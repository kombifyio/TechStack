package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

func TestStackKitAdvancedTrustImportPinsAndFeedsTheExactBundle(t *testing.T) {
	bundle := []byte(`{"keys":[{"issuerId":"techstack.test","keyId":"ed25519://sha256/00","publicKey":"AA"}],"schemaVersion":"stackkit.advanced-trust-bundle/v1"}`)
	command := &agentpb.StackKitCommand{
		Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_TRUST_IMPORT, OwnerApproved: true, AdvancedTrustBundle: bundle,
	}
	args, err := stackKitOperationArgs(command)
	if err != nil {
		t.Fatalf("stackKitOperationArgs: %v", err)
	}
	digest := sha256.Sum256(bundle)
	want := []string{
		"advanced", "trust", "import", "--bundle", stackKitAdvancedTrustBundlePath,
		"--expect-sha256", "sha256:" + hex.EncodeToString(digest[:]), "--owner-approve", "--json",
	}
	if !slices.Equal(args, want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	workDir := t.TempDir()
	if materializeErr := materializeStackKitAdvancedTrustBundle(workDir, command.AdvancedTrustBundle); materializeErr != nil {
		t.Fatalf("materialize: %v", materializeErr)
	}
	fed, err := os.ReadFile(filepath.Join(workDir, filepath.FromSlash(stackKitAdvancedTrustBundlePath)))
	if err != nil || string(fed) != string(bundle) {
		t.Fatalf("CLI bundle file = %q (%v), want the exact dispatched bundle", fed, err)
	}
}
