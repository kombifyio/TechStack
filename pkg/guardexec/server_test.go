package guardexec

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/guardbinding"
)

type fakeBackend struct {
	mu       sync.Mutex
	executed []string
	deny     bool
	staged   int
}

func (b *fakeBackend) Execute(_ context.Context, command *agentpb.StackKitCommand, _ func(ProcessRef), _ func(*agentpb.LogEntry)) *agentpb.StackKitResult {
	b.mu.Lock()
	b.executed = append(b.executed, command.CommandId)
	b.mu.Unlock()
	return &agentpb.StackKitResult{CommandId: command.CommandId, Success: true}
}
func (b *fakeBackend) Capabilities(context.Context) []string { return nil }
func (b *fakeBackend) RuntimeDescribe(context.Context) (RuntimeDescription, error) {
	return RuntimeDescription{}, nil
}
func (b *fakeBackend) RuntimeInstall(context.Context, RuntimeInstallParams) (RuntimeEvidence, error) {
	return RuntimeEvidence{}, nil
}
func (b *fakeBackend) ProbeDocker(context.Context, string) ([]byte, error) { return nil, nil }
func (b *fakeBackend) ProbeFile(context.Context, string) ([]byte, error)   { return nil, nil }
func (b *fakeBackend) UpdateStage(context.Context, StageParams) (StageResult, error) {
	b.mu.Lock()
	b.staged++
	b.mu.Unlock()
	return StageResult{}, nil
}
func (b *fakeBackend) UpdateCommit(context.Context, CommitParams) (CommitResult, error) {
	return CommitResult{}, nil
}
func (b *fakeBackend) HostSecurityVerify(context.Context) (Observation, error) {
	return Observation{Supported: true}, nil
}
func (b *fakeBackend) BackupStatus(context.Context) (Observation, error) {
	return Observation{Supported: true}, nil
}
func (b *fakeBackend) Status(context.Context, bool) Status { return Status{Ready: true} }
func (b *fakeBackend) DenyMutations() bool                 { return b.deny }

type memoryReplay struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (r *memoryReplay) Claim(id string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[string]bool{}
	}
	fresh := !r.seen[id]
	r.seen[id] = true
	return fresh, nil
}

func serveTest(t *testing.T, server *Server) *Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "executor.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { _ = server.Serve(context.Background(), conn) }()
		}
	}()
	return &Client{Socket: path}
}

func admitted(t *testing.T, id string, mutate func(*guardbinding.Binding)) ExecuteParams {
	t.Helper()
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	command := &agentpb.StackKitCommand{
		CommandId: id, Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_RESTART,
		WorkingDirectory: "/srv/stack", StackkitInstanceId: "stackkit-main", ServiceKey: "base", OwnerApproved: true,
		Release: &agentpb.StackKitReleasePin{Version: "v0.16.0", PlatformOs: "linux", PlatformArch: "amd64", ArchiveSha256: digest, ReleaseIndexSha256: digest},
	}
	raw, err := protojson.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	payloadDigest, err := guardbinding.CanonicalDigest(raw)
	if err != nil {
		t.Fatal(err)
	}
	binding := guardbinding.Binding{
		CommandID: id, TenantID: "tenant-1", WorkerID: "worker-1", Operation: command.Operation.String(), PayloadDigest: payloadDigest,
		WorkerGeneration: 1, CurrentGeneration: 1, AuthorityExpiresAt: time.Now().Add(time.Minute), ContractVersion: guardbinding.ContractVersion,
	}
	if mutate != nil {
		mutate(&binding)
	}
	return ExecuteParams{Command: raw, Binding: binding}
}

