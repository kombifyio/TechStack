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

// This pinned CLI stand-in consumes the admission input before writing intent.
// It models that seam only: StackKits owns the real manifest/CUE/retry policy.
const preinstalledAdmissionCLI = `#!/bin/sh
set -eu
test "${TECHSTACK_MANAGED:-}" = 1
test -z "${TECHSTACK_AGENT_TOKEN:-}${KOMBIFY_SECRET:-}${SERVICE_AUTH_SECRET:-}"
manifest=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = --preinstalled-manifest ]; then shift; manifest=$1; fi
  shift
done
if [ -d "$IMAGE_CACHE_FIXTURE" ]; then
  test -n "$manifest"
  test "$(cat "$manifest")" = admitted
else
  test -z "$manifest"
fi
candidate=$(cat)
if [ -f intent ]; then
  test "$(cat intent)" = "$candidate"
else
  printf '%s' "$candidate" > intent
fi
`

func TestManagedInitDelegatesProtectedImageAdmissionBeforeIntent(t *testing.T) {
	// The executor runs as root on managed Linux hosts. Non-root test runners
	// exercise refusal of their own untrusted cache; the root gate also checks
	// successful admission and its retry through the actual child process.
	root := t.TempDir()
	if os.Geteuid() == 0 {
		var err error
		root, err = os.MkdirTemp("/root", "techstack-image-admission-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(root) })
	}
	binary := filepath.Join(root, "stackkit")
	if err := os.WriteFile(binary, []byte(preinstalledAdmissionCLI), 0o755); err != nil { // #nosec G306 -- executable test stand-in.
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(preinstalledAdmissionCLI))
	archive, index := strings.Repeat("a", 64), strings.Repeat("b", 64)
	pin, err := json.Marshal(map[string]any{
		"schemaVersion": "techstack.stackkit-release-pin/v2", "kit": "basement-kit", "version": "v0.49.7",
		"platform":      map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH},
		"archiveSha256": archive, "indexSha256": index, "binarySha256": hex.EncodeToString(digest[:]), "binaryPath": binary,
	})
	if err != nil {
		t.Fatal(err)
	}
	pinPath := filepath.Join(root, "pin.json")
	if err := os.WriteFile(pinPath, pin, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TECHSTACK_AGENT_TOKEN", "must-not-reach-init")
	t.Setenv("KOMBIFY_SECRET", "must-not-reach-init")
	t.Setenv("SERVICE_AUTH_SECRET", "must-not-reach-init")
	for _, scenario := range []string{"ordinary", "image", "malformed", "missing", "writable", "manifest-symlink", "ancestor-symlink", "early-inventory"} {
		t.Run(scenario, func(t *testing.T) {
			base := filepath.Join(root, scenario)
			work := filepath.Join(base, "workspace")
			cache := filepath.Join(base, "cache")
			manifest := filepath.Join(cache, "neutral-image.json")
			if err := os.MkdirAll(work, 0o750); err != nil {
				t.Fatal(err)
			}
			if scenario != "ordinary" {
				if err := os.Mkdir(cache, 0o755); err != nil {
					t.Fatal(err)
				}
				if scenario != "missing" {
					content := "admitted"
					if scenario == "malformed" {
						content = "rejected"
					}
					if err := os.WriteFile(manifest, []byte(content), 0o644); err != nil { // #nosec G306 -- non-secret test manifest.
						t.Fatal(err)
					}
				}
			}
			switch scenario {
			case "writable":
				if err := os.Chmod(cache, 0o777); err != nil {
					t.Fatal(err)
				}
			case "manifest-symlink":
				real := filepath.Join(base, "real-manifest")
				if err := os.Rename(manifest, real); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(real, manifest); err != nil {
					t.Fatal(err)
				}
			case "ancestor-symlink":
				real := filepath.Join(base, "real-cache")
				if err := os.Rename(cache, real); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(real, cache); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("IMAGE_CACHE_FIXTURE", cache)
			executor := &StackKitExecutor{pinPath: pinPath, cacheRoot: filepath.Join(root, "releases"), preinstalledManifestPath: manifest}
			candidate := []byte(`{"metadata":{"name":"home-stack"},"kit":{"slug":"basement-kit"},"generation":{"target":"terramate"}}`)
			command := &agentpb.StackKitCommand{
				CommandId: scenario, Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_INIT,
				Stackkit: "basement-kit", StackName: "home-stack", OwnerEmail: "owner@example.com", OwnerApproved: true,
				WorkingDirectory: work, CandidateSpecJson: candidate,
				Release: &agentpb.StackKitReleasePin{Version: "v0.49.7", PlatformOs: runtime.GOOS, PlatformArch: runtime.GOARCH, ArchiveSha256: archive, ReleaseIndexSha256: index},
			}
			if scenario == "early-inventory" {
				command.InventoryJson = []byte(`{"schemaVersion":"stackkit.inventory/v1"}`)
				if err := os.Mkdir(filepath.Join(work, ".stackkit"), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(work, ".stackkit", "inventory.json"), []byte("previous inventory"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			wantSuccess := scenario == "ordinary" || scenario == "image" && os.Geteuid() == 0
			result := executor.Execute(context.Background(), command)
			if result.Success != wantSuccess {
				t.Fatalf("success=%t, want %t: %s", result.Success, wantSuccess, result.Stderr)
			}
			intent, err := os.ReadFile(filepath.Join(work, "intent"))
			if wantSuccess {
				if err != nil || string(intent) != string(candidate) {
					t.Fatalf("intent=%q: %v", intent, err)
				}
				command.CommandId += "-retry"
				if retry := executor.Execute(context.Background(), command); !retry.Success {
					t.Fatalf("admitted retry failed: %s", retry.Stderr)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("denied admission wrote intent %q: %v", intent, err)
			}
			if scenario == "early-inventory" {
				inventory, err := os.ReadFile(filepath.Join(work, ".stackkit", "inventory.json"))
				if err != nil || string(inventory) != "previous inventory" {
					t.Fatalf("denied admission changed inventory to %q: %v", inventory, err)
				}
			}
		})
	}
}
