package guardexec

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/guardbinding"
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

// An executor with an empty pin file refuses a mutating command before it
// reaches the host, and still runs an observation.
func TestEmptyBindingPinsDenyMutationsAndAdmitObservations(t *testing.T) {
	backend := &pinBoundaryBackend{}
	server := &Server{
		Backend: backend,
		Pins: func() ([]guardbinding.PinnedKey, error) {
			return nil, nil
		},
	}
	release := &agentpb.StackKitReleasePin{
		Version: "v0.48.9", PlatformOs: "linux", PlatformArch: "amd64",
		ArchiveSha256: strings.Repeat("ab", 32), ReleaseIndexSha256: strings.Repeat("cd", 32),
	}
	mutation := &agentpb.StackKitCommand{
		CommandId: "pin-deny-reboot", Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_REBOOT,
		OwnerApproved: true, Release: release,
	}
	denial := server.execute(context.Background(), executeRaw(t, mutation), &frameWriter{w: &bytes.Buffer{}})
	if denial == nil || denial.Code != CodeBindingMismatch || backend.executed != 0 {
		t.Fatalf("mutation denial = %+v, executed = %d", denial, backend.executed)
	}

	observation := &agentpb.StackKitCommand{
		CommandId: "pin-observe-plan", Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN,
		WorkingDirectory: "/opt/stackkit", Release: release,
	}
	if denial := server.execute(context.Background(), executeRaw(t, observation), &frameWriter{w: &bytes.Buffer{}}); denial != nil || backend.executed != 1 {
		t.Fatalf("observation denial = %+v, executed = %d", denial, backend.executed)
	}
}

func executeRaw(t *testing.T, command *agentpb.StackKitCommand) json.RawMessage {
	t.Helper()
	raw, err := protojson.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := guardbinding.CanonicalDigest(raw)
	if err != nil {
		t.Fatal(err)
	}
	params, err := json.Marshal(ExecuteParams{Command: raw, Binding: guardbinding.Binding{
		CommandID: command.GetCommandId(), Operation: command.Operation.String(),
		PayloadDigest: digest, WorkerGeneration: 1, CurrentGeneration: 1,
		AuthorityExpiresAt: time.Now().Add(time.Hour), ContractVersion: guardbinding.ContractVersion,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return params
}

// pinBoundaryBackend records whether a command reached the host.
type pinBoundaryBackend struct {
	executed int
}

func (b *pinBoundaryBackend) Execute(_ context.Context, command *agentpb.StackKitCommand, _ func(ProcessRef), _ func(*agentpb.LogEntry)) *agentpb.StackKitResult {
	b.executed++
	return &agentpb.StackKitResult{CommandId: command.GetCommandId(), Success: true}
}

func (b *pinBoundaryBackend) Capabilities(context.Context) []string { return nil }
func (b *pinBoundaryBackend) RuntimeDescribe(context.Context) (RuntimeDescription, error) {
	return RuntimeDescription{}, nil
}
func (b *pinBoundaryBackend) RuntimeInstall(context.Context, RuntimeInstallParams) (RuntimeEvidence, error) {
	return RuntimeEvidence{}, nil
}
func (b *pinBoundaryBackend) ProbeDocker(context.Context, string) ([]byte, error) { return nil, nil }
func (b *pinBoundaryBackend) ProbeFile(context.Context, string) ([]byte, error)   { return nil, nil }
func (b *pinBoundaryBackend) UpdateStage(context.Context, StageParams) (StageResult, error) {
	return StageResult{}, nil
}
func (b *pinBoundaryBackend) UpdateCommit(context.Context, CommitParams) (CommitResult, error) {
	return CommitResult{}, nil
}
func (b *pinBoundaryBackend) HostSecurityVerify(context.Context) (Observation, error) {
	return Observation{}, nil
}
func (b *pinBoundaryBackend) BackupStatus(context.Context) (Observation, error) {
	return Observation{}, nil
}
func (b *pinBoundaryBackend) Status(context.Context, bool) Status { return Status{} }
func (b *pinBoundaryBackend) DenyMutations() bool                 { return false }
