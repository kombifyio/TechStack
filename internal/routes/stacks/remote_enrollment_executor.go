package stacks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/routes/trust"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/discovery"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/remoteguard"
	"github.com/kombifyio/techstack/pkg/specv2"
)

// RemoteEnrollmentExecutor runs the connect-remote Guard enrollment lane for
// durable remote_enrollment jobs. It owns credential custody (wallet), the
// one-time pairing capability and the control-plane origin; the jobs queue
// owns durability, the execution claim, retries and progress projection.
type RemoteEnrollmentExecutor struct {
	handlers wizardRunHandlers
}

// NewRemoteEnrollmentExecutor builds the executor from the same route config
// as the wizard HTTP handlers, so both paths share one custody model.
func NewRemoteEnrollmentExecutor(cfg WizardRunRouteConfig) *RemoteEnrollmentExecutor {
	return &RemoteEnrollmentExecutor{handlers: newWizardRunHandlers(cfg)}
}

// ExecuteRemoteEnrollment re-runs the SSH enrollment for one stack. It is
// idempotent by contract: a repeated call re-mints the capability and re-runs
// the install one-liner against the persisted connection binding.
func (x *RemoteEnrollmentExecutor) ExecuteRemoteEnrollment(
	ctx context.Context,
	req jobs.RemoteEnrollmentRequest,
	progress func(step, message string, percent int),
) (jobs.RemoteEnrollmentOutcome, error) {
	if x == nil {
		return jobs.RemoteEnrollmentOutcome{}, errors.New("remote enrollment executor is not configured")
	}
	h := &x.handlers
	if h.crud.stackStore == nil || h.cfg.Trust.Jobs == nil {
		return jobs.RemoteEnrollmentOutcome{}, errors.New("remote enrollment requires the canonical stack and job stores")
	}
	tenantID := strings.TrimSpace(req.TenantID)
	stackID := strings.TrimSpace(req.StackID)
	if tenantID == "" || stackID == "" {
		return jobs.RemoteEnrollmentOutcome{}, errors.New("remote enrollment requires tenant and stack identity")
	}
	stack, err := h.crud.stackStore.GetStack(ctx, tenantID, stackID)
	if err != nil {
		if errors.Is(err, controlplane.ErrNotFound) {
			// The stack is gone: no retry can change that.
			return jobs.RemoteEnrollmentOutcome{}, fmt.Errorf("remote enrollment stack is unavailable")
		}
		return jobs.RemoteEnrollmentOutcome{}, fmt.Errorf("load stack for remote enrollment: %w", err)
	}
	ownerID := strings.TrimSpace(stack.OwnerSubjectID)
	if ownerID == "" || ownerID != strings.TrimSpace(req.OwnerID) {
		return jobs.RemoteEnrollmentOutcome{}, errors.New("remote enrollment owner binding mismatch")
	}
	mode := strings.ToLower(strings.TrimSpace(runtimeStringFromConfig(stack.Config, "server_provisioning_mode")))
	if mode != specv2.TransportConnectRemote {
		return jobs.RemoteEnrollmentOutcome{}, fmt.Errorf("stack is not configured for connect-remote enrollment")
	}
	origin := strings.TrimSpace(config.PublicOriginFromEnv())
	if origin == "" {
		origin = resolveWizardControlPlaneOrigin(nil)
	}
	if origin == "" {
		return jobs.RemoteEnrollmentOutcome{}, errors.New("control plane origin is not configured for remote enrollment")
	}
	creds, credErr := h.credentialsFromStackConfig(ctx, tenantID, ownerID, stack)
	if credErr != nil {
		// Missing custody is actionable, never retryable.
		return jobs.RemoteEnrollmentOutcome{}, credErr
	}

	plannedServerID := strings.TrimSpace(req.PlannedServerID)
	if specNodeID := joinSpecNodeIDFromStack(stack); specNodeID != "" {
		resolved, ensureErr := h.crud.persistJoinServerIntent(
			ctx, ownerID, tenantID, stack,
			&specv2.Projection{NodeID: specNodeID},
			specv2.TransportConnectRemote,
			runtimeStringFromConfig(stack.Config, "server_remote_host"),
		)
		if ensureErr != nil {
			return jobs.RemoteEnrollmentOutcome{}, fmt.Errorf("reserve the Node inventory for remote enrollment: %w", ensureErr)
		}
		if plannedServerID == "" {
			plannedServerID = resolved
		}
	}

	token, tokenErr := h.remoteEnrollmentPairingToken(ctx, tenantID, req.JobID, ownerID, stack)
	if tokenErr != nil {
		return jobs.RemoteEnrollmentOutcome{}, tokenErr
	}
	enroller := remoteguard.NewEnroller(remoteguard.Config{
		Timeout:  remoteEnrollmentTimeout,
		HostKeys: discovery.NewHostKeyStore(""),
		Workers:  h.cfg.Trust.Workers,
	})
	enrollErr := enroller.Enroll(ctx, tenantID, remoteguard.Request{
		Credentials:     creds,
		ControlPlaneURL: origin,
		PairingToken:    token,
	}, progress)
	if enrollErr != nil {
		reason, message, retryable := remoteguard.ClassifyEnrollmentError(enrollErr)
		if retryable {
			return jobs.RemoteEnrollmentOutcome{}, &jobs.RemoteEnrollmentRetryableError{
				Reason: reason, Message: message, Cause: enrollErr,
			}
		}
		return jobs.RemoteEnrollmentOutcome{}, &jobs.RemoteEnrollmentFailure{
			Reason: reason, Message: message, Cause: enrollErr,
		}
	}
	serverID := h.resolveRemoteEnrollmentServerID(ctx, tenantID, plannedServerID)
	return jobs.RemoteEnrollmentOutcome{
		ServerID: serverID,
		Message:  "Guard enrolled over SSH",
	}, nil
}