// The executor runs only what was admitted: a payload changed after admission,
// a command that differs from its binding, an expired authority, a replayed
// identity, an unknown operation, a stranger on the socket and a host in
// recovery all stop at the boundary; nothing reaches the privileged backend.
func TestExecutorRefusesAnythingButTheExactAdmittedFreshCommand(t *testing.T) {
	backend := &fakeBackend{}
	self := uint32(os.Getuid())
	server := &Server{Backend: backend, Replay: &memoryReplay{}, AllowPeer: func(uid uint32) bool { return uid == self }}
	client := serveTest(t, server)
	execute := func(params ExecuteParams) error {
		_, err := client.Call(context.Background(), OpStackKitExecute, params, nil)
		return err
	}

	if err := execute(admitted(t, "cmd-ok", nil)); err != nil {
		t.Fatalf("the admitted command was refused: %v", err)
	}
	if err := execute(admitted(t, "cmd-ok", nil)); !IsCode(err, CodeReplayed) {
		t.Fatalf("replay err = %v", err)
	}
	altered := admitted(t, "cmd-altered", nil)
	altered.Command = []byte(string(altered.Command[:len(altered.Command)-1]) + `,"serviceKey":"other"}`)
	if err := execute(altered); !IsCode(err, CodeBindingMismatch) {
		t.Fatalf("altered payload err = %v", err)
	}
	if err := execute(admitted(t, "cmd-other", func(b *guardbinding.Binding) { b.CommandID = "cmd-swapped" })); !IsCode(err, CodeBindingMismatch) {
		t.Fatalf("swapped identity err = %v", err)
	}
	if err := execute(admitted(t, "cmd-op", func(b *guardbinding.Binding) {
		b.Operation = agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_LOGS.String()
	})); !IsCode(err, CodeBindingMismatch) {
		t.Fatalf("swapped operation err = %v", err)
	}
	if err := execute(admitted(t, "cmd-legacy", func(b *guardbinding.Binding) { b.ContractVersion = guardbinding.LegacyContractVersion })); !IsCode(err, CodeBindingMismatch) {
		t.Fatalf("self-made binding for a mutation err = %v", err)
	}
	if err := execute(admitted(t, "cmd-expired", func(b *guardbinding.Binding) { b.AuthorityExpiresAt = time.Now().Add(-time.Second) })); !IsCode(err, CodeAuthorityExpired) {
		t.Fatalf("expired authority err = %v", err)
	}
	if _, err := client.Call(context.Background(), "shell.exec", map[string]string{"argv": "rm -rf /"}, nil); !IsCode(err, CodeUnknownOperation) {
		t.Fatalf("unknown operation err = %v", err)
	}
	if _, err := client.Call(context.Background(), OpProbeDocker, ProbeDockerParams{Kind: "run"}, nil); !IsCode(err, CodeNotAllowed) {
		t.Fatalf("free-form container probe err = %v", err)
	}
	// The read-only observations run fixed argv: a caller cannot add to it.
	for _, op := range []string{OpHostSecurityVerify, OpBackupStatus} {
		if _, err := client.Call(context.Background(), op, map[string]string{"argv": "--apply"}, nil); !IsCode(err, CodeInvalidRequest) {
			t.Fatalf("%s with caller input err = %v", op, err)
		}
		if _, err := client.Call(context.Background(), op, nil, nil); err != nil {
			t.Fatalf("%s without input: %v", op, err)
		}
	}
	backend.deny = true
	if err := execute(admitted(t, "cmd-recovery", nil)); !IsCode(err, CodeRecoveryNeeded) {
		t.Fatalf("recovery err = %v", err)
	}
	if _, err := client.Call(context.Background(), OpUpdateStage, StageParams{}, nil); !IsCode(err, CodeRecoveryNeeded) {
		t.Fatalf("update during recovery err = %v", err)
	}
	backend.deny = false

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.executed) != 1 || backend.executed[0] != "cmd-ok" || backend.staged != 0 {
		t.Fatalf("the privileged backend ran %v (staged %d); only the admitted command may reach it", backend.executed, backend.staged)
	}

	stranger := serveTest(t, &Server{Backend: backend, Replay: &memoryReplay{}, AllowPeer: func(uint32) bool { return false }})
	if _, err := stranger.Call(context.Background(), OpStatus, nil, nil); !IsCode(err, CodePeerNotAllowed) {
		t.Fatalf("stranger err = %v", err)
	}
}

// A refused request is answered with its structured denial and handled: the
// socket-activated instance must exit 0, or every denial and every absent
// manifest leaves a failed unit and a degraded host behind.
func TestAnsweredDenialIsASuccessfulHandling(t *testing.T) {
	self := uint32(os.Getuid())
	server := &Server{Backend: &fakeBackend{}, Replay: &memoryReplay{}, AllowPeer: func(uid uint32) bool { return uid == self }}
	path := filepath.Join(t.TempDir(), "executor.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	handled := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			handled <- err
			return
		}
		handled <- server.Serve(context.Background(), conn)
	}()

	_, callErr := (&Client{Socket: path}).Call(context.Background(), "shell.exec", map[string]string{"argv": "id"}, nil)
	if !IsCode(callErr, CodeUnknownOperation) {
		t.Fatalf("the client was not answered with the denial: %v", callErr)
	}
	select {
	case err := <-handled:
		if err != nil {
			t.Fatalf("the executor instance reported a fault for a handled denial: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the executor instance did not finish")
	}
}
