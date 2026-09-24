package grpcserver

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// Inject the received result at the private transport adapter, then exercise
// the public log query: the regression is missing parent-job activity.
func TestStackKitResultLogsRemainQueryableByParentJob(t *testing.T) {
	for _, test := range []struct{ jobID, commandID string }{
		{"job-parent-verify", "job-parent-verify-apply"},
		{"job-parent-verify", "job-parent-verify-apply-2"},
		{"job-parent-verify", "job-parent-verify-init"},
		{"job-parent-verify", "job-parent-verify-generate"},
		{"job-parent-verify", "job-parent-verify-plan"},
		{"job-parent-verify", "job-parent-verify-verify"},
		{"direct-command", "direct-command"},
		{"direct-command-2", "direct-command-2"},
	} {
		t.Run(test.commandID, func(t *testing.T) {
			jobID, commandID := test.jobID, test.commandID
			srv := &Server{}
			srv.appendStackKitResultLogs("agent-1", &agentpb.StackKitResult{CommandId: commandID, Stderr: "runtime failed"})
			for _, entry := range srv.GetRuntimeLogs(RuntimeLogQuery{JobID: jobID}) {
				if entry.Fields["command_id"] == commandID {
					return
				}
			}
			t.Fatal("parent job query lost the received command failure")
		})
	}
}
