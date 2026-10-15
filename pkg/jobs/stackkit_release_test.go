package jobs

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/servermaintenance"
)

func TestManagedCommandsUseServedRuntimeWithoutChangingCompilerIdentity(t *testing.T) {
	root := t.TempDir()
	binary := []byte("immutable compiler artifact; not executed")
	binaryPath := filepath.Join(root, "stackkit")
	if err := os.WriteFile(binaryPath, binary, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(binary)
	pin := stackkitrelease.Pin{
		SchemaVersion: stackkitrelease.PinSchemaVersion, Kit: "cloud-kit", Version: "v0.51.0",
		Platform:      stackkitrelease.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH},
		ArchiveSHA256: strings.Repeat("a", 64), IndexSHA256: strings.Repeat("b", 64),
		BinarySHA256: hex.EncodeToString(digest[:]), BinaryPath: binaryPath,
	}
	pinBytes, err := json.Marshal(pin)
	if err != nil {
		t.Fatal(err)
	}
	pinPath := filepath.Join(root, "compiler-pin.json")
	if err := os.WriteFile(pinPath, pinBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(stackKitReleasePinEnv, pinPath)
	t.Setenv(stackKitReleaseCacheEnv, root)

	runtimeBinary := []byte("immutable served runtime artifact; not executed")
	runtimeDigest := sha256.Sum256(runtimeBinary)
	pin.Version, pin.Platform, pin.BinaryPath = "v0.50.9", stackkitrelease.Platform{OS: "linux", Arch: "amd64"}, "/app/.stackkit/bin/stackkit"
	pin.ArchiveSHA256, pin.IndexSHA256, pin.BinarySHA256 = strings.Repeat("c", 64), strings.Repeat("d", 64), hex.EncodeToString(runtimeDigest[:])
	pinBytes, err = json.Marshal(pin)
	if err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(root, "served-runtime.tar.gz")
	file, err := os.Create(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	archive := tar.NewWriter(gz)
	for _, entry := range []struct {
		name string
		data []byte
	}{{".stackkit/stackkits-release-pin.json", pinBytes}, {".stackkit/bin/stackkit", runtimeBinary}} {
		if err := archive.WriteHeader(&tar.Header{Name: entry.name, Mode: 0o700, Size: int64(len(entry.data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	for _, close := range []func() error{archive.Close, gz.Close, file.Close} {
		if err := close(); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(stackKitReleaseBundleEnv, bundlePath)
	sender := &advancedResultSender{success: true, result: `{"schemaVersion":"stackkit.command-result/v1","status":"success","data":{"schema_version":"stackkit.host-maintenance/v1","plan_digest":"sha256:` + strings.Repeat("e", 64) + `","outcome":"applied"}}`}
	target := servermaintenance.Target{TenantID: "tenant-a", AgentID: "agent-a", JobID: "job-a"}
	if _, err := NewHostMaintenanceDispatcher(sender).Plan(t.Context(), target); err != nil {
		t.Fatal(err)
	}
	dispatched := false
	for _, command := range sender.commands {
		if command.Release.GetVersion() != pin.Version || command.Release.GetArchiveSha256() != pin.ArchiveSHA256 || command.Release.GetReleaseIndexSha256() != pin.IndexSHA256 {
			t.Fatalf("managed command did not use the served runtime: %v", command.Release)
		}
		dispatched = true
	}
	if !dispatched {
		t.Fatal("managed operation was not dispatched")
	}
	compiler, err := ConfiguredPinnedStackKitRelease()
	if err != nil || compiler == nil || compiler.Receipt().Version != "v0.51.0" {
		t.Fatalf("compiler identity changed: %v, %v", compiler, err)
	}
	if err := os.WriteFile(bundlePath, []byte("invalid served archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfiguredTargetStackKitRelease(); err == nil {
		t.Fatal("invalid served runtime silently fell back to the compiler")
	}
	t.Setenv(stackKitReleaseBundleEnv, "")
	standalone, err := ConfiguredTargetStackKitRelease()
	if err != nil || standalone == nil || standalone.Receipt().Version != compiler.Receipt().Version {
		t.Fatalf("unbundled target lost its configured release: %v, %v", standalone, err)
	}
}
