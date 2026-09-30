package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// The pinned CLI stand-in records the capability file it was handed and its
// mode, then answers like `stackkit advanced restore-drill run --json`.
const advancedCapabilityRecorderScript = `#!/bin/sh
previous=""
for word in "$@"; do
  if [ "$previous" = "--capability" ]; then
    stat -c %a "$word" > "RECORD.mode"
    cat "$word" > "RECORD.bytes"
  fi
  previous="$word"
done
printf '%s\n' '{"schemaVersion":"stackkit.command-result/v1","command":"stackkit advanced restore-drill run","status":"success","data":{"schemaVersion":"stackkit.restore-drill-report/v1","status":"succeeded"}}'
`

func TestAgentHandsTheCapabilityOverAsAPrivateFileAndRemovesIt(t *testing.T) {
	root := t.TempDir()
	record := filepath.Join(root, "record")
	binary := filepath.Join(root, "stackkit")
	script := []byte(strings.ReplaceAll(advancedCapabilityRecorderScript, "RECORD", record))
	if err := os.WriteFile(binary, script, 0o755); err != nil { // #nosec G306 -- executable test stand-in.
		t.Fatal(err)
	}
	binaryDigest := sha256.Sum256(script)
	archive, index := strings.Repeat("a", 64), strings.Repeat("b", 64)
	pin, _ := json.Marshal(map[string]any{
		"schemaVersion": "techstack.stackkit-release-pin/v2", "kit": "basement-kit", "version": "v0.47.0",
		"platform":      map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH},
		"archiveSha256": archive, "indexSha256": index, "binarySha256": hex.EncodeToString(binaryDigest[:]), "binaryPath": binary,
	})
	pinPath := filepath.Join(root, "stackkits-release-pin.json")
	if err := os.WriteFile(pinPath, pin, 0o600); err != nil {
		t.Fatal(err)
	}
	// A release that ships the catalog lists restore.drill as available.
	catalog, err := os.ReadFile(filepath.Join("..", "..", "internal", "stackkitrelease", "pinned-advanced-operations.json")) // #nosec G304 G703 -- fixed repository fixture.
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stackkits-advanced-operations-v1.json"), catalog, 0o600); err != nil { // #nosec G703 -- test temp directory.
		t.Fatal(err)
	}
	workDir := filepath.Join(root, "workspace")
	if err := os.Mkdir(workDir, 0o750); err != nil {
		t.Fatal(err)
	}
	capability := []byte(`{"schemaVersion":"stackkit.advanced-capability/v1","capabilityId":"test"}`)
	executor := &StackKitExecutor{pinPath: pinPath, cacheRoot: filepath.Join(root, "cache")}
	result := executor.Execute(context.Background(), &agentpb.StackKitCommand{
		CommandId: "job-drill-1", Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_ADVANCED_RESTORE_DRILL,
		WorkingDirectory: workDir, OwnerApproved: true, AdvancedCapability: capability,
		Release: &agentpb.StackKitReleasePin{Version: "v0.47.0", PlatformOs: runtime.GOOS, PlatformArch: runtime.GOARCH, ArchiveSha256: archive, ReleaseIndexSha256: index},
	})
	if !result.Success {
		t.Fatalf("restore drill failed: %s", result.Stderr)
	}
	handed, _ := os.ReadFile(record + ".bytes")
	mode, _ := os.ReadFile(record + ".mode")
	if string(handed) != string(capability) || strings.TrimSpace(string(mode)) != "600" {
		t.Fatalf("CLI read capability %q with mode %q, want the dispatched bytes in a 0600 file", handed, mode)
	}
	left, _ := filepath.Glob(filepath.Join(workDir, ".stackkit", ".techstack-capability-*"))
	if len(left) != 0 {
		t.Fatalf("capability files left after the command: %v", left)
	}
}
