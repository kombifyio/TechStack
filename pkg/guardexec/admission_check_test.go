package guardexec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/guardrelease"
)

// An operator can ask a host whether it would admit a signed manifest without
// staging anything: an older release and an older issuance are refused with the
// codes the updater itself reports, a current one is admitted, and none of the
// questions changes the host's recorded state.
func TestAdmissionCheckRefusesOlderManifestsAndChangesNothing(t *testing.T) {
	h := newHost(t, false, func(cfg *UpdaterConfig) { cfg.Verifier = trustingVerifier{} })
	now := time.Now().UTC()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(h.handoff, "sig"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sig"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := func(name, version string, issuedAgo time.Duration) string {
		raw, err := json.Marshal(guardrelease.Manifest{
			SchemaVersion: guardrelease.ManifestSchema, Version: version, MinimumAdmittedVersion: "0.40.0",
			Artifacts: []guardrelease.Artifact{{OS: "linux", Arch: "amd64", SHA256: digestHex("guard-" + version), Size: 12}},
			IssuedAt:  now.Add(-issuedAgo), ExpiresAt: now.Add(72 * time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, target := range []string{h.handoff, dir} {
			if err := os.WriteFile(filepath.Join(target, name), raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return filepath.Join(dir, name)
	}
	sig := filepath.Join(dir, "sig")

	// The host has accepted the newest manifest (0.51.0, issued an hour ago).
	if _, err := h.updater.AuthenticateManifest(filepath.Base(manifest("newest", "0.51.0", time.Hour)), "sig"); err != nil {
		t.Fatal(err)
	}
	before := stateSnapshot(t, h.state)

	check := func(path string) ManifestAdmission {
		t.Helper()
		decision, err := h.updater.CheckManifest(path, sig)
		if err != nil {
			t.Fatal(err)
		}
		return decision
	}
	if got := check(manifest("older-release", "0.49.0", time.Minute)); got.Admitted || got.Code != guardrelease.CodeDowngrade || got.InstalledVersion != "0.50.0" || got.ManifestVersion != "0.49.0" {
		t.Fatalf("older release decision = %+v", got)
	}
	if got := check(manifest("older-issue", "0.51.0", 3*time.Hour)); got.Admitted || got.Code != guardrelease.CodeManifestSuperseded {
		t.Fatalf("older issuance decision = %+v", got)
	}
	if got := check(manifest("current", "0.51.0", 30*time.Minute)); !got.Admitted || got.Code != "" {
		t.Fatalf("current manifest decision = %+v", got)
	}

	if after := stateSnapshot(t, h.state); after != before {
		t.Fatalf("the admission check changed the executor state:\nbefore %s\nafter  %s", before, after)
	}
	if got := h.installed(t); got != "guard-0.50.0" {
		t.Fatalf("installed executable changed to %q", got)
	}
}

// stateSnapshot is every file below dir with its content digest.
func stateSnapshot(t *testing.T, dir string) string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		out = append(out, path+"="+digestHex(string(raw)))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, "\n")
}
