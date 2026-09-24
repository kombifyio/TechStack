package routes

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/specv2"
)

// nativeV2WizardFeatureKey gates the native v2 projection surface; the flag is
// declared in pkg/features/flags.go and defaults to off.
const nativeV2WizardFeatureKey = "native_v2_wizard"

// Envelope keys of the wizard preview contract.
const (
	wizardDetailUserGuidanceKey = "user_guidance"
	wizardGuidanceBodyKey       = "body"
	wizardResponseValidKey      = "valid"
	wizardResponseNodeIDKey     = "node_id"
)

// wizardFeatureChecker is the minimal capability check the wizard routes
// need; pkg/features.Service satisfies it.
type wizardFeatureChecker interface {
	IsEnabled(ctx context.Context, featureKey, userID string) (bool, error)
}

// WizardRouteConfig wires the native v2 wizard surface.
type WizardRouteConfig struct {
	Features  wizardFeatureChecker
	Seeds     specv2.SeedSource
	Projector specv2.Projector
	Validator specv2.SpecValidator
	// ReleaseVersion is informational provenance for the response (the
	// pinned StackKits release the validator binary came from).
	ReleaseVersion string
	// RemoteSSHTester performs outbound SSH reachability checks for the
	// connect-remote wizard. It is intentionally separate from LAN discovery.
	RemoteSSHTester wizardRemoteSSHTester
	Wallet          controlplane.WalletStore
}

type wizardRouteHandlers struct {
	cfg WizardRouteConfig
}

// RegisterWizardRoutes adds the native v2 wizard projection surface.
func RegisterWizardRoutes(r *httpx.Router, cfg WizardRouteConfig) {
	h := wizardRouteHandlers{cfg: cfg}

	// POST /api/v1/wizard/preview - project a WizardIntent onto the pinned
	// kit seed and validate the result with the pinned StackKits CLI. Side
	// effect free; the persisting sibling is POST /api/v1/wizard/runs
	// (internal/routes/stacks/wizard_runs.go).
	r.POST("/api/v1/wizard/preview", h.preview)
	r.POST("/api/v1/wizard/remote/test-ssh", h.testRemoteSSH)
}

// wizardPreviewRequest is the closed preview contract. base_spec carries the
// existing kit deployment's spec for join previews until the wizard-run
// endpoint resolves deployments server-side.
type wizardPreviewRequest struct {
	Intent            specv2.WizardIntent     `json:"intent"`
	BaseSpec          map[string]any          `json:"base_spec,omitempty"`
	SmartHomeContext  specv2.SmartHomeContext `json:"smart_home_context,omitempty"`
	intentAdjustments []specv2.IntentAdjustment
}

// wizardProjectionWriteBudget bounds the response write for the preview
// projection. It runs the pinned-CLI validator with a 2-minute timeout
// (pkg/specv2), which exceeds the server-wide 60s write timeout; the
// extension keeps a slow but successful projection from being cut off.
const wizardProjectionWriteBudget = 5 * time.Minute

func (h wizardRouteHandlers) preview(e *httpx.Event) error {
	_ = httpx.ExtendWriteDeadline(e.Response, wizardProjectionWriteBudget)
	userID, _, ok := authenticatedUser(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	if !h.nativeV2WizardEnabled(e.Request.Context(), userID) {
		return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden, "Native v2 wizard is not enabled", map[string]any{
			inventoryReasonCodeField:          "feature_not_enabled",
			"required_features":               []string{nativeV2WizardFeatureKey},
			"missing_features":                []string{nativeV2WizardFeatureKey},
			managedRuntimeDetailsRetryableKey: false,
			wizardDetailUserGuidanceKey: map[string]any{
				inventoryMCPTitleField: "Native v2 wizard is in beta",
				wizardGuidanceBodyKey:  "Enable the native_v2_wizard beta feature to preview Architecture v2 projections.",
			},
		})
	}

	request, handled := h.decodeWizardPreview(e)
	if handled {
		return nil
	}
	seed, handled := h.previewSeed(e, request)
	if handled {
		return nil
	}

	if h.cfg.Projector == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "StackKits goal authoring is not configured", map[string]any{
			inventoryReasonCodeField:          "wizard_projector_unavailable",
			managedRuntimeDetailsRetryableKey: true,
		})
	}
	projection, projectErr := h.cfg.Projector.Project(e.Request.Context(), seed, request.Intent, request.Intent.HomelabID)
	if projectErr != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, projectErr.Error(), map[string]any{
			inventoryReasonCodeField:          "wizard_projection_rejected",
			managedRuntimeDetailsRetryableKey: false,
		})
	}
	if h.cfg.Validator == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "StackKits validator is not configured", map[string]any{
			inventoryReasonCodeField:          "wizard_validator_unavailable",
			managedRuntimeDetailsRetryableKey: true,
			wizardDetailUserGuidanceKey: map[string]any{
				inventoryMCPTitleField: "Validator unavailable",
				wizardGuidanceBodyKey:  "The pinned StackKits release admission is not configured on this instance, so projected specs cannot be validated.",
			},
		})
	}

	response := map[string]any{
		wizardResponseValidKey:  true,
		"kit_slug":              previewKitSlug(seed),
		wizardResponseNodeIDKey: projection.NodeID,
		"spec":                  projection.Spec,
		"unmapped_goals":        projection.UnmappedGoals,
		"unmapped_purpose":      projection.UnmappedPurpose,
	}
	if len(request.intentAdjustments) > 0 {
		response["intent_adjustments"] = request.intentAdjustments
		response["effective_intent"] = request.Intent
	}
	for _, goal := range request.Intent.Goals {
		if goal == "smart-home" {
			context := request.SmartHomeContext
			if request.Intent.KitAssignment.Mode == specv2.KitAssignmentJoin {
				workloads, _ := seed["workloads"].(map[string]any)
				if _, exists := workloads["smart-home"]; exists {
					context.Existing = true
				}
			}
			response["smart_home_recommendation"] = specv2.RecommendSmartHome(context, request.Intent.UseCaseSettings["smart-home"])
			break
		}
	}
	if h.cfg.ReleaseVersion != "" {
		response["release_version"] = h.cfg.ReleaseVersion
	}
	if validateErr := h.cfg.Validator.ValidateSpec(e.Request.Context(), projection.Spec); validateErr != nil {
		// Mirroring the unifier preview: an invalid projection is structured
		// data for the review step, not an HTTP failure.
		response[wizardResponseValidKey] = false
		response["validate_error"] = validateErr.Error()
	}
	return httpx.Success(e, http.StatusOK, response)
}

