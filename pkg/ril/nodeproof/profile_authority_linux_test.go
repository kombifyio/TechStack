//go:build linux

package nodeproof

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the authorization boundary in isolated root-owned custody; this
// does not claim that Product Apply took place.
func TestAppliedProfileEvidenceRevalidation(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root-owned isolated evidence")
	}
	dir, err := os.MkdirTemp("/var/lib", "ril-profile-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	authority := filepath.Join(dir, "authority")
	workspace := filepath.Join(dir, "workspace")
	for _, p := range []string{authority, workspace} {
		if err = os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	record := profileAuthority{Schema: "kombify.ril-applied-profile.v2", Profile: "standard", Workspace: workspace, PlanHash: "sha256:" + strings.Repeat("a", 64), ResultHash: "sha256:" + strings.Repeat("b", 64)}
	for i := 0; i < 7; i++ {
		p := filepath.Join(workspace, string(rune('a'+i)))
		data := []byte{byte(i)}
		if err = os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		record.Evidence = append(record.Evidence, profileEvidence{Path: p, Digest: "sha256:" + hex.EncodeToString(sum[:])})
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(authority, ProfileAuthorityFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = admitHostedProfile(authority); err != nil {
		t.Fatalf("unchanged applied authority: %v", err)
	}
	if err = os.WriteFile(record.Evidence[0].Path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = admitHostedProfile(authority); err == nil {
		t.Fatal("changed evidence admitted")
	}
	if err = os.Remove(filepath.Join(authority, ProfileAuthorityFile)); err != nil {
		t.Fatal(err)
	}
	if err = admitHostedProfile(authority); err == nil {
		t.Fatal("missing authority admitted")
	}
}
