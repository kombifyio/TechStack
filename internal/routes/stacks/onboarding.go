// The onboarding journey surface (ONBOARDING-JOURNEY-STANDARD §7, §8).
//
// The server owns the descriptor, the persisted state, the derived completion
// signals and the availability decision; the client owns the render model and
// resolves it with @kombiverselabs/onboarding-core. That split is why GET
// returns the descriptor verbatim instead of a pre-rendered checklist: two
// implementations of "what does the user see" would drift, one cannot.
package stacks

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/kombifyio/techstack/internal/onboarding"
	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
)

const (
	maxOnboardingBodyBytes = 16 << 10

	onboardingCapability = "techstack.onboarding"

	// onboardingErrorDerivedOnly marks a client trying to report itself
	// through a step that spends money.
	onboardingErrorDerivedOnly = "completion.derived_only"
	// onboardingErrorRevisionConflict marks a failed compare-and-swap.
	onboardingErrorRevisionConflict = "revision.conflict"
	// onboardingErrorNoControlPlane marks the PocketBase-only lane, where
	// there is nowhere to persist a journey.
	onboardingErrorNoControlPlane = "onboarding_requires_control_plane"

	// Envelope keys shared with the managed-runtime denials
	// (pkg/monthlyruntime/service.go) so one client shape parses every refusal.
	detailsKeyErrorCode = "error_code"
	guidanceKeyNextStep = "next_steps"
)

// OnboardingRouteConfig wires the journey surface. Features and DeploymentMode
// feed the availability decision — the same entitlement chain the create path
// runs, so the card and the denial can never disagree.
type OnboardingRouteConfig struct {
	DeploymentMode    config.DeploymentMode
	Features          onboarding.FeatureChecker
	DefaultTenantID   string
	ServiceAuthSecret string
	ServiceAuthNext   string
}

type onboardingHandlers struct {
	service           onboarding.Service
	defaultTenantID   string
	available         bool
	serviceAuthSecret string
	serviceAuthNext   string
}

// RegisterOnboardingRoutes registers the journey read and action endpoints.
// Without a control-plane store the surface fails closed with one explicit
// reason rather than silently pretending the journey is empty.
func RegisterOnboardingRoutes(r *httpx.Router, cfg OnboardingRouteConfig) {
	if !cfg.DeploymentMode.IsValid() {
		cfg.DeploymentMode = config.ModeSelfHosted
	}
	stores := currentControlPlaneStores()
	h := onboardingHandlers{
		service: onboarding.Service{
			Store: stores.Onboarding,
			Signals: onboarding.SignalSources{
				Homelabs:       stores.Homelabs,
				KitDeployments: stores.Stacks,
				Servers:        stores.Servers,
				Services:       stores.Services,
				Onboarding:     stores.Onboarding,
			},
			DeploymentMode: cfg.DeploymentMode,
			Features:       cfg.Features,
		},
		defaultTenantID:   strings.TrimSpace(cfg.DefaultTenantID),
		available:         stores.Onboarding != nil,
		serviceAuthSecret: strings.TrimSpace(cfg.ServiceAuthSecret),
		serviceAuthNext:   strings.TrimSpace(cfg.ServiceAuthNext),
	}
	r.GET("/api/v1/onboarding/{journeyId}", h.getJourney)
	r.PUT("/api/v1/onboarding/{journeyId}", h.applyAction)
	r.POST("/api/v1/onboarding/{journeyId}/reset", h.resetJourney)
	r.GET("/api/v1/internal/onboarding/facts/{authority}", h.getOwnerFacts)
}

func (h onboardingHandlers) getJourney(e *httpx.Event) error {
	journeyID, principal, err := h.scope(e)
	if err != nil {
		return err
	}
	result, err := h.service.Get(e.Request.Context(), journeyID, principal)
	if err != nil {
		return h.writeError(e, err)
	}
	return httpx.Success(e, http.StatusOK, result)
}

// onboardingActionRequest is the closed wire contract of one action. Unknown
// fields are rejected: a client must not be able to push a whole state blob
// through an endpoint that only accepts transitions.
type onboardingActionRequest struct {
	Action string `json:"action"`
	StepID string `json:"step_id"`
	Source string `json:"source"`
}

func (h onboardingHandlers) applyAction(e *httpx.Event) error {
	journeyID, principal, err := h.scope(e)
	if err != nil {
		return err
	}

	var req onboardingActionRequest
	decoder := json.NewDecoder(io.LimitReader(e.Request.Body, maxOnboardingBodyBytes))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&req); decodeErr != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest,
			"Onboarding action payload is not valid", map[string]any{
				detailsKeyReasonCode: "onboarding_payload_invalid",
				detailsKeyRetryable:  false,
			})
	}

	expectRevision, err := onboardingExpectedRevision(e)
	if err != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest,
			"If-Match must be a revision number", map[string]any{
				detailsKeyReasonCode: "onboarding_if_match_invalid",
				detailsKeyRetryable:  false,
			})
	}

	result, applyErr := h.service.Apply(e.Request.Context(), journeyID, principal,
		onboarding.Action(strings.TrimSpace(req.Action)), req.StepID, strings.TrimSpace(req.Source), expectRevision)
	if applyErr != nil {
		return h.writeError(e, applyErr)
	}
	return httpx.Success(e, http.StatusOK, result)
}

