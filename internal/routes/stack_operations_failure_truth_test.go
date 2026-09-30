package routes

import (
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

// Regression (owner report 2026-09-26): a failed Cloudreve lifecycle action
// stayed at the top of the dashboard after a later successful rollout,
// because only a newer job of the same type could supersede it.
func TestLatestFailureIsSupersededByLaterSuccessfulRollout(t *testing.T) {
	t0 := time.Date(2026, 9, 24, 13, 0, 0, 0, time.UTC)
	jobs := []controlplane.Job{ // newest first, as ListJobsByStack returns them
		{ID: "job-deploy-ok", Type: "deploy", State: "completed", CreatedAt: t0.Add(2 * time.Hour)},
		{ID: "job-cloudreve", Type: "stackkit_lifecycle", State: "failed", Error: "cloudreve: container unhealthy", CreatedAt: t0.Add(time.Hour)},
		{ID: "job-deploy-failed", Type: "deploy", State: "failed", CreatedAt: t0},
	}
	if got := latestStackFailureFromJobs(jobs); got != nil {
		t.Fatalf("latest failure = %+v, want none after the successful rollout", got)
	}

	// A registration ("update") is not a rollout and does not hide the failure.
	jobs[0] = controlplane.Job{ID: "job-register", Type: "update", State: "completed", CreatedAt: t0.Add(2 * time.Hour)}
	if got := latestStackFailureFromJobs(jobs); got == nil || got.JobID != "job-cloudreve" {
		t.Fatalf("latest failure = %+v, want the Cloudreve failure", got)
	}
}

// Regression: a rollout failure without a recorded target rendered as an
// unassigned banner although the deployment had a server, and a failure of a
// server that no longer exists stayed on the page.
func TestRolloutFailureIsAssignedToItsServer(t *testing.T) {
	node := stackOperationServer{ID: "srv-1", ServerID: "srv-1", AgentID: "agent-new", Assignment: "stack"}

	untargeted := &stackLatestFailure{JobID: "job-cloudreve", Type: "stackkit_lifecycle", State: "failed"}
	got := resolveFailureTarget(untargeted, []stackOperationServer{node}, nil)
	if got == nil || got.ServerID != "srv-1" {
		t.Fatalf("untargeted failure = %+v, want it on srv-1", got)
	}

	gone := &stackLatestFailure{JobID: "job-old", Type: "deploy", State: "failed", AgentID: "agent-destroyed"}
	if got := resolveFailureTarget(gone, []stackOperationServer{node}, nil); got != nil {
		t.Fatalf("failure of a destroyed server = %+v, want none", got)
	}

	if got := resolveFailureTarget(untargeted, nil, nil); got != untargeted {
		t.Fatalf("failure without any server = %+v, want it kept unassigned", got)
	}
}
