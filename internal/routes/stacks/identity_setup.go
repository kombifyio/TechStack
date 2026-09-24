package stacks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/specv2"
	"golang.org/x/sync/errgroup"
)

const identityCapabilityPending = "identity_runtime_capability_pending"

type orchestratorWizardIdentityRuntime struct{ orch *orchestrator.Orchestrator }

type stackKitIdentityEnvelope struct {
	Data json.RawMessage `json:"data"`
}

type stackKitOwnerActivationData struct {
	Status        string `json:"status"`
	Origin        string `json:"origin"`
	ExpiresAt     string `json:"expiresAt"`
	ActivationURL string `json:"activationURL"`
}

type stackKitHouseholdListData struct {
	Users []struct {
		Username    string `json:"username"`
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
		Status      string `json:"status"`
	} `json:"users"`
}

func (runtime orchestratorWizardIdentityRuntime) Snapshot(ctx context.Context, request WizardIdentityRuntimeRequest) (WizardIdentitySnapshot, error) {
	var snapshot WizardIdentitySnapshot
	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error {
		result, err := runtime.run(groupCtx, request, orchestrator.StackKitIdentityCommandRequest{Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_OWNER_ACTIVATION_STATUS})
		if err != nil {
			return err
		}
		snapshot.Owner, err = decodeOwnerActivation(result.CommandResultJson)
		return err
	})
	group.Go(func() error {
		result, err := runtime.run(groupCtx, request, orchestrator.StackKitIdentityCommandRequest{Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_LIST})
		if err != nil {
			return err
		}
		snapshot.Members, err = decodeHouseholdMembers(result.CommandResultJson)
		return err
	})
	if err := group.Wait(); err != nil {
		return WizardIdentitySnapshot{}, err
	}
	return snapshot, nil
}

func (runtime orchestratorWizardIdentityRuntime) IssueOwnerActivation(ctx context.Context, request WizardIdentityRuntimeRequest) (WizardOwnerActivation, error) {
	result, err := runtime.run(ctx, request, orchestrator.StackKitIdentityCommandRequest{
		Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_OWNER_ACTIVATION_ISSUE, OwnerApproved: true,
	})
	if err != nil {
		return WizardOwnerActivation{}, err
	}
	raw := append([]byte(nil), result.SensitiveResultJson...)
	clear(result.SensitiveResultJson)
	result.SensitiveResultJson = nil
	return decodeOwnerActivation(raw)
}

func (runtime orchestratorWizardIdentityRuntime) InviteHouseholdMember(ctx context.Context, request WizardHouseholdInvitationRequest) (WizardHouseholdInvitation, error) {
	result, err := runtime.run(ctx, request.WizardIdentityRuntimeRequest, orchestrator.StackKitIdentityCommandRequest{
		Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_INVITE, OwnerApproved: request.OwnerApproved,
		HouseholdUsername: request.Username, HouseholdEmail: request.Email, HouseholdDisplayName: request.DisplayName,
	})
	if err != nil {
		return WizardHouseholdInvitation{}, err
	}
	raw := append([]byte(nil), result.SensitiveResultJson...)
	clear(result.SensitiveResultJson)
	result.SensitiveResultJson = nil
	var envelope stackKitIdentityEnvelope
	var data struct {
		Username, Email, DisplayName, Status, ExpiresAt, SetupURL string
	}
	if json.Unmarshal(raw, &envelope) != nil || json.Unmarshal(envelope.Data, &data) != nil {
		return WizardHouseholdInvitation{}, errors.New("decode StackKits household invitation")
	}
	invitation := WizardHouseholdInvitation{ClientRef: request.ClientRef, Username: data.Username, Email: data.Email, DisplayName: data.DisplayName, Status: data.Status, ActivationURL: data.SetupURL}
	if invitation.Status == "" {
		invitation.Status = "pending"
	}
	invitation.ExpiresAt = parseIdentityTime(data.ExpiresAt)
	return invitation, nil
}

func (runtime orchestratorWizardIdentityRuntime) run(ctx context.Context, request WizardIdentityRuntimeRequest, command orchestrator.StackKitIdentityCommandRequest) (*agentpb.StackKitResult, error) {
	command.TenantID, command.OwnerID, command.DeploymentID = request.TenantID, request.OwnerID, request.DeploymentID
	return runtime.orch.RunStackKitIdentityCommand(ctx, command)
}

