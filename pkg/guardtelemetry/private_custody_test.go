package guardtelemetry_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/privatefs"
	"github.com/kombifyio/techstack/pkg/guardtelemetry"
)

func TestSpoolKeepsWritesInAdmittedPrivateCustody(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "spool")
	spool, err := guardtelemetry.OpenSpool(guardtelemetry.SpoolConfig{Dir: dir, MaxBytes: 2048, SegmentBytes: 512})
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside")
	original := []byte("private synthetic sentinel")
	if err := os.WriteFile(outside, original, 0o600); err != nil {
		t.Fatal(err)
	}
	samples := []guardtelemetry.Sample{{Name: "container_cpu_usage_percent", Value: 1, Labels: map[string]string{"container_name": strings.Repeat("x", 2400)}}}
	if _, err := spool.Append(time.Now(), samples); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state.json.tmp", "seg-00000000000000000002.jsonl.tmp"} {
		if err := os.Symlink(outside, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := spool.Append(time.Now(), samples); err != nil {
		t.Fatal(err)
	}
	assertOutside := func() {
		t.Helper()
		got, err := os.ReadFile(outside)
		if err != nil || !bytes.Equal(got, original) {
			t.Fatalf("spool changed outside custody: %q, %v", got, err)
		}
	}
	assertOutside()
	// Rename the admitted root and replace its old pathname. The existing
	// spool must continue using the admitted directory, including state writes.
	moved := filepath.Join(parent, "moved")
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(parent, "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := guardtelemetry.OpenSpool(guardtelemetry.SpoolConfig{Dir: dir}); err == nil {
		t.Fatal("spool admitted a symlink custody root")
	}
	last, err := spool.Append(time.Now(), samples)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := guardtelemetry.OpenSpool(guardtelemetry.SpoolConfig{Dir: moved, MaxBytes: 2048, SegmentBytes: 512})
	if err != nil || reopened.Epoch() != spool.Epoch() || reopened.LastSequence() != last.Sequence {
		t.Fatalf("private custody did not reopen its durable sequence: %v", err)
	}
	if err := reopened.Ack(last.Sequence); err != nil {
		t.Fatal(err)
	}
	if records, bytes := reopened.Backlog(); records != 0 || bytes != 0 {
		t.Fatal("acknowledged private spool retained pending records")
	}
	assertOutside()
	// A private directory does not establish custody of independently shared
	// existing inodes. Neither a hardlink nor a writable leaf may be repaired.
	for _, attack := range []string{"hardlink", "writable-leaf"} {
		leafRoot := filepath.Join(parent, attack)
		// First prove that the root is admitted, including its actual Windows
		// ACL. A later refusal must concern the planted leaf, not an unsafe root.
		if _, err := guardtelemetry.OpenSpool(guardtelemetry.SpoolConfig{Dir: leafRoot}); err != nil {
			t.Fatalf("could not establish private leaf custody: %v", err)
		}
		leaf := filepath.Join(leafRoot, "seg-00000000000000000001.jsonl")
		if attack == "hardlink" {
			if err := os.Link(outside, leaf); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.WriteFile(leaf, original, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := makeCustodyLeafShared(leaf); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := guardtelemetry.OpenSpool(guardtelemetry.SpoolConfig{Dir: leafRoot}); !errors.Is(err, privatefs.ErrUnsafeCustody) {
			t.Fatalf("spool did not refuse %s as unsafe private leaf custody: %v", attack, err)
		}
		assertOutside()
	}
	entries, err := os.ReadDir(other)
	if err != nil || len(entries) != 0 {
		t.Fatal("spool wrote through its replaced root pathname")
	}
	if runtime.GOOS != "windows" {
		unsafe := filepath.Join(parent, "unsafe")
		if err := os.Mkdir(unsafe, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(unsafe, 0o777); err != nil {
			t.Fatal(err)
		}
		if _, err := guardtelemetry.OpenSpool(guardtelemetry.SpoolConfig{Dir: unsafe}); err == nil {
			t.Fatal("spool admitted custody writable by another principal")
		}
	}
}
