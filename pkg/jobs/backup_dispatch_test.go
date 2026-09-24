package jobs

import (
	"context"
	"errors"
	"testing"

	agentpb "github.com/kombifyio/techstack/pkg/api/agentpb"
)

type recordingCommander struct {
	commands []*agentpb.StackKitCommand
	agents   []string
	result   *agentpb.StackKitResult
	err      error
}

func (r *recordingCommander) SendStackKitCommand(_ context.Context, agentID string, cmd *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	r.agents = append(r.agents, agentID)
	r.commands = append(r.commands, cmd)
	if r.err != nil {
		return nil, r.err
	}
	if r.result != nil {
		return r.result, nil
	}
	return &agentpb.StackKitResult{Success: true}, nil
}

func backupJob() *Job {
	return &Job{
		ID: "job-abc", Type: JobTypeBackup, TargetID: "stack-a", TargetName: "photos",
		Payload: map[string]interface{}{"tenant_id": "tenant-a", "owner_id": "owner-a"},
	}
}

// TestBackupPrefersTheTypedAgentCommand pins the transport. The Guard polls
// outbound and holds a per-agent token, so the closed command set is the only
// path to a node; the HTTP runner exists for a co-located self-hosted server.
func TestBackupPrefersTheTypedAgentCommand(t *testing.T) {
	commander := &recordingCommander{}
	httpRunner := &recordingBackupRunner{}
	handler := BackupHandler(&ProvisionConfig{
		StackKitCommander: commander,
		RuntimeActions:    RuntimeActions{BackupRunner: httpRunner},
		BackupAgentResolver: func(context.Context, string, string) (string, error) {
			return "worker-7", nil
		},
	})

	if err := handler(context.Background(), backupJob(), nil); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(commander.commands) != 1 {
		t.Fatalf("expected one typed command, got %d", len(commander.commands))
	}
	if len(httpRunner.calls) != 0 {
		t.Fatalf("the legacy HTTP runner must not be used when a commander exists: %+v", httpRunner.calls)
	}
	cmd := commander.commands[0]
	if cmd.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_RUN {
		t.Fatalf("operation = %s", cmd.Operation)
	}
	if cmd.CommandId != "job-abc" {
		t.Fatalf("the job id must be the idempotency key, got %q", cmd.CommandId)
	}
	if commander.agents[0] != "worker-7" {
		t.Fatalf("agent = %q", commander.agents[0])
	}
}

func TestBackupRefusesWithoutAnAgent(t *testing.T) {
	commander := &recordingCommander{}
	for _, resolver := range []BackupAgentResolver{
		nil,
		func(context.Context, string, string) (string, error) { return "", nil },
		func(context.Context, string, string) (string, error) { return "", errors.New("worker store down") },
	} {
		handler := BackupHandler(&ProvisionConfig{StackKitCommander: commander, BackupAgentResolver: resolver})
		if err := handler(context.Background(), backupJob(), nil); err == nil {
			t.Fatal("a stack without a resolvable agent must not dispatch")
		}
	}
	if len(commander.commands) != 0 {
		t.Fatalf("nothing may be sent without an agent: %+v", commander.commands)
	}
}

// TestBackupSurfacesANodeFailure keeps a failed snapshot from being recorded as
// a successful backup, which is the one outcome a backup product cannot afford.
func TestBackupSurfacesANodeFailure(t *testing.T) {
	handler := BackupHandler(&ProvisionConfig{
		StackKitCommander:   &recordingCommander{result: &agentpb.StackKitResult{Success: false, ExitCode: 2}},
		BackupAgentResolver: func(context.Context, string, string) (string, error) { return "worker-7", nil },
	})
	if err := handler(context.Background(), backupJob(), nil); err == nil {
		t.Fatal("an unsuccessful node result must fail the job")
	}
}

func TestBackupRefusesWithNoDispatcherAtAll(t *testing.T) {
	handler := BackupHandler(&ProvisionConfig{})
	if err := handler(context.Background(), backupJob(), nil); err == nil {
		t.Fatal("a runtime with no dispatcher must fail rather than report success")
	}
}