func decodeOwnerActivation(raw []byte) (WizardOwnerActivation, error) {
	var envelope stackKitIdentityEnvelope
	var data stackKitOwnerActivationData
	if json.Unmarshal(raw, &envelope) != nil || json.Unmarshal(envelope.Data, &data) != nil || strings.TrimSpace(data.Status) == "" {
		return WizardOwnerActivation{}, errors.New("decode StackKits owner activation")
	}
	return WizardOwnerActivation{Status: data.Status, Origin: data.Origin, ExpiresAt: parseIdentityTime(data.ExpiresAt), ActivationURL: data.ActivationURL}, nil
}

func decodeHouseholdMembers(raw []byte) ([]WizardHouseholdMember, error) {
	var envelope stackKitIdentityEnvelope
	var data stackKitHouseholdListData
	if json.Unmarshal(raw, &envelope) != nil || json.Unmarshal(envelope.Data, &data) != nil {
		return nil, errors.New("decode StackKits household members")
	}
	members := make([]WizardHouseholdMember, 0, len(data.Users))
	for _, user := range data.Users {
		status := strings.TrimSpace(user.Status)
		if status == "" {
			status = "pending"
		}
		members = append(members, WizardHouseholdMember{Username: user.Username, Email: user.Email, DisplayName: user.DisplayName, Status: status})
	}
	return members, nil
}

func parseIdentityTime(value string) *time.Time {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	parsed = parsed.UTC()
	return &parsed
}

type WizardIdentityRuntime interface {
	Snapshot(context.Context, WizardIdentityRuntimeRequest) (WizardIdentitySnapshot, error)
	IssueOwnerActivation(context.Context, WizardIdentityRuntimeRequest) (WizardOwnerActivation, error)
	InviteHouseholdMember(context.Context, WizardHouseholdInvitationRequest) (WizardHouseholdInvitation, error)
}

type WizardIdentityRuntimeRequest struct {
	TenantID     string
	OwnerID      string
	DeploymentID string
}

type WizardIdentitySnapshot struct {
	Owner   WizardOwnerActivation
	Members []WizardHouseholdMember
}

type WizardOwnerActivation struct {
	Status        string
	Origin        string
	ExpiresAt     *time.Time
	ActivationURL string
	ReasonCode    string
}

type WizardHouseholdMember struct {
	Username    string `json:"username"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name,omitempty"`
	Status      string `json:"status"`
}

type WizardHouseholdInvitationRequest struct {
	WizardIdentityRuntimeRequest
	ClientRef     string
	Username      string
	Email         string
	DisplayName   string
	OwnerApproved bool
}

type WizardHouseholdInvitation struct {
	ClientRef     string
	Username      string
	Email         string
	DisplayName   string
	Status        string
	ExpiresAt     *time.Time
	ActivationURL string
}

type ownerActivationRequest struct {
	OwnerApproved bool `json:"owner_approved"`
}

type householdInvitationBody struct {
	ClientRef     string `json:"client_ref"`
	Username      string `json:"username"`
	Email         string `json:"email"`
	DisplayName   string `json:"display_name,omitempty"`
	OwnerApproved bool   `json:"owner_approved"`
}

func (h wizardRunHandlers) getStackIdentity(e *httpx.Event) error {
	request, stack, err := h.authorizeIdentityStack(e, "techstack.stacks.identity.read")
	if err != nil {
		return err
	}
	response := h.identityResponse(e.Request.Context(), request, stack, WizardIdentitySnapshot{})
	if h.cfg.IdentityRuntime != nil {
		snapshot, err := h.cfg.IdentityRuntime.Snapshot(e.Request.Context(), request)
		if err == nil {
			response = h.identityResponse(e.Request.Context(), request, stack, snapshot)
		} else {
			response["owner"] = ownerActivationWire(WizardOwnerActivation{Status: "failed", ReasonCode: "identity_status_failed"}, false)
		}
	}
	e.Response.Header().Set("Cache-Control", "no-store")
	return httpx.Success(e, http.StatusOK, response)
}