// decodeWizardPreview reads and validates the request; handled == true means
// the error response was already written.
func (h wizardRouteHandlers) decodeWizardPreview(e *httpx.Event) (wizardPreviewRequest, bool) {
	var request wizardPreviewRequest
	body, tooLarge, readErr := readRequestBodyLimited(e.Request.Body, maxUnifierRequestBodyBytes)
	if readErr != nil {
		_ = httpx.BadRequest(e, "Failed to read request body", nil)
		return request, true
	}
	if tooLarge {
		_ = httpx.Error(e, http.StatusRequestEntityTooLarge, ksapi.ErrCodeBadRequest, "Request body too large", nil)
		return request, true
	}
	if len(body) == 0 {
		_ = httpx.BadRequest(e, "Request body required", nil)
		return request, true
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if decodeErr := decoder.Decode(&request); decodeErr != nil {
		_ = httpx.BadRequest(e, "Invalid wizard preview request: "+decodeErr.Error(), nil)
		return request, true
	}
	request.Intent, request.intentAdjustments = specv2.NormalizeCreationIntent(request.Intent)
	if intentErr := request.Intent.Validate(); intentErr != nil {
		_ = httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, intentErr.Error(), map[string]any{
			inventoryReasonCodeField:          "wizard_intent_invalid",
			managedRuntimeDetailsRetryableKey: false,
		})
		return request, true
	}
	return request, false
}

// previewSeed resolves the projection base: the pinned kit seed template for
// found runs, the caller-supplied current spec for join previews (until
// wizard runs resolve deployments server-side in phase 3). handled == true
// means the error response was already written.
func (h wizardRouteHandlers) previewSeed(e *httpx.Event, request wizardPreviewRequest) (map[string]any, bool) {
	if request.Intent.KitAssignment.Mode == specv2.KitAssignmentJoin {
		if len(request.BaseSpec) == 0 {
			_ = httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "Join previews require base_spec until wizard runs resolve deployments server-side", map[string]any{
				inventoryReasonCodeField:          "wizard_join_preview_requires_base_spec",
				managedRuntimeDetailsRetryableKey: false,
			})
			return nil, true
		}
		return request.BaseSpec, false
	}
	if h.cfg.Seeds == nil {
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "StackKits seed templates are not configured", map[string]any{
			inventoryReasonCodeField:          "wizard_seed_templates_unavailable",
			managedRuntimeDetailsRetryableKey: true,
		})
		return nil, true
	}
	seed, seedErr := h.cfg.Seeds.Seed(request.Intent.KitAssignment.KitSlug)
	if seedErr != nil {
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Failed to load the kit seed: "+seedErr.Error(), map[string]any{
			inventoryReasonCodeField:          "wizard_seed_unavailable",
			managedRuntimeDetailsRetryableKey: true,
		})
		return nil, true
	}
	return seed, false
}

// nativeV2WizardEnabled fails closed: a nil checker, a checker error, and a
// disabled flag all read as disabled.
func (h wizardRouteHandlers) nativeV2WizardEnabled(ctx context.Context, userID string) bool {
	if h.cfg.Features == nil {
		return false
	}
	enabled, err := h.cfg.Features.IsEnabled(ctx, nativeV2WizardFeatureKey, userID)
	return err == nil && enabled
}

func previewKitSlug(seed map[string]any) string {
	kit, _ := seed["kit"].(map[string]any)
	slug, _ := kit["slug"].(string)
	return slug
}
