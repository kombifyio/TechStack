package guardexec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kombifyio/techstack/pkg/guardrelease"
)

func stageRuntime(t *testing.T, runtimeRoot, cli, server string) string {
	t.Helper()
	stage := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stage, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"bin/stackkit": cli, "bin/stackkit-server": server} {
		if err := os.WriteFile(filepath.Join(stage, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pin, _ := json.Marshal(map[string]any{
		"schemaVersion": "techstack.stackkit-release-pin/v2", "kit": "cloud-kit", "version": "v0.49.4",
		"platform":      map[string]string{"os": "linux", "arch": "amd64"},
		"archiveSha256": digestHex("archive"), "indexSha256": digestHex("index"), "binarySha256": digestHex(cli),
		"binaryPath": filepath.Join(runtimeRoot, "bin", "stackkit"),
	})
	if err := os.WriteFile(filepath.Join(stage, "stackkits-release-pin.json"), pin, 0o644); err != nil {
		t.Fatal(err)
	}
	return stage
}

// A StackKits runtime bundle comes from the unprivileged daemon and root reads
// and runs more than its two executables. It is approved only when the whole tree
// is the one the signed release names, so a forged catalog next to genuine
// binaries is refused like a swapped binary, and the pin points into the
// root-owned runtime root.
func TestStackKitsRuntimeBundleIsApprovedOnlyForTheSignedTree(t *testing.T) {
	const root = "/app/.stackkit"
	withCatalog := func(t *testing.T, stage, catalog string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(stage, "stackkits-advanced-operations-v1.json"), []byte(catalog), 0o644); err != nil {
			t.Fatal(err)
		}
		return stage
	}
	signed := withCatalog(t, stageRuntime(t, root, "cli", "server"), `{"argv":["plan"]}`)
	tree, err := guardrelease.TreeDigest(signed)
	if err != nil {
		t.Fatal(err)
	}
	want := guardrelease.StackKitsArchRuntime{
		ArchiveSHA256: digestHex("archive"), BinarySHA256: digestHex("cli"), ServerSHA256: digestHex("server"), BundleSHA256: tree,
	}
	check := stackKitsStageCheck("v0.49.4", want, root)
	if err := check(signed); err != nil {
		t.Fatalf("the signed runtime was refused: %v", err)
	}
	extra := withCatalog(t, stageRuntime(t, root, "cli", "server"), `{"argv":["plan"]}`)
	if err := os.WriteFile(filepath.Join(extra, "bin", "helper.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, stage := range map[string]string{
		"forged catalog beside genuine binaries": withCatalog(t, stageRuntime(t, root, "cli", "server"), `{"argv":["rm","-rf","/"]}`),
		"missing catalog":                        stageRuntime(t, root, "cli", "server"),
		"extra file":                             extra,
		"swapped CLI":                            withCatalog(t, stageRuntime(t, root, "trojan-cli", "server"), `{"argv":["plan"]}`),
		"swapped server":                         withCatalog(t, stageRuntime(t, root, "cli", "trojan-server"), `{"argv":["plan"]}`),
	} {
		if err := check(stage); !IsDenial(err, CodeBindingMismatch) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	// Right tree, wrong place: the pin points outside the runtime root.
	outside := withCatalog(t, stageRuntime(t, "/home/kombify-guard", "cli", "server"), `{"argv":["plan"]}`)
	outsideTree, err := guardrelease.TreeDigest(outside)
	if err != nil {
		t.Fatal(err)
	}
	outsideWant := want
	outsideWant.BundleSHA256 = outsideTree
	if err := stackKitsStageCheck("v0.49.4", outsideWant, root)(outside); !IsDenial(err, CodeBindingMismatch) {
		t.Fatalf("pin outside the runtime root: err = %v", err)
	}
	if err := stackKitsStageCheck("v0.50.0", want, root)(signed); !IsDenial(err, CodeBindingMismatch) {
		t.Fatalf("a runtime of another release: err = %v", err)
	}
}

// The executor reads only the StackKits access manifests it was configured with,
// as bounded regular files: the daemon cannot use it to read arbitrary root-only
// files or follow a link it planted.
func TestExecutorReadsOnlyAllowlistedManifests(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "access.json")
	secret := filepath.Join(dir, "shadow")
	for path, content := range map[string]string{manifest: `{"schemaVersion":"stackkit.access-manifest/v2"}`, secret: "root:x"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "linked.json")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	huge := filepath.Join(dir, "huge.json")
	if err := os.WriteFile(huge, make([]byte, maxManifestProbe+1), 0o600); err != nil {
		t.Fatal(err)
	}
	host := &Host{cfg: HostConfig{ManifestPaths: []string{manifest, link, huge, filepath.Join(dir, "missing.json")}}}
	if content, err := host.ProbeFile(t.Context(), manifest); err != nil || string(content) != `{"schemaVersion":"stackkit.access-manifest/v2"}` {
		t.Fatalf("the allowlisted manifest was not read: %q %v", content, err)
	}
	if _, err := host.ProbeFile(t.Context(), secret); !IsDenial(err, CodeNotAllowed) {
		t.Fatalf("a file outside the allowlist err = %v", err)
	}
	if _, err := host.ProbeFile(t.Context(), filepath.Join(dir, "sub", "..", "shadow")); !IsDenial(err, CodeNotAllowed) {
		t.Fatalf("a traversal into a file outside the allowlist err = %v", err)
	}
	if _, err := host.ProbeFile(t.Context(), link); !IsDenial(err, CodeNotAllowed) {
		t.Fatalf("an allowlisted path that is a symlink err = %v", err)
	}
	if _, err := host.ProbeFile(t.Context(), huge); !IsDenial(err, CodeNotAllowed) {
		t.Fatalf("an oversized manifest err = %v", err)
	}
	if _, err := host.ProbeFile(t.Context(), filepath.Join(dir, "missing.json")); !IsDenial(err, CodeNotFound) {
		t.Fatalf("a missing manifest err = %v", err)
	}
}
