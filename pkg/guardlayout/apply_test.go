package guardlayout

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kombifyio/techstack/pkg/guardbinding"
)

type recordingRunner struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	return nil
}

func newRoot(t *testing.T, legacyUnit string) (root string, opts Options, runner *recordingRunner, chowned map[string][2]int) {
	t.Helper()
	root = t.TempDir()
	for _, dir := range []string{"/run/systemd/system", "/etc/systemd/system", "/etc/techstack", "/var/lib/techstack/guard"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if legacyUnit != "" {
		if err := os.WriteFile(filepath.Join(root, "/etc/systemd/system/"+AgentUnit), []byte(legacyUnit), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Existing identity, journal and enrollment of the root install.
	for _, file := range []string{"/var/lib/techstack/guard/identity.key", "/var/lib/techstack/guard/journal.log"} {
		if err := os.WriteFile(filepath.Join(root, file), []byte("keep-"+filepath.Base(file)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "/etc/techstack/agent-enrollment.json"), []byte(testEnrollment), 0o600); err != nil {
		t.Fatal(err)
	}
	runner = &recordingRunner{}
	chowned = map[string][2]int{}
	opts = Options{
		Root: root, Runner: runner, GuardUser: "kombify-guard",
		Lookup: func(string) (int, int, error) { return 990, 990, nil },
		Chown:  func(path string, uid, gid int) error { chowned[path] = [2]int{uid, gid}; return nil },
		ChownBeneath: func(r *os.Root, rel string, uid, gid int) error {
			chowned[filepath.Join(r.Name(), rel)] = [2]int{uid, gid}
			return nil
		},
		VerifyExecutor: func(context.Context, string) error { return nil },
	}
	return root, opts, runner, chowned
}

const testEnrollment = `{"data":{"runtime_agent_id":"worker-1","agent_token":"token","binding_signing_keys":[{"key_id":"key-1","public_key":"cHVibGlj"}]}}`

const rootUnit = "[Service]\nUser=root\nExecStart=/usr/local/bin/techstack agent\n"

// Migrating a running root Guard keeps its enrollment, identity and journal
// (only re-owned), keeps the old unit until the privileged executor answers, and
// restores it when the new daemon never reports ready.
func TestMigrationKeepsStateKeepsTheOldUnitUntilReadyAndRestoresItOnFailure(t *testing.T) {
	root, opts, runner, chowned := newRoot(t, rootUnit)
	agentUnit := filepath.Join(root, "/etc/systemd/system/"+AgentUnit)

	// The executor does not answer: nothing the running Guard uses is touched.
	opts.VerifyExecutor = func(context.Context, string) error { return errors.New("no answer") }
	if _, err := Apply(t.Context(), opts); err == nil {
		t.Fatal("the layout was applied although the executor does not answer")
	}
	if unit, _ := os.ReadFile(agentUnit); string(unit) != rootUnit {
		t.Fatalf("the running Guard's unit changed before the executor was proven: %q", unit)
	}

	// The executor answers, but the new daemon fails its trial: the old unit returns.
	opts.VerifyExecutor = func(context.Context, string) error { return nil }
	opts.Trial = func(context.Context, string) error { return errors.New("supervisor unavailable") }
	if _, err := Apply(t.Context(), opts); err == nil {
		t.Fatal("a failed trial start was not reported")
	}
	if unit, _ := os.ReadFile(agentUnit); string(unit) != rootUnit {
		t.Fatalf("the legacy unit was not restored: %q", unit)
	}

	var trialID string
	opts.Trial = func(ctx context.Context, id string) error { trialID = id; return StartLayoutTrial(ctx, opts, id) }
	result, err := Apply(t.Context(), opts)
	if err != nil || !result.Migrated || !result.Switched || trialID == "" || result.TrialID != trialID {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	if unit, _ := os.ReadFile(agentUnit); IsLegacyUnit(string(unit)) || !strings.Contains(string(unit), "User=kombify-guard") {
		t.Fatalf("the daemon unit still runs as root: %q", unit)
	}
	if legacy, _ := os.ReadFile(agentUnit + LegacyUnitSuffix); string(legacy) != rootUnit {
		t.Fatalf("the legacy unit copy = %q", legacy)
	}
	for _, file := range []string{"identity.key", "journal.log"} {
		content, err := os.ReadFile(filepath.Join(root, "/var/lib/techstack/guard", file))
		if err != nil || string(content) != "keep-"+file {
			t.Fatalf("%s was not preserved: %q %v", file, content, err)
		}
		if owner := chowned[filepath.Join(root, "/var/lib/techstack/guard", file)]; owner != [2]int{990, 990} {
			t.Fatalf("%s owner = %v, want the Guard user", file, owner)
		}
	}
	if owner := chowned[filepath.Join(root, "/etc/techstack/agent-enrollment.json")]; owner != [2]int{990, 990} {
		t.Fatalf("the enrollment is not owned by the Guard user: %v", owner)
	}
	// The executor keeps its own root-only copy of the pinned binding keys.
	if pins, err := guardbinding.ReadPins(filepath.Join(root, executorStateDir, guardbinding.PinFile)); err != nil || len(pins) != 1 || pins[0].KeyID != "key-1" {
		t.Fatalf("the executor did not pin the enrollment's binding key: %v %v", pins, err)
	}
	if info, _ := os.Stat(filepath.Join(root, "/var/lib/techstack/guard")); info.Mode().Perm() != 0o700 {
		t.Fatalf("the Guard state directory mode = %v", info.Mode().Perm())
	}

	// The supervisor restores the legacy unit on a failed trial.
	if err := RestoreLegacy(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	if err := FinishLayoutTrial(opts, trialID); err != nil {
		t.Fatal(err)
	}
	if unit, _ := os.ReadFile(agentUnit); string(unit) != rootUnit {
		t.Fatalf("RestoreLegacy left %q", unit)
	}
	if !strings.Contains(strings.Join(runner.calls, "\n"), "start "+AgentUnit) {
		t.Fatalf("the restored Guard was not started: %v", runner.calls)
	}
}

// A unit that belongs to someone else (the gRPC packaging unit runs as its own
// user) is never replaced by the HTTPS layout.
func TestLayoutDoesNotReplaceAForeignGuardUnit(t *testing.T) {
	foreign := "[Service]\nUser=techstack\nExecStart=/usr/bin/techstack agent\n"
	root, opts, _, _ := newRoot(t, foreign)
	if _, err := Apply(t.Context(), opts); err == nil {
		t.Fatal("a foreign unit was replaced")
	}
	if unit, _ := os.ReadFile(filepath.Join(root, "/etc/systemd/system/"+AgentUnit)); string(unit) != foreign {
		t.Fatalf("the foreign unit changed: %q", unit)
	}
}

// Re-owning a daemon-writable tree never touches a root-owned inode that the
// daemon hard-linked into it, and never follows a symlink out of the tree.
func TestReownDoesNotFollowLinksOutOfTheGuardTree(t *testing.T) {
	root, opts, _, chowned := newRoot(t, rootUnit)
	guardDir := filepath.Join(root, "/var/lib/techstack/guard")
	rootOwned := filepath.Join(root, "/etc/techstack/root-secret")
	if err := os.WriteFile(rootOwned, []byte("root only"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(rootOwned, filepath.Join(guardDir, "planted-link")); err != nil {
		t.Skipf("hard links are unavailable here: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "/etc"), filepath.Join(guardDir, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	if _, reowned := chowned[filepath.Join(guardDir, "planted-link")]; reowned {
		t.Fatal("a hard link to a root-owned file was re-owned")
	}
	if _, reowned := chowned[filepath.Join(root, "/etc/techstack/root-secret")]; reowned {
		t.Fatal("a root-owned file outside the tree was re-owned through the tree")
	}
	if owner := chowned[filepath.Join(guardDir, "identity.key")]; owner != [2]int{990, 990} {
		t.Fatalf("the daemon's own file was not re-owned: %v", owner)
	}
}
