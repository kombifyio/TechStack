package main

import (
	"context"
	"strings"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

// changeHintAnnouncer tells the Companion's notification edge that an owner's
// homelab lists changed so cached reads revalidate. Announce is best-effort,
// never blocks and never fails the caller.
type changeHintAnnouncer interface {
	Announce(ownerSubject string)
}

// serverEventHintProjector keeps the in-transaction node-lifecycle projection
// and adds the post-commit hint: controlplane calls ServerEventCommitted only
// after the server event's transaction committed.
type serverEventHintProjector struct {
	nodeLifecycleNotificationProjector
	hints changeHintAnnouncer
}

func (p serverEventHintProjector) ServerEventCommitted(result *controlplane.ServerEventResult) {
	if p.hints == nil || !serverEventChangesLists(result) {
		return
	}
	p.hints.Announce(result.Server.OwnerSubjectID)
}

// serverEventChangesLists is false for a plain heartbeat and for an
// observation that only refreshed an unchanged inventory revision.
func serverEventChangesLists(result *controlplane.ServerEventResult) bool {
	if result == nil || !result.Applied || result.Server == nil {
		return false
	}
	return len(result.Transitions) > 0 || (result.Inventory != nil && !result.InventoryRefreshed)
}

// terminalJobHintObserver announces the owner of a terminal job that changed
// services or servers. The job store already accepted the snapshot, so the
// change is committed; the observer never returns an error.
type terminalJobHintObserver struct {
	hints changeHintAnnouncer
}

func (o terminalJobHintObserver) ObserveTerminalJob(_ context.Context, job jobs.JobSnapshot) error {
	if o.hints == nil || !terminalJobChangesLists(job) {
		return nil
	}
	o.hints.Announce(stackKitNotificationString(job, "owner_id"))
	return nil
}

func terminalJobChangesLists(job jobs.JobSnapshot) bool {
	switch job.Type {
	case jobs.JobTypeProvision, jobs.JobTypeDeploy, jobs.JobTypeDestroy, jobs.JobTypeReconcileLease,
		jobs.JobTypeDriftResolve, jobs.JobTypeRemoteEnrollment:
		return true
	case jobs.JobTypeStackKitLifecycle:
		switch strings.ToLower(stackKitNotificationString(job, "operation")) {
		case jobs.StackKitLifecycleApply, jobs.StackKitLifecycleUpgrade, jobs.StackKitLifecycleDriftReconcile,
			jobs.StackKitLifecycleServiceStart, jobs.StackKitLifecycleServiceStop, jobs.StackKitLifecycleServiceRestart,
			jobs.StackKitLifecycleRemove, jobs.StackKitLifecycleAddressBind, jobs.StackKitLifecycleNetworkRebind,
			jobs.StackKitLifecycleAdvancedChangeSet, jobs.StackKitLifecycleRollback,
			jobs.StackKitLifecycleBackupRestoreActivate, jobs.StackKitLifecycleBackupRestoreRecover,
			jobs.StackKitLifecycleAIProvider:
			return true
		}
	}
	return false
}
