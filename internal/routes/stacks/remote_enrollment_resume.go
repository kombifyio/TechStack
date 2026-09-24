package stacks

import (
	"errors"
	"net/http"
	"strings"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	"github.com/kombifyio/techstack/internal/routes/trust"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/specv2"
)

type resumeRemoteEnrollmentRequest struct {
	PairingJobID string `json:"pairing_job_id"`
	Password     string `json:"password"`
}

// resumeRemoteEnrollment retries connect-remote Guard enrollment using the
// persisted stack connection binding and wallet custody. StackKit rollout is
// intentionally separate: this endpoint only re-runs the SSH connection lane.
func (h wizardRunHandlers) resumeRemoteEnrollment(e *httpx.Event) error {
	ownerID, authErr := requireStackAuth(e)
	if authErr != nil {
		return authErr
	}
	tenantID, tenantErr := tenantguard.TenantScope(tenantIDFromRequest(e), ownerID, "techstack.stacks.resume-remote-enrollment")
	if tenantErr != nil {
		return tenantErr
	}
	stackID := strings.TrimSpace(e.Request.PathValue("id"))
	if stackID == "" {
		return httpx.BadRequest(e, "stack id is required", nil)
	}
	if h.crud.stackStore == nil || h.cfg.Trust.Workers == nil || h.cfg.Trust.Jobs == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Remote SSH enrollment recovery is unavailable", wizardRunDetails(
				"remote_enrollment_resume_unavailable", true,
				"Recovery unavailable",
				"Retry when the control plane and pairing store are available.",
			))
	}
	stack, err := h.crud.stackStore.GetStack(e.Request.Context(), tenantID, stackID)
	if err != nil {
		if errors.Is(err, controlplane.ErrNotFound) {
			return httpx.NotFound(e, "stack not found")
		}
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to load stack", nil)
	}
	if strings.TrimSpace(stack.OwnerSubjectID) != ownerID {
		return httpx.NotFound(e, "stack not found")
	}
	mode := strings.ToLower(strings.TrimSpace(runtimeStringFromConfig(stack.Config, "server_provisioning_mode")))
	if mode != specv2.TransportConnectRemote {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"This stack is not configured for connect-remote enrollment", wizardRunDetails(
				"remote_enrollment_resume_wrong_mode", false,
				"Wrong connection mode",
				"Only connect-remote stacks can resume SSH enrollment on the persisted connection.",
			))
	}

	var req resumeRemoteEnrollmentRequest
	if decodeErr := decodeStrictJSONBody(e.Request.Body, &req); decodeErr != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest, "Invalid remote enrollment resume request", map[string]any{
			detailsKeyReasonCode: "invalid_remote_enrollment_resume_body",
		})
	}

	run := &wizardRunState{
		tenantID: tenantID,
		ownerID:  ownerID,
	}
	if password := strings.TrimSpace(req.Password); password != "" {
		remote := &wizardRunRemoteParams{
			Host:       runtimeStringFromConfig(stack.Config, "server_remote_host"),
			User:       runtimeStringFromConfig(stack.Config, "server_remote_user"),
			AuthMethod: "password",
			Password:   password,
			UseSudo:    remoteUseSudoFromConfig(stack.Config),
		}
		if rawPort := stack.Config["server_remote_port"]; rawPort != nil {
			switch typed := rawPort.(type) {
			case int:
				remote.Port = &typed
			case float64:
				port := int(typed)
				remote.Port = &port
			}
		}
		run.request.Remote = remote
		if err := h.persistWizardRemoteConnectionBinding(e.Request.Context(), run, stackID); err != nil {
			return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation,
				"Could not refresh the persisted SSH password", wizardRunDetails(
					"remote_enrollment_password_persist_failed", true,
					"Password not saved",
					err.Error(),
				))
		}
	}

	if _, credErr := h.resolveWizardRemoteCredentials(e.Request.Context(), run, stackID); credErr != nil {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"Persisted remote SSH credentials are unavailable", wizardRunDetails(
				"remote_enrollment_credentials_missing", true,
				"Connection credentials missing",
				credErr.Error(),
			))
	}

	nodeRole := remoteEnrollmentNodeRole(stack)
	specNodeID := joinSpecNodeIDFromStack(stack)
	if specNodeID == "" {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"The joined Node projection is unavailable for remote enrollment recovery", wizardRunDetails(
				"remote_enrollment_join_projection_missing", true,
				"Joined Node unavailable",
				"Reload the deployment and retry adding the Node before resuming SSH enrollment.",
			))
	}
	plannedServerID, ensureErr := h.crud.persistJoinServerIntent(
		e.Request.Context(),
		run.ownerID,
		run.tenantID,
		stack,
		&specv2.Projection{NodeID: specNodeID},
		specv2.TransportConnectRemote,
		runtimeStringFromConfig(stack.Config, "server_remote_host"),
	)
	if ensureErr != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"Failed to reserve the Node inventory for remote enrollment", wizardRunDetails(
				"remote_enrollment_server_intent_failed", true,
				"Node inventory unavailable",
				"Retry when the canonical server inventory is available.",
			))
	}
	minted, handled := h.mintRemoteEnrollmentPairing(e, run, stack, nodeRole, specNodeID)
	if handled {
		return nil
	}
	h.startWizardRemoteEnrollment(e, run, stackID, minted, plannedServerID)

	return httpx.Success(e, http.StatusAccepted, map[string]any{
		recoveryResponseSuccessKey:         true,
		specResponseMessageKey:             "Remote SSH enrollment retry accepted",
		recoveryResponseKitDeploymentIDKey: stackID,
		"pairing_job_id":                   minted.JobID,
		wizardRunPlannedServerID:           plannedServerID,
		"server_remote_host":        runtimeStringFromConfig(stack.Config, "server_remote_host"),
		"server_remote_user":        runtimeStringFromConfig(stack.Config, "server_remote_user"),
		"server_remote_port":        stack.Config["server_remote_port"],
		"server_remote_auth_method": runtimeStringFromConfig(stack.Config, "server_remote_auth_method"),
		"server_remote_credential_ref": firstNonEmpty(
			runtimeStringFromConfig(stack.Config, "server_remote_credential_ref"),
			runtimeStringFromConfig(stack.Config, "server_remote_ssh_key_label"),
		),
	})
}

