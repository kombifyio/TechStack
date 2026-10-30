package routes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/kombifyio/techstack/pkg/serviceauth"

	"github.com/kombifyio/techstack/internal/household"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/logger"
)

// The household routes are the private Cloud to Techstack surface of the
// Companion Feed household: the owner-approved projection of a Cloud household
// member to the owner's homelab, and the Kombify Connect D10 link between a
// Cloud subject and a Pocket ID identity. They sit behind the same Cloud
// service-to-service authentication as the runtime summary and fail closed.
// householdProjectionBudget bounds one owner-approved identity command round
// trip to the node, independent of how long the caller waits.
const householdProjectionBudget = 2 * time.Minute

const (
	householdProjectionPath = "/api/v1/internal/household/projection"
	householdLinksPath      = "/api/v1/internal/household/links"
	householdLinkBeginPath  = "/api/v1/internal/household/links/begin"
	householdLinkDonePath   = "/api/v1/internal/household/links/complete"

	// Scopes the Cloud service token must carry for each route.
	HouseholdProjectionScope = "household:projection"
	HouseholdLinkScope       = "household:link"
	HouseholdLinksReadScope  = "household:links"

	householdBodyLimit = 16 << 10
)

// HouseholdRouteConfig wires the household routes.
type HouseholdRouteConfig struct {
	Service           *household.Service
	ServiceAuthSecret string
	ServiceAuthNext   string
}

var householdSurface = cloudReadSurface{
	errorPrefix: "techstack.household.", unavailableReason: "household_unavailable",
	title: "Household unavailable", body: "Techstack could not process this household request safely.",
}

type householdHandlers struct {
	svc          *household.Service
	secret, next string
}

// RegisterHouseholdInternalRoutes mounts the projection and link routes.
func RegisterHouseholdInternalRoutes(r *httpx.Router, cfg HouseholdRouteConfig) {
	h := householdHandlers{svc: cfg.Service, secret: cfg.ServiceAuthSecret, next: cfg.ServiceAuthNext}
	r.POST(householdProjectionPath, h.projection)
	r.POST(householdLinkBeginPath, h.linkBegin)
	r.POST(householdLinkDonePath, h.linkComplete)
	r.GET(householdLinksPath, h.links)
}

// serve verifies the Cloud service token and its scope before any body is read,
// then runs fn with the verified caller. Inside the servicecall middleware the
// router never sees a returned error, so every branch writes its own response.
func (h householdHandlers) serve(e *httpx.Event, scope string, fn func(e *httpx.Event, caller *servicecall.Caller)) error {
	if !serviceauth.CanVerify(h.secret) || h.svc == nil {
		return householdSurface.deny(e, http.StatusServiceUnavailable, householdSurface.unavailableReason, true,
			"Retry after Techstack service authentication is configured.")
	}
	authCfg, err := serviceauth.Apply(servicecall.Config{
		ServiceName: runtimeSummaryService, Secret: h.secret, SecretNext: h.next,
		AllowedCallers: []string{runtimeSummaryCallerCloud}, Enabled: true,
	})
	if err != nil {
		return householdSurface.deny(e, http.StatusServiceUnavailable, householdSurface.unavailableReason, true,
			"Retry after Techstack service authentication is configured.")
	}
	auth := servicecall.RequireServiceAuth(authCfg)
	auth(servicecall.RequireScope(scope)(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		e.Request = request
		caller := servicecall.FromContext(request.Context())
		if caller == nil || caller.Service != runtimeSummaryCallerCloud {
			_ = householdSurface.deny(e, http.StatusForbidden, "caller_not_allowed", false, "Only kombify Cloud may call this route.")
			return
		}
		e.Response.Header().Set("Cache-Control", "no-store")
		e.Response.Header().Set("Vary", "X-Kombify-Service-Auth")
		fn(e, caller)
	}))).ServeHTTP(e.Response, e.Request)
	return nil
}

func decodeHouseholdBody(e *httpx.Event, into any) bool {
	body, err := io.ReadAll(io.LimitReader(e.Request.Body, householdBodyLimit+1))
	if err != nil || len(body) > householdBodyLimit {
		_ = householdSurface.deny(e, http.StatusRequestEntityTooLarge, "body_too_large", false, "Send a smaller request.")
		return false
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		_ = householdSurface.deny(e, http.StatusBadRequest, "body_invalid", false, "Send a valid JSON body with the documented fields.")
		return false
	}
	return true
}

