package executionchannel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/kombifyio/techstack/internal/guardbootstrap"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
)

// PrepareManagedRuntimeTarget observes the existing first-boot baseline on the
// exact stored server. It neither provisions a host nor accepts a host address,
// shell command, private key, local path or installation instruction from HTTP.
func (c *Channel) PrepareManagedRuntimeTarget(ctx context.Context, request jobs.ManagedRuntimeTargetRequest) (*jobs.RuntimeTargetBootstrapResult, error) {
	started := time.Now()
	if c == nil || c.Targets == nil || strings.TrimSpace(request.LeaseID) == "" {
		return nil, jobs.ErrManagedRuntimeTargetCredentialFailed
	}
	server, err := c.leaseServer(ctx, request.TenantID, request.LeaseID)
	if err != nil {
		return nil, err
	}
	if request.StackID == "" || server.StackID != request.StackID || !serverOwnedBy(*server, request.OwnerID) {
		return nil, monthlyruntime.ErrForbidden
	}
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	target, fingerprint, err := c.ResolveTarget(probeCtx, *server, server.OwnerSubjectID)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, ctx.Err()
		}
		if errors.Is(err, jobs.ErrManagedRuntimeEnrollmentFailed) ||
			errors.Is(err, monthlyruntime.ErrForbidden) ||
			errors.Is(err, monthlyruntime.ErrExecutionAuthorityInactive) ||
			errors.Is(err, monthlyruntime.ErrInvalidLease) ||
			errors.Is(err, monthlyruntime.ErrRuntimeVMGone) ||
			errors.Is(err, monthlyruntime.ErrFeatureDisabled) {
			// An observed failed enrollment or refused authority cannot be
			// repaired by first-boot SSH convergence.
			return hostPreparationFailed(jobs.RuntimeTargetBootstrapUnknown, "execution_custody", started)
		}
		// cloud-init admits Guard before the asynchronous host-prep unit
		// installs its execution user and Guard reports the host-key inventory.
		// Missing custody and resolver timeouts are pending inside the same
		// persisted four-minute window, never an unbounded retry or a pass.
		return nil, hostPreparationPending()
	}
	output, err := c.runCommand(probeCtx, target, fingerprint, "sudo -n /bin/bash -s", guardbootstrap.RuntimeHostReadinessScript())
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, ctx.Err()
		}
		if check := guardbootstrap.RuntimeHostReadinessFailureCheck(output); check != "" {
			reason := jobs.RuntimeTargetBootstrapUnknown
			if check == "docker" {
				reason = jobs.RuntimeTargetBootstrapDockerFailed
			}
			return hostPreparationFailed(reason, check, started)
		}
		var exit *ssh.ExitError
		if errors.As(err, &exit) {
			if exit.ExitStatus() == 75 {
				return nil, hostPreparationPending()
			}
			if exit.ExitStatus() == 78 {
				return hostPreparationFailed(jobs.RuntimeTargetBootstrapUnknown, "host_preparation_failed", started)
			}
			return nil, hostPreparationPending()
		}
		text := strings.ToLower(err.Error())
		if strings.Contains(text, "host key mismatch") || strings.Contains(text, "host key custody changed") {
			return hostPreparationFailed(jobs.RuntimeTargetBootstrapUnknown, "pinned_host_key", started)
		}
		// First-boot SSH authentication, sshd reload EOFs and transport gaps
		// can precede execution-user provisioning. The queue's durable window
		// turns continued pending into timeout; only observed terminal prep
		// failures and actual pin violations fail immediately above.
		return nil, hostPreparationPending()
	}
	return &jobs.RuntimeTargetBootstrapResult{Status: "ready", ReasonCode: jobs.RuntimeTargetBootstrapReady, Message: "Cloud-init host preparation v2 and the live canonical baseline are ready", Output: "host_prep_record=/var/lib/kombify/host-prep/v2.status\nhost_prep_status=ready\nbaseline=verified\nexecution_channel=pinned", DurationMS: time.Since(started).Milliseconds()}, nil
}

func hostPreparationPending() error {
	return &jobs.JobWaitError{Reason: jobs.ManagedRuntimeHostPreparationPendingReason, Message: "The managed host's first-boot baseline is not ready yet", ResumeAfter: 15 * time.Second}
}

func hostPreparationFailed(reason, check string, started time.Time) (*jobs.RuntimeTargetBootstrapResult, error) {
	// check is selected solely by this package or the canonical baseline allowlist.
	message := "Managed host readiness failed: " + check
	return &jobs.RuntimeTargetBootstrapResult{Status: "failed", ReasonCode: reason, Message: message, Output: "failed_check=" + check, DurationMS: time.Since(started).Milliseconds()}, fmt.Errorf("%w: %s", monthlyruntime.ErrNodeChannelUnavailable, message)
}