func (h wizardRunHandlers) issueOwnerActivation(e *httpx.Event) error {
	request, _, err := h.authorizeIdentityStack(e, "techstack.stacks.identity.owner-activation")
	if err != nil {
		return err
	}
	var body ownerActivationRequest
	if decodeStrictJSONBody(e.Request.Body, &body) != nil || !body.OwnerApproved {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "Explicit owner approval is required", map[string]any{"reason_code": "owner_approval_required"})
	}
	if h.cfg.IdentityRuntime == nil {
		return identityCapabilityUnavailable(e)
	}
	activation, err := h.cfg.IdentityRuntime.IssueOwnerActivation(e.Request.Context(), request)
	if err != nil {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Owner activation is unavailable", map[string]any{"reason_code": "owner_activation_failed", "retryable": true})
	}
	e.Response.Header().Set("Cache-Control", "no-store")
	return httpx.Success(e, http.StatusOK, map[string]any{"owner": ownerActivationWire(activation, true)})
}

func (h wizardRunHandlers) createHouseholdInvitation(e *httpx.Event) error {
	request, stack, err := h.authorizeIdentityStack(e, "techstack.stacks.identity.household-invite")
	if err != nil {
		return err
	}
	var body householdInvitationBody
	if decodeStrictJSONBody(e.Request.Body, &body) != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "Invalid household invitation", map[string]any{"reason_code": "household_invitation_invalid"})
	}
	body.ClientRef = strings.TrimSpace(body.ClientRef)
	body.Username = strings.TrimSpace(body.Username)
	body.Email = strings.TrimSpace(body.Email)
	body.DisplayName = strings.TrimSpace(body.DisplayName)
	if !body.OwnerApproved || body.ClientRef == "" || body.Username == "" || body.Email == "" {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "Household invitation requires owner approval and one complete person", map[string]any{"reason_code": "household_invitation_invalid"})
	}
	planned, ok := h.plannedHouseholdPerson(e.Request.Context(), request, stack, body.ClientRef)
	if !ok || !strings.EqualFold(planned.Email, body.Email) || (planned.Name != "" && planned.Name != body.DisplayName) {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Household invitation does not match the persisted Wizard intent", map[string]any{"reason_code": "household_invitation_intent_mismatch", "retryable": false})
	}
	if h.cfg.IdentityRuntime == nil {
		return identityCapabilityUnavailable(e)
	}
	snapshot, err := h.cfg.IdentityRuntime.Snapshot(e.Request.Context(), request)
	if err != nil || snapshot.Owner.Status != "active" {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Activate the owner before inviting household members", map[string]any{"reason_code": "owner_activation_required", "retryable": true})
	}
	invitation, err := h.cfg.IdentityRuntime.InviteHouseholdMember(e.Request.Context(), WizardHouseholdInvitationRequest{
		WizardIdentityRuntimeRequest: request, ClientRef: body.ClientRef, Username: body.Username,
		Email: body.Email, DisplayName: body.DisplayName, OwnerApproved: true,
	})
	if err != nil {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Household invitation failed", map[string]any{"reason_code": "household_invitation_failed", "retryable": true})
	}
	e.Response.Header().Set("Cache-Control", "no-store")
	return httpx.Success(e, http.StatusCreated, map[string]any{"invitation": householdInvitationWire(invitation)})
}

func (h wizardRunHandlers) plannedHouseholdPerson(ctx context.Context, request WizardIdentityRuntimeRequest, stack *controlplane.Stack, clientRef string) (specv2.PlannedPersonIntent, bool) {
	if h.crud.homelabStore == nil || stack == nil || stack.HomelabID == "" {
		return specv2.PlannedPersonIntent{}, false
	}
	homelab, err := h.crud.homelabStore.GetHomelabByOwner(ctx, request.TenantID, request.OwnerID)
	if err != nil || homelab == nil || homelab.ID != stack.HomelabID {
		return specv2.PlannedPersonIntent{}, false
	}
	for _, person := range householdIntentFromHomelab(homelab).PlannedPeople {
		if person.ClientRef == clientRef {
			return person, true
		}
	}
	return specv2.PlannedPersonIntent{}, false
}