// remoteEnrollmentPairingToken reuses the durable row's registration token
// while it is still valid and otherwise mints a fresh token-only capability.
// The token never travels through jobs.payload_json.
func (h wizardRunHandlers) remoteEnrollmentPairingToken(
	ctx context.Context,
	tenantID, jobID, ownerID string,
	stack *controlplane.Stack,
) (string, error) {
	if h.cfg.Trust.Jobs != nil && strings.TrimSpace(jobID) != "" {
		if current, err := h.cfg.Trust.Jobs.GetJob(ctx, tenantID, jobID); err == nil && current != nil {
			token := strings.TrimSpace(stringFromAny(current.Result["registration_token"]))
			if token != "" && pairingTokenResultStillValid(current.Result["token_expires_at"], time.Now().UTC()) {
				return token, nil
			}
		}
	}
	if h.cfg.Trust.Workers == nil {
		return "", errors.New("pairing store is unavailable for remote enrollment")
	}
	params := trust.PairingTokenParams{
		Name:                    strings.TrimSpace(stack.Name + " remote-ssh"),
		StackID:                 stack.ID,
		SpecNodeID:              joinSpecNodeIDFromStack(stack),
		ServerProvisioningMode:  specv2.TransportConnectRemote,
		NodeRole:                remoteEnrollmentNodeRole(stack),
		ServerRemoteHost:        runtimeStringFromConfig(stack.Config, "server_remote_host"),
		ServerRemoteUser:        runtimeStringFromConfig(stack.Config, "server_remote_user"),
		ServerRemoteAuthMethod:  runtimeStringFromConfig(stack.Config, "server_remote_auth_method"),
		ServerRemoteSSHKeyLabel: firstNonEmpty(
			runtimeStringFromConfig(stack.Config, "server_remote_credential_ref"),
			runtimeStringFromConfig(stack.Config, "server_remote_ssh_key_label"),
		),
		ServerRemoteUseSudo: remoteUseSudoFromConfig(stack.Config),
	}
	if rawPort := stack.Config["server_remote_port"]; rawPort != nil {
		switch typed := rawPort.(type) {
		case int:
			params.ServerRemotePort = &typed
		case float64:
			port := int(typed)
			params.ServerRemotePort = &port
		}
	}
	minted, err := trust.MintStackPairingTokenOnly(ctx, h.cfg.Trust, tenantID, ownerID, stack, params)
	if err != nil {
		return "", fmt.Errorf("mint the pairing capability for remote enrollment: %w", err)
	}
	if strings.TrimSpace(minted.Token) == "" {
		return "", errors.New("pairing capability mint returned no token")
	}
	return minted.Token, nil
}

// pairingTokenResultStillValid accepts both the in-memory time.Time and the
// RFC3339 string shape a durable job result round-trips through Postgres.
func pairingTokenResultStillValid(value any, now time.Time) bool {
	switch typed := value.(type) {
	case time.Time:
		return typed.After(now.Add(time.Minute))
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(typed))
		if err != nil {
			return false
		}
		return parsed.After(now.Add(time.Minute))
	default:
		return false
	}
}