func onBehalfOf(caller *servicecall.Caller) (sub, org string) {
	if caller == nil || caller.OnBehalfOf == nil {
		return "", ""
	}
	return strings.TrimSpace(caller.OnBehalfOf.Sub), strings.TrimSpace(caller.OnBehalfOf.OrgID)
}

type householdProjectionBody struct {
	OwnerSubject       string `json:"owner_subject"`
	ProjectionRevision string `json:"projection_revision"`
	Member             struct {
		Subject     string `json:"subject"`
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
		// NodeApps are Cloud catalog ids (homelab.app.<id>). Absent or null
		// means all apps; an empty list means none.
		NodeApps *[]string `json:"node_apps"`
	} `json:"member"`
	Action string `json:"action"`
}

func (h householdHandlers) projection(e *httpx.Event) error {
	return h.serve(e, HouseholdProjectionScope, func(e *httpx.Event, caller *servicecall.Caller) {
		var body householdProjectionBody
		if !decodeHouseholdBody(e, &body) {
			return
		}
		body.OwnerSubject = strings.TrimSpace(body.OwnerSubject)
		body.Member.Subject = strings.TrimSpace(body.Member.Subject)
		revision, revisionErr := strconv.ParseUint(body.ProjectionRevision, 10, 63)
		if revisionErr != nil || revision == 0 || strconv.FormatUint(revision, 10) != body.ProjectionRevision {
			_ = householdSurface.deny(e, http.StatusBadRequest, "projection_revision_required", false,
				"Send the positive Cloud owner/member projection generation.")
			return
		}
		if body.OwnerSubject == "" || body.Member.Subject == "" || (body.Action != household.ActionAdd && body.Action != household.ActionRemove) {
			_ = householdSurface.deny(e, http.StatusBadRequest, "projection_invalid", false, "Name the owner, the member and the desired action add or remove.")
			return
		}
		var apps household.AppGrant
		if body.Member.NodeApps != nil {
			parsed, err := household.ParseNodeApps(*body.Member.NodeApps)
			if err != nil {
				_ = householdSurface.deny(e, http.StatusBadRequest, "node_apps_invalid", false, "Send node_apps as homelab.app.<id> catalog ids of grantable apps.")
				return
			}
			apps = household.AppGrant{Set: true, Apps: parsed}
		}
		if body.Action == household.ActionRemove && body.Member.NodeApps != nil {
			_ = householdSurface.deny(e, http.StatusBadRequest, "projection_invalid", false, "A removal carries no node_apps.")
			return
		}
		// The projection runs on behalf of the person who acts: the owner who
		// invites or removes. A member may also remove themselves.
		sub, org := onBehalfOf(caller)
		memberLeaves := body.Action == household.ActionRemove && sub == body.Member.Subject
		if body.Action == household.ActionUpdateApps && sub != body.OwnerSubject {
			_ = householdSurface.deny(e, http.StatusForbidden, "principal_mismatch", false,
				"Only the homelab owner changes the apps of a household member.")
			return
		}
		if sub == "" || (sub != body.OwnerSubject && !memberLeaves) {
			_ = householdSurface.deny(e, http.StatusForbidden, "principal_mismatch", false,
				"Project a household member on behalf of the homelab owner who invites or removes them.")
			return
		}
		// The identity command travels the agent channel and runs on the
		// owner's node; it must not be cancelled midway because the caller
		// stopped waiting. The projection keeps the request's values but not
		// its cancellation, bounded by its own budget, and its outcome is
		// logged so a lost answer can be traced.
		projectCtx, cancelProject := context.WithTimeout(context.WithoutCancel(e.Request.Context()), householdProjectionBudget)
		defer cancelProject()
		started := time.Now()
		result, err := h.svc.Project(projectCtx, household.ProjectionRequest{
			HintTenant: org, OwnerSubject: body.OwnerSubject, Action: body.Action, Apps: apps, Revision: revision,
			Member: household.Member{Subject: body.Member.Subject, Email: body.Member.Email, DisplayName: body.Member.DisplayName},
		})
		logger.Default().Info("household_projection_outcome",
			"request_id", e.Request.Header.Get("X-Request-ID"), "action", string(body.Action),
			"status", string(result.Status), "failed", err != nil,
			"caller_gone", e.Request.Context().Err() != nil, "duration_ms", time.Since(started).Milliseconds())
		if errors.Is(err, household.ErrNodeUnreachable) || errors.Is(err, household.ErrAmbiguousAccount) {
			// The node cannot take the command now. Techstack keeps no queue;
			// Cloud holds the pending projection and re-sends it. The cause is
			// logged (bounded, no member data) so a stopped identity stack or a
			// refused command can be told apart from a disconnected agent.
			cause := err.Error()
			if len(cause) > 300 {
				cause = cause[:300]
			}
			logger.Default().Warn("household_projection_pending",
				"request_id", e.Request.Header.Get("X-Request-ID"),
				"action", string(body.Action), "cause", cause)
			reason := "identity_unavailable"
			if errors.Is(err, household.ErrAmbiguousAccount) && !errors.Is(err, household.ErrNodeUnreachable) {
				reason = "identity_account_ambiguous"
			}
			_ = e.JSON(http.StatusAccepted, map[string]any{
				"status": household.StatusPending, "reason_code": reason, "retryable": true,
			})
			return
		}
		if err != nil {
			h.writeError(e, err)
			return
		}
		out := map[string]any{"status": result.Status, "projection_revision": body.ProjectionRevision}
		if result.StackID != "" {
			out["stack_id"], out["username"], out["link_status"] = result.StackID, result.Username, result.LinkStatus
		}
		if result.ActivationURL != "" {
			// One-time passkey enrolment link: returned once, never stored, for
			// the owner-approved hand-over to the invited person.
			out["activation_url"] = result.ActivationURL
			if result.ExpiresAt != nil {
				out["activation_expires_at"] = result.ExpiresAt.UTC().Format(time.RFC3339)
			}
		}
		if body.Action == household.ActionRemove {
			out["stacks"] = result.RemovedStacks
		}
		_ = e.JSON(http.StatusAccepted, out)
	})
}