// resetJourney is its own verb because re-entry is a product decision, not an
// action among others: the user asked to see the tour again.
func (h onboardingHandlers) resetJourney(e *httpx.Event) error {
	journeyID, principal, err := h.scope(e)
	if err != nil {
		return err
	}
	result, applyErr := h.service.Apply(e.Request.Context(), journeyID, principal, onboarding.ActionReset, "", "", nil)
	if applyErr != nil {
		return h.writeError(e, applyErr)
	}
	return httpx.Success(e, http.StatusOK, result)
}

// scope authenticates the caller and resolves the tenant. Self-host sessions
// carry no org claim, so they fall back to the configured tenant instead of
// failing closed on every request.
func (h onboardingHandlers) scope(e *httpx.Event) (string, onboarding.Principal, error) {
	ownerID, err := requireStackAuth(e)
	if err != nil {
		return "", onboarding.Principal{}, err
	}
	tenantID, err := tenantguard.TenantScope(tenantIDFromRequest(e), h.defaultTenantID, onboardingCapability)
	if err != nil {
		return "", onboarding.Principal{}, err
	}
	if !h.available {
		// Reject, not Error: Error returns nil once the write succeeds, and a
		// caller shaped `if err != nil` would then keep going and emit a
		// second envelope.
		return "", onboarding.Principal{}, httpx.Reject(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Onboarding requires the control-plane database", map[string]any{
				detailsKeyReasonCode: onboardingErrorNoControlPlane,
				detailsKeyRetryable:  true,
			})
	}
	journeyID := strings.TrimSpace(e.Request.PathValue("journeyId"))
	return journeyID, onboarding.Principal{TenantID: tenantID, OwnerSubjectID: ownerID}, nil
}

// onboardingExpectedRevision reads the optional If-Match precondition. The
// revision also travels in the response body because the browser client reads
// bodies, not headers.
func onboardingExpectedRevision(e *httpx.Event) (*int, error) {
	raw := strings.Trim(strings.TrimSpace(e.Request.Header.Get("If-Match")), `"`)
	if raw == "" || raw == "*" {
		return nil, nil
	}
	revision, err := strconv.Atoi(raw)
	if err != nil || revision < 0 {
		return nil, errors.New("invalid If-Match")
	}
	return &revision, nil
}

func (h onboardingHandlers) writeError(e *httpx.Event, err error) error {
	switch {
	case errors.Is(err, onboarding.ErrJourneyNotFound):
		return httpx.Error(e, http.StatusNotFound, ksapi.ErrCodeNotFound,
			"Unknown onboarding journey", map[string]any{
				detailsKeyReasonCode: "journey_not_found",
				detailsKeyRetryable:  false,
			})
	case errors.Is(err, onboarding.ErrStepNotFound):
		return httpx.Error(e, http.StatusNotFound, ksapi.ErrCodeNotFound,
			"Unknown onboarding step", map[string]any{
				detailsKeyReasonCode: "step_not_found",
				detailsKeyRetryable:  false,
			})
	case errors.Is(err, onboarding.ErrUnknownAction):
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest,
			"Unknown onboarding action", map[string]any{
				detailsKeyReasonCode: "action_not_supported",
				detailsKeyRetryable:  false,
			})
	case errors.Is(err, onboarding.ErrDerivedOnly):
		return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
			"This step completes from server facts, not from a client report", map[string]any{
				detailsKeyErrorCode:  onboardingErrorDerivedOnly,
				detailsKeyReasonCode: onboardingErrorDerivedOnly,
				detailsKeyRetryable:  false,
				wizardRunUserGuidanceKey: map[string]any{
					wizardRunGuidanceTitle: "Finish the step to tick it off",
					wizardRunGuidanceBody:  "This step costs money to complete, so kombify only marks it done once the server has actually done it.",
					guidanceKeyNextStep: []string{
						"Complete the step in the product.",
						"Skip it instead if you want it out of your way — skipping unlocks nothing.",
					},
				},
			})
	case errors.Is(err, onboarding.ErrRevisionConflict):
		// 412 has no ErrorCode of its own; CONFLICT is the closest and the
		// reason_code is what the client actually branches on.
		return httpx.Error(e, http.StatusPreconditionFailed, ksapi.ErrCodeConflict,
			"Onboarding state moved on since it was read", map[string]any{
				detailsKeyErrorCode:  onboardingErrorRevisionConflict,
				detailsKeyReasonCode: onboardingErrorRevisionConflict,
				detailsKeyRetryable:  true,
			})
	case errors.Is(err, onboarding.ErrControlPlaneUnavailable):
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Onboarding requires the control-plane database", map[string]any{
				detailsKeyReasonCode: onboardingErrorNoControlPlane,
				detailsKeyRetryable:  true,
			})
	default:
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"Failed to resolve onboarding", nil)
	}
}
