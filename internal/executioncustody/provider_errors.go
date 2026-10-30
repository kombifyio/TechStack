package executioncustody

import (
	"encoding/json"
	"errors"

	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/internal/providercontroljobs"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/releasechannel"
)

// Only existing public domain contracts cross custody. Raw provider/database
// error strings never become a serialized response or a reconstructed error.
var providerErrorKinds = []struct {
	kind     string
	sentinel error
}{
	{"activation-blocked", providercontrol.ErrMutationActivationBlocked},
	{"decommission-unavailable", providercontroljobs.ErrNativeDecommissionUnavailable},
	{"capacity-unavailable", providercontrol.ErrManagedRuntimeCapacityPolicyUnavailable},
	{"capacity-authority-unavailable", monthlyruntime.ErrCapacityPolicyAuthorityUnavailable},
	{"capacity-exceeded", providercontrol.ErrManagedRuntimeCapacityExceeded},
	{"provider-create-blocked", providercontrol.ErrProviderCreateBlocked},
	{"admission-conflict", providercontrol.ErrNativeAdmissionConflict},
	{"unclassified-custody", providercontrol.ErrManagedRuntimeUnclassifiedCustody},
	{"operation-not-found", providercontrol.ErrOperationNotFound},
	{"power-not-admitted", monthlyruntime.ErrPowerNotAdmitted},
	{"power-in-progress", monthlyruntime.ErrPowerTransitionInProgress},
	{"node-channel-unavailable", monthlyruntime.ErrNodeChannelUnavailable},
	{"tenant-not-owned", releasechannel.ErrTenantNotOwned},
	{"stage-owner-not-admitted", releasechannel.ErrStageOwnerNotAdmitted},
	{"runtime-forbidden", monthlyruntime.ErrForbidden},
	{"enrollment-pending", monthlyruntime.ErrEnrollmentPending},
	{"execution-authority-inactive", monthlyruntime.ErrExecutionAuthorityInactive},
	{"enrollment-failed", jobs.ErrManagedRuntimeEnrollmentFailed},
	{"target-credentials-unavailable", jobs.ErrManagedRuntimeTargetCredentialFailed},
}

func encodeProviderError(reply *providerReply, err error) {
	var pending *providercontroljobs.NativeDecommissionPendingError
	var wait *jobs.JobWaitError
	var denied *monthlyruntime.CapacityPolicyDenialError
	var exceeded *providercontrol.ManagedRuntimeCapacityExceededError
	var protection providercontrol.CreateProtectionError
	var blocked providercontrol.ProviderCreateBlockedError
	switch {
	case errors.As(err, &pending):
		reply.Kind = "decommission-pending"
		if pending.Wait != nil {
			reply.WaitReason = pending.Wait.Reason
			reply.ResumeAfter = pending.Wait.ResumeAfter
		}
	case errors.As(err, &wait):
		reply.Kind = "wait"
		reply.WaitReason = wait.Reason
		reply.ResumeAfter = wait.ResumeAfter
	case errors.As(err, &denied):
		reply.Kind = "capacity-denied"
		reply.Domain, _ = json.Marshal(denied)
	case errors.As(err, &exceeded):
		reply.Kind = "capacity-exceeded-details"
		reply.Domain, _ = json.Marshal(exceeded)
	case errors.As(err, &protection):
		reply.Kind = "create-protection"
		reply.Domain, _ = json.Marshal(protection)
	case errors.As(err, &blocked):
		reply.Kind = "create-blocked-details"
		reply.Domain, _ = json.Marshal(blocked)
	default:
		for _, entry := range providerErrorKinds {
			if errors.Is(err, entry.sentinel) {
				reply.Kind = entry.kind
				return
			}
		}
	}
}

func providerWaitMessage(reason string) string {
	switch reason {
	case jobs.ManagedRuntimeHostPreparationPendingReason:
		return "Waiting for the managed VPS host preparation to finish"
	default:
		return "Waiting for native execution dependency"
	}
}

func decodeProviderError(reply providerReply) error {
	wait := &jobs.JobWaitError{Reason: reply.WaitReason, Message: providerWaitMessage(reply.WaitReason), ResumeAfter: reply.ResumeAfter}
	switch reply.Kind {
	case "decommission-pending":
		return &providercontroljobs.NativeDecommissionPendingError{Wait: wait}
	case "wait":
		return wait
	case "capacity-denied":
		var value monthlyruntime.CapacityPolicyDenialError
		if json.Unmarshal(reply.Domain, &value) == nil {
			return &value
		}
	case "capacity-exceeded-details":
		var value providercontrol.ManagedRuntimeCapacityExceededError
		if json.Unmarshal(reply.Domain, &value) == nil {
			return &value
		}
	case "create-protection":
		var value providercontrol.CreateProtectionError
		if json.Unmarshal(reply.Domain, &value) == nil {
			return value
		}
	case "create-blocked-details":
		var value providercontrol.ProviderCreateBlockedError
		if json.Unmarshal(reply.Domain, &value) == nil {
			return value
		}
	default:
		for _, entry := range providerErrorKinds {
			if reply.Kind == entry.kind {
				return entry.sentinel
			}
		}
	}
	return errors.New("native execution operation denied")
}