func (h wizardRunHandlers) authorizeIdentityStack(e *httpx.Event, action string) (WizardIdentityRuntimeRequest, *controlplane.Stack, error) {
	ownerID, err := requireStackAuth(e)
	if err != nil {
		return WizardIdentityRuntimeRequest{}, nil, err
	}
	tenantID, err := tenantguard.TenantScope(tenantIDFromRequest(e), ownerID, action)
	if err != nil {
		return WizardIdentityRuntimeRequest{}, nil, err
	}
	stackID := strings.TrimSpace(e.Request.PathValue("id"))
	if stackID == "" || h.crud.stackStore == nil {
		return WizardIdentityRuntimeRequest{}, nil, httpx.NewNotFoundError("Stack not found", nil)
	}
	stack, err := h.crud.stackStore.GetStack(e.Request.Context(), tenantID, stackID)
	if err != nil || stack == nil || stack.OwnerSubjectID != ownerID {
		return WizardIdentityRuntimeRequest{}, nil, httpx.NewNotFoundError("Stack not found", nil)
	}
	return WizardIdentityRuntimeRequest{TenantID: tenantID, OwnerID: ownerID, DeploymentID: stackID}, stack, nil
}

func (h wizardRunHandlers) identityResponse(ctx context.Context, request WizardIdentityRuntimeRequest, stack *controlplane.Stack, snapshot WizardIdentitySnapshot) map[string]any {
	owner := snapshot.Owner
	if h.cfg.IdentityRuntime == nil {
		owner = WizardOwnerActivation{Status: "unavailable", ReasonCode: identityCapabilityPending}
	}
	household := map[string]any{"profile": specv2.HouseholdProfileSolo, "planned_people": []any{}, "members": snapshot.Members}
	if h.crud.homelabStore != nil && stack != nil && stack.HomelabID != "" {
		if homelab, err := h.crud.homelabStore.GetHomelabByOwner(ctx, request.TenantID, request.OwnerID); err == nil && homelab != nil && homelab.ID == stack.HomelabID {
			if planned := householdIntentFromHomelab(homelab); planned.Profile != "" {
				household["profile"] = planned.Profile
				people := make([]map[string]any, 0, len(planned.PlannedPeople))
				for _, person := range planned.PlannedPeople {
					people = append(people, map[string]any{"client_ref": person.ClientRef, "name": person.Name, "email": person.Email, "status": "draft"})
				}
				household["planned_people"] = people
			}
		}
	}
	return map[string]any{"owner": ownerActivationWire(owner, false), "household": household}
}

func householdIntentFromHomelab(homelab *controlplane.Homelab) specv2.HouseholdIntent {
	if homelab == nil {
		return specv2.HouseholdIntent{}
	}
	wizard, _ := homelab.Intent["wizard"].(map[string]any)
	raw, ok := wizard["household"]
	if !ok {
		return specv2.HouseholdIntent{}
	}
	data, _ := json.Marshal(raw)
	var intent specv2.HouseholdIntent
	_ = json.Unmarshal(data, &intent)
	return intent
}

func ownerActivationWire(value WizardOwnerActivation, includeURL bool) map[string]any {
	status := strings.TrimSpace(value.Status)
	if status == "" {
		status = "unavailable"
	}
	out := map[string]any{"status": status}
	if value.Origin != "" {
		out["origin"] = value.Origin
	}
	if value.ExpiresAt != nil {
		out["expires_at"] = value.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if value.ReasonCode != "" {
		out["reason_code"] = value.ReasonCode
	}
	if includeURL && value.ActivationURL != "" {
		out["activation_url"] = value.ActivationURL
	}
	return out
}

func householdInvitationWire(value WizardHouseholdInvitation) map[string]any {
	out := map[string]any{
		"client_ref": value.ClientRef, "username": value.Username, "email": value.Email,
		"display_name": value.DisplayName, "status": value.Status,
	}
	if value.ExpiresAt != nil {
		out["expires_at"] = value.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if value.ActivationURL != "" {
		out["activation_url"] = value.ActivationURL
	}
	return out
}

func identityCapabilityUnavailable(e *httpx.Event) error {
	return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Identity activation requires a compatible pinned StackKits runtime", map[string]any{
		"reason_code": identityCapabilityPending, "retryable": true,
	})
}
