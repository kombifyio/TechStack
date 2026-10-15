//go:build linux && amd64

package jobs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

// A Linux controller serves its Agent a Linux runtime bundle. That bundle names
// its executable inside the bundle (a relative path), so a managed change set
// could not plan its routes with it and failed with "managed address planning
// requires an admitted controller bundle" (managed Cloud Advanced lane, phase
// change-set). The Linux controller holds the same published archive as its
// own pinned release and must plan with that, and only with that.
func TestControllerAddressPlannerUsesTheLinuxControllersOwnPairedRelease(t *testing.T) {
	for _, scenario := range []string{"paired", "other archive", "other version"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			executable := []byte("linux-native-stackkit-executable")
			digest := fmt.Sprintf("%x", sha256.Sum256(executable))
			own := filepath.Join(dir, "controller", "stackkit")
			if err := os.MkdirAll(filepath.Dir(own), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(own, executable, 0o700); err != nil {
				t.Fatal(err)
			}
			pin := stackkitrelease.Pin{
				SchemaVersion: stackkitrelease.PinSchemaVersion, Kit: "cloud-kit", Version: "v0.51.2",
				Platform:      stackkitrelease.Platform{OS: "linux", Arch: "amd64"},
				ArchiveSHA256: strings.Repeat("a", 64), IndexSHA256: strings.Repeat("b", 64),
				BinarySHA256: digest, BinaryPath: "/app/.stackkit/bin/stackkit",
			}

			bundled, err := json.Marshal(pin)
			if err != nil {
				t.Fatal(err)
			}
			var archive bytes.Buffer
			gz := gzip.NewWriter(&archive)
			writer := tar.NewWriter(gz)
			for name, content := range map[string][]byte{".stackkit/stackkits-release-pin.json": bundled, ".stackkit/bin/stackkit": executable} {
				if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
					t.Fatal(err)
				}
				if _, err := writer.Write(content); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			bundlePath := filepath.Join(dir, "stackkit-release-linux-amd64.tar.gz")
			if err := os.WriteFile(bundlePath, archive.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			target, err := stackkitrelease.ResolveLinuxRuntimeBundle(bundlePath)
			if err != nil {
				t.Fatal(err)
			}

			controller := pin
			controller.BinaryPath = own
			switch scenario {
			case "other archive":
				controller.ArchiveSHA256 = strings.Repeat("c", 64)
			case "other version":
				controller.Version = "v0.51.1"
			}
			raw, err := json.Marshal(controller)
			if err != nil {
				t.Fatal(err)
			}
			pinPath := filepath.Join(dir, "controller", "stackkits-release-pin.json")
			if err := os.WriteFile(pinPath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TECHSTACK_STACKKIT_RELEASE_PIN", pinPath)
			t.Setenv("TECHSTACK_STACKKIT_RELEASE_CACHE", filepath.Join(dir, "releases"))
			t.Setenv("TECHSTACK_STACKKITS_DIR", "")

			planner, err := controllerAddressPlannerRelease(target)
			if scenario == "paired" {
				if err != nil || planner.BinaryPath() != own {
					t.Fatalf("planner = %q, %v; want the controller's own executable %q", planner.BinaryPath(), err, own)
				}
				return
			}
			if err == nil || planner.BinaryPath() != "" {
				t.Fatalf("a different release planned routes for the runtime: %q", planner.BinaryPath())
			}
		})
	}
}