func remoteEnrollmentNodeRole(stack *controlplane.Stack) string {
	if stack == nil {
		return "foundation"
	}
	spec, ok := stack.Config[stackConfigKeySpecV2].(map[string]any)
	if !ok {
		return "foundation"
	}
	nodes, ok := spec["nodes"].([]any)
	if !ok || len(nodes) == 0 {
		return "foundation"
	}
	last, ok := nodes[len(nodes)-1].(map[string]any)
	if !ok {
		return "foundation"
	}
	roles, ok := last["roles"].([]any)
	if !ok || len(roles) == 0 {
		return "foundation"
	}
	return specv2.NormalizeRole(strings.TrimSpace(stringFromAny(roles[0])))
}

func (h wizardRunHandlers) mintRemoteEnrollmentPairing(e *httpx.Event, run *wizardRunState, stack *controlplane.Stack, nodeRole, specNodeID string) (trust.MintedStackPairing, bool) {
	params := trust.PairingTokenParams{
		Name:                    strings.TrimSpace(stack.Name + " remote-ssh"),
		StackID:                 stack.ID,
		SpecNodeID:              specNodeID,
		ServerProvisioningMode:  specv2.TransportConnectRemote,
		NodeRole:                nodeRole,
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
	minted, err := trust.MintStackPairingToken(e.Request.Context(), h.cfg.Trust, run.tenantID, run.ownerID, stack, params)
	if err != nil || minted.JobID == "" {
		_ = httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"Failed to prepare remote SSH enrollment", wizardRunDetails(
				"remote_enrollment_pairing_failed", true,
				"Enrollment not prepared",
				"Retry when the pairing store is available.",
			))
		return minted, true
	}
	return minted, false
}