type householdLinkBody struct {
	OwnerSubject string `json:"owner_subject"`
	StackID      string `json:"stack_id,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	ReferenceID  string `json:"reference_id,omitempty"`
	Nonce        string `json:"nonce,omitempty"`
}

func (h householdHandlers) linkRequest(e *httpx.Event, caller *servicecall.Caller, body householdLinkBody) (household.LinkRequest, bool) {
	sub, org := onBehalfOf(caller)
	if sub == "" || strings.TrimSpace(body.OwnerSubject) == "" {
		_ = householdSurface.deny(e, http.StatusForbidden, "principal_mismatch", false,
			"Link a household member on behalf of that member and name the homelab owner.")
		return household.LinkRequest{}, false
	}
	return household.LinkRequest{HintTenant: org, OwnerSubject: body.OwnerSubject, MemberSubject: sub, StackID: body.StackID}, true
}

func (h householdHandlers) linkBegin(e *httpx.Event) error {
	return h.serve(e, HouseholdLinkScope, func(e *httpx.Event, caller *servicecall.Caller) {
		var body householdLinkBody
		if !decodeHouseholdBody(e, &body) {
			return
		}
		req, ok := h.linkRequest(e, caller, body)
		if !ok {
			return
		}
		begin, err := h.svc.BeginLink(e.Request.Context(), req)
		if err != nil {
			h.writeError(e, err)
			return
		}
		_ = e.JSON(http.StatusOK, map[string]any{
			"stack_id": begin.StackID, "username": begin.Username, "issuer": begin.Issuer,
			"client_id": begin.ClientID, "scope": begin.Scope, "reference_id": begin.ReferenceID,
		})
	})
}

func (h householdHandlers) linkComplete(e *httpx.Event) error {
	return h.serve(e, HouseholdLinkScope, func(e *httpx.Event, caller *servicecall.Caller) {
		var body householdLinkBody
		if !decodeHouseholdBody(e, &body) {
			return
		}
		req, ok := h.linkRequest(e, caller, body)
		if !ok {
			return
		}
		ref, err := h.svc.CompleteLink(e.Request.Context(), household.LinkProof{LinkRequest: req, IDToken: body.IDToken, Nonce: body.Nonce, ReferenceID: body.ReferenceID})
		if err != nil {
			h.writeError(e, err)
			return
		}
		_ = e.JSON(http.StatusOK, householdRefWire(*ref))
	})
}

func (h householdHandlers) links(e *httpx.Event) error {
	return h.serve(e, HouseholdLinksReadScope, func(e *httpx.Event, caller *servicecall.Caller) {
		owner := strings.TrimSpace(e.Request.URL.Query().Get("owner_subject"))
		if owner == "" || len(e.Request.URL.Query()) != 1 {
			_ = householdSurface.deny(e, http.StatusBadRequest, "owner_subject_required", false, "Send exactly the owner_subject query parameter.")
			return
		}
		// A call on behalf of the owner reads the whole household; on behalf of
		// a member it reads only that member's own reference; a plain service
		// call (no user) reads the household for recipient resolution.
		sub, org := onBehalfOf(caller)
		only := ""
		if sub != "" && sub != owner {
			only = sub
		}
		links, err := h.svc.ListLinks(e.Request.Context(), org, owner, only)
		if err != nil {
			h.writeError(e, err)
			return
		}
		stacks := make([]map[string]any, 0, len(links.Stacks))
		for _, stack := range links.Stacks {
			members := make([]map[string]any, 0, len(stack.Linked))
			for _, ref := range stack.Linked {
				members = append(members, householdRefWire(ref))
			}
			invited := make([]map[string]any, 0, len(stack.Invited))
			for _, ref := range stack.Invited {
				invited = append(invited, householdRefWire(ref))
			}
			stacks = append(stacks, map[string]any{"stack_id": stack.StackID, "members": members, "invited": invited})
		}
		_ = e.JSON(http.StatusOK, map[string]any{"owner_subject": links.OwnerSubject, "stacks": stacks})
	})
}

// householdRefWire is the HomelabIdentityRefV1 form of one reference. It
// carries no e-mail address and no credential.
func householdRefWire(ref controlplane.HomelabIdentityRef) map[string]any {
	out := map[string]any{
		"stack_id": ref.StackID, "idp": ref.IdP, "username": ref.Username, "role": ref.Role,
		"cloud_subject": ref.CloudSubject, "status": ref.Status,
	}
	if ref.LinkedAt != nil {
		out["linked_at"] = ref.LinkedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func (h householdHandlers) writeError(e *httpx.Event, err error) {
	switch {
	case errors.Is(err, household.ErrMemberInvalid):
		_ = householdSurface.deny(e, http.StatusBadRequest, "member_invalid", false, "Send a member with a subject, an e-mail address and a name.")
	case errors.Is(err, household.ErrAppInvalid):
		_ = householdSurface.deny(e, http.StatusBadRequest, "node_apps_invalid", false, "Send node_apps as homelab.app.<id> catalog ids of grantable apps.")
	case errors.Is(err, household.ErrOwnerMember):
		_ = householdSurface.deny(e, http.StatusBadRequest, "owner_not_member", false, "The homelab owner is not a projected household member.")
	case errors.Is(err, household.ErrOwnerNotAuthorized), errors.Is(err, ErrInventoryAccessDenied):
		_ = householdSurface.deny(e, http.StatusForbidden, "owner_access_denied", false, "Open Techstack in the same account and organization to refresh its access context.")
	case errors.Is(err, household.ErrNoReference):
		_ = householdSurface.deny(e, http.StatusNotFound, "no_invitation", false, "The person has no household invitation on this homelab.")
	case errors.Is(err, household.ErrAmbiguousStack):
		_ = householdSurface.deny(e, http.StatusConflict, "stack_id_required", false, "Name the stack to link; the person is invited on several.")
	case errors.Is(err, household.ErrProofInvalid):
		_ = householdSurface.deny(e, http.StatusForbidden, "proof_invalid", false, "Sign in to the homelab's Pocket ID as the invited person and retry.")
	case errors.Is(err, household.ErrIssuerUnavailable):
		_ = householdSurface.deny(e, http.StatusServiceUnavailable, "issuer_unavailable", true, "Retry when the homelab's Pocket ID is reachable.")
	case errors.Is(err, household.ErrNodeUnreachable):
		_ = householdSurface.deny(e, http.StatusServiceUnavailable, "identity_unavailable", true, "Retry when the homelab is connected.")
	case errors.Is(err, controlplane.ErrHouseholdUsernameTaken):
		_ = householdSurface.deny(e, http.StatusConflict, "username_taken", false, "The homelab already has a different person with this username.")
	default:
		logger.Default().Warn("household_request_failed", "route", e.Request.URL.Path, "error", err.Error())
		_ = householdSurface.deny(e, http.StatusServiceUnavailable, householdSurface.unavailableReason, true, "Retry later.")
	}
}
