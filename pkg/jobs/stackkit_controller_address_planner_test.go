package jobs

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

func TestControllerAddressPlannerAdmitsOnlyTheExactPairedExecutable(t *testing.T) {
	for _, scenario := range []string{"paired", "changed executable", "wrong version", "wrong index", "wrong target archive", "wrong native platform", "alternate path"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "bin"), 0o700); err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(dir, "bin", "stackkit.exe")
			content := []byte("verified-native-executable")
			if err := os.WriteFile(binary, content, 0o700); err != nil {
				t.Fatal(err)
			}
			target := stackkitrelease.Receipt{Kit: "basement-kit", Version: "v0.47.4", Platform: stackkitrelease.Platform{OS: "linux", Arch: "amd64"}, ArchiveSHA256: strings.Repeat("a", 64), IndexSHA256: strings.Repeat("b", 64)}
			binding := controllerAddressPlannerPin{SchemaVersion: controllerAddressPlannerSchema, LinuxArchiveSHA256: target.ArchiveSHA256, Controller: stackkitrelease.Pin{SchemaVersion: stackkitrelease.PinSchemaVersion, Kit: target.Kit, Version: target.Version, Platform: stackkitrelease.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, ArchiveSHA256: strings.Repeat("c", 64), IndexSHA256: target.IndexSHA256, BinarySHA256: fmt.Sprintf("%x", sha256.Sum256(content)), BinaryPath: "bin/stackkit.exe"}}
			switch scenario {
			case "changed executable":
				if err := os.WriteFile(binary, []byte("different executable"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "wrong version":
				binding.Controller.Version = "v0.47.3"
			case "wrong index":
				binding.Controller.IndexSHA256 = strings.Repeat("d", 64)
			case "wrong target archive":
				binding.LinuxArchiveSHA256 = strings.Repeat("e", 64)
			case "wrong native platform":
				binding.Controller.Platform.OS = "unknown"
			case "alternate path":
				binding.Controller.BinaryPath = "../stackkit.exe"
			}
			raw, _ := json.Marshal(binding)
			if err := os.WriteFile(filepath.Join(dir, controllerAddressPlannerFile), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			admitted, err := admitControllerAddressPlanner(dir, target)
			if scenario == "paired" {
				if err != nil || admitted.BinaryPath() != binary {
					t.Fatalf("paired native planner unavailable: %v", err)
				}
			} else if err == nil || admitted.BinaryPath() != "" {
				t.Fatal("unbound executable was admitted")
			}
		})
	}
}
