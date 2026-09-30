package stacks

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/stackidentity"
)

// The Stack Identity routes read and write the one local copy of the owner's
// Stack Identity: the homelab row (name + presentation). There is no second
// name. kombify Cloud sync runs in the browser through the Gateway with the
// user's own token; these routes only apply the sync rules
// (pkg/stackidentity.Reconcile) to the local copy.

// stackIdentityView is the local API shape (camelCase, as the open-core
// StackIdentity type the UI renders).
type stackIdentityView struct {
	Name              string  `json:"name"`
	CharacterID       string  `json:"characterId"`
	AnimationStyle    string  `json:"animationStyle"`
	AnimationEnabled  *bool   `json:"animationEnabled,omitempty"`
	IconStyle         string  `json:"iconStyle,omitempty"`
	GlowColorOverride *string `json:"glowColorOverride,omitempty"`
	SavedAt           string  `json:"savedAt,omitempty"`
}

type stackIdentitySyncState struct {
	CloudRevision int  `json:"cloud_revision"`
	Pending       bool `json:"pending"`
}

type stackIdentityResponse struct {
	StackIdentity *stackIdentityView            `json:"stack_identity,omitempty"`
	Editable      bool                          `json:"editable"`
	Sync          *stackIdentitySyncState       `json:"sync,omitempty"`
	LocalRevision *int64                        `json:"local_revision,omitempty"`
	Mutation      *stackIdentityMutationReceipt `json:"mutation,omitempty"`
}

// A future account-scoped outbox can acknowledge only this exact receipt.
type stackIdentityMutationReceipt struct {
	ID             string `json:"id"`
	TenantID       string `json:"tenant_id"`
	OwnerSubjectID string `json:"owner_subject_id"`
}

// cloudStackIdentityV1 is kombify Cloud's StackIdentityV1 wire form.
type cloudStackIdentityV1 struct {
	Revision          int     `json:"revision"`
	Name              string  `json:"name"`
	CharacterID       string  `json:"character_id"`
	AnimationStyle    string  `json:"animation_style"`
	AnimationEnabled  *bool   `json:"animation_enabled"`
	IconStyle         string  `json:"icon_style"`
	GlowColorOverride *string `json:"glow_color_override"`
	UpdatedAt         string  `json:"updated_at"`
}

// cloudStackIdentityPatch is the body the browser sends to Cloud's
// PUT /api/v1/stack-identity (through the Gateway's /v1/cloud route).
type cloudStackIdentityPatch struct {
	Name              string  `json:"name"`
	CharacterID       string  `json:"character_id,omitempty"`
	AnimationStyle    string  `json:"animation_style,omitempty"`
	AnimationEnabled  *bool   `json:"animation_enabled,omitempty"`
	IconStyle         string  `json:"icon_style,omitempty"`
	GlowColorOverride *string `json:"glow_color_override,omitempty"`
}

type stackIdentityPush struct {
	IfMatch int                     `json:"if_match"`
	Body    cloudStackIdentityPatch `json:"body"`
}

type stackIdentitySyncRequest struct {
	// Cloud is nil when kombify Cloud has no Stack Identity (HTTP 404).
	Cloud *cloudStackIdentityV1 `json:"cloud"`
}

type stackIdentitySyncResponse struct {
	stackIdentityResponse
	Action string             `json:"action"`
	Reason string             `json:"reason"`
	Push   *stackIdentityPush `json:"push,omitempty"`
}

func (h crudRouteHandlers) stackIdentityScope(e *httpx.Event, action string) (tenantID, ownerID string, err error) {
	ownerID, err = requireStackAuth(e)
	if err != nil {
		return "", "", err
	}
	tenantID, err = tenantguard.TenantScope(tenantIDFromRequest(e), ownerID, action)
	return tenantID, ownerID, err
}

func (h crudRouteHandlers) ownedHomelab(e *httpx.Event, tenantID, ownerID string) (*controlplane.Homelab, error) {
	if h.homelabStore == nil || tenantID == "" {
		return nil, controlplane.ErrNotFound
	}
	return h.homelabStore.GetHomelabByOwner(e.Request.Context(), tenantID, ownerID)
}

// getStackIdentity returns the homelab's Stack Identity. A homelab that still
// carries its generated name has none yet.
func (h crudRouteHandlers) getStackIdentity(e *httpx.Event) error {
	tenantID, ownerID, err := h.stackIdentityScope(e, "techstack.stack_identity.read")
	if err != nil {
		return err
	}
	var homelab *controlplane.Homelab
	var lookupErr error
	var revision *int64
	if store, ok := h.homelabStore.(controlplane.ConditionalStackIdentityStore); ok {
		var current int64
		homelab, current, lookupErr = store.ReadStackIdentitySnapshot(e.Request.Context(), tenantID, ownerID)
		revision = &current
	} else {
		homelab, lookupErr = h.ownedHomelab(e, tenantID, ownerID)
	}
	if lookupErr != nil && !errors.Is(lookupErr, controlplane.ErrNotFound) {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to load the Stack Identity", nil)
	}
	response := h.stackIdentityResponseFor(homelab)
	response.LocalRevision = revision
	if revision != nil {
		writeStackIdentityRevisionHeaders(e, *revision)
	}
	return httpx.Success(e, http.StatusOK, response)
}

// updateStackIdentity is a local Stack Identity edit. It creates the homelab
// when the owner names it before the first rollout, and it is marked pending
// until kombify Cloud confirms it (when connected).
func (h crudRouteHandlers) updateStackIdentity(e *httpx.Event) error {
	tenantID, ownerID, err := h.stackIdentityScope(e, "techstack.stack_identity.write")
	if err != nil {
		return err
	}
	if h.homelabStore == nil || tenantID == "" {
		return stackIdentityUnavailable(e)
	}
	var request stackIdentityView
	decoder := json.NewDecoder(io.LimitReader(e.Request.Body, 4096))
	if decodeErr := decoder.Decode(&request); decodeErr != nil {
		return httpx.BadRequest(e, "Invalid JSON")
	}
	name, nameErr := controlplane.NormalizeStackIdentityName(request.Name)
	if nameErr != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation,
			"Stack Identity name must be between 1 and 30 characters", map[string]any{
				detailsKeyReasonCode: "stack_identity_name_invalid",
				detailsKeyRetryable:  false,
			})
	}
	presentation, presentationErr := presentationFromView(request)
	if presentationErr != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, presentationErr.Error(), map[string]any{
			detailsKeyReasonCode: "stack_identity_presentation_invalid",
			detailsKeyRetryable:  false,
		})
	}

	ctx := e.Request.Context()
	homelab, err := h.homelabStore.GetOrCreateHomelabForOwner(ctx, controlplane.CreateHomelabRequest{
		ID:             deterministicHomelabID(tenantID, ownerID),
		TenantID:       tenantID,
		OwnerSubjectID: ownerID,
		Name:           name,
	})
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to resolve homelab", nil)
	}
	if presentation == nil {
		presentation = homelab.Identity.Presentation
	}
	updated, err := h.homelabStore.UpdateHomelabStackIdentity(ctx, tenantID, homelab.ID, controlplane.HomelabStackIdentityWrite{
		Name:          name,
		Presentation:  presentation,
		CloudRevision: homelab.Identity.CloudRevision,
		Pending:       true,
		EditedAt:      time.Now().UTC(),
	})
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to save the Stack Identity", nil)
	}
	return httpx.Success(e, http.StatusOK, h.stackIdentityResponseFor(updated))
}

// syncStackIdentity applies one sync step with kombify Cloud's record, which
// the browser read through the Gateway. It adopts Cloud's record, confirms an
// identical one, or returns the body the browser must push to Cloud.
func (h crudRouteHandlers) syncStackIdentity(e *httpx.Event) error {
	tenantID, ownerID, err := h.stackIdentityScope(e, "techstack.stack_identity.sync")
	if err != nil {
		return err
	}
	if h.homelabStore == nil || tenantID == "" {
		return stackIdentityUnavailable(e)
	}
	var request stackIdentitySyncRequest
	decoder := json.NewDecoder(io.LimitReader(e.Request.Body, 8192))
	if decodeErr := decoder.Decode(&request); decodeErr != nil {
		return httpx.BadRequest(e, "Invalid JSON")
	}
	cloud, cloudErr := cloudFromWire(request.Cloud)
	if cloudErr != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, cloudErr.Error(), map[string]any{
			detailsKeyReasonCode: "stack_identity_cloud_invalid",
			detailsKeyRetryable:  false,
		})
	}

	ctx := e.Request.Context()
	var homelab *controlplane.Homelab
	var lookupErr error
	var snapshotRevision *int64
	if store, ok := h.homelabStore.(controlplane.ConditionalStackIdentityStore); ok {
		var revision int64
		homelab, revision, lookupErr = store.ReadStackIdentitySnapshot(ctx, tenantID, ownerID)
		snapshotRevision = &revision
	} else {
		homelab, lookupErr = h.homelabStore.GetHomelabByOwner(ctx, tenantID, ownerID)
	}
	if lookupErr != nil && !errors.Is(lookupErr, controlplane.ErrNotFound) {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to resolve homelab", nil)
	}
	if homelab == nil || errors.Is(lookupErr, controlplane.ErrNotFound) {
		// Before the first homelab exists there is nothing local to sync; the
		// Cloud identity is still what the owner sees.
		response := stackIdentitySyncResponse{Action: string(stackidentity.ActionNone), Reason: "homelab_missing"}
		response.Editable = true
		if cloud != nil {
			response.StackIdentity = viewFromCloud(*cloud)
		}
		return httpx.Success(e, http.StatusOK, response)
	}

	decision := stackidentity.Reconcile(localFromHomelab(homelab), cloud)
	response := stackIdentitySyncResponse{Action: string(decision.Action), Reason: decision.Reason}
	switch decision.Action {
	case stackidentity.ActionAdoptCloud, stackidentity.ActionInSync:
		write := controlplane.HomelabStackIdentityWrite{
			Name:             cloud.Name,
			Presentation:     presentationPtr(cloud.Presentation),
			CloudRevision:    cloud.Revision,
			Pending:          false,
			EditedAt:         cloud.UpdatedAt,
			ExpectedRevision: snapshotRevision,
		}
		if decision.Action == stackidentity.ActionInSync && homelab.NamedAt != nil {
			write.EditedAt = *homelab.NamedAt
		}
		updated, writeErr := h.homelabStore.UpdateHomelabStackIdentity(ctx, tenantID, homelab.ID, write)
		if errors.Is(writeErr, controlplane.ErrIdentityRevisionConflict) {
			return httpx.Error(e, http.StatusPreconditionFailed, ksapi.ErrCodeConflict,
				"Stack Identity changed while syncing; read it again",
				map[string]any{detailsKeyReasonCode: "stack_identity_revision_conflict"})
		}
		if writeErr != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to save the Stack Identity", nil)
		}
		homelab = updated
	case stackidentity.ActionPushLocal:
		response.Push = &stackIdentityPush{IfMatch: decision.IfMatch, Body: pushBody(homelab, decision.IfMatch)}
	}
	response.stackIdentityResponse = h.stackIdentityResponseFor(homelab)
	return httpx.Success(e, http.StatusOK, response)
}

func (h crudRouteHandlers) stackIdentityResponseFor(homelab *controlplane.Homelab) stackIdentityResponse {
	response := stackIdentityResponse{Editable: h.homelabStore != nil}
	if homelab == nil {
		return response
	}
	response.Sync = &stackIdentitySyncState{
		CloudRevision: homelab.Identity.CloudRevision,
		Pending:       homelab.Identity.Pending,
	}
	if homelab.NamedAt == nil {
		return response
	}
	view := &stackIdentityView{Name: homelab.Name, SavedAt: formatAPITime(*homelab.NamedAt)}
	if p := homelab.Identity.Presentation; p != nil {
		view.CharacterID = p.CharacterID
		view.AnimationStyle = p.AnimationStyle
		view.AnimationEnabled = p.AnimationEnabled
		view.IconStyle = p.IconStyle
		view.GlowColorOverride = p.GlowColorOverride
	}
	response.StackIdentity = view
	return response
}

func localFromHomelab(homelab *controlplane.Homelab) stackidentity.Local {
	local := stackidentity.Local{
		Name:          homelab.Name,
		Named:         homelab.NamedAt != nil,
		Presentation:  homelab.Identity.Presentation,
		CloudRevision: homelab.Identity.CloudRevision,
		Pending:       homelab.Identity.Pending,
	}
	if homelab.NamedAt != nil {
		local.EditedAt = *homelab.NamedAt
	}
	return local
}

func cloudFromWire(wire *cloudStackIdentityV1) (*stackidentity.Cloud, error) {
	if wire == nil {
		return nil, nil
	}
	name, err := controlplane.NormalizeStackIdentityName(wire.Name)
	if err != nil {
		return nil, errors.New("cloud Stack Identity name is invalid")
	}
	if wire.Revision < 1 {
		return nil, errors.New("cloud Stack Identity revision is required")
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(wire.UpdatedAt))
	if err != nil {
		return nil, errors.New("cloud Stack Identity updated_at is invalid")
	}
	presentation := controlplane.StackIdentityPresentation{
		CharacterID:       strings.TrimSpace(wire.CharacterID),
		AnimationStyle:    strings.TrimSpace(wire.AnimationStyle),
		AnimationEnabled:  wire.AnimationEnabled,
		IconStyle:         strings.TrimSpace(wire.IconStyle),
		GlowColorOverride: wire.GlowColorOverride,
	}
	if err := validatePresentation(presentation); err != nil {
		return nil, err
	}
	return &stackidentity.Cloud{Revision: wire.Revision, Name: name, Presentation: presentation, UpdatedAt: updatedAt.UTC()}, nil
}

func viewFromCloud(cloud stackidentity.Cloud) *stackIdentityView {
	return &stackIdentityView{
		Name:              cloud.Name,
		CharacterID:       cloud.Presentation.CharacterID,
		AnimationStyle:    cloud.Presentation.AnimationStyle,
		AnimationEnabled:  cloud.Presentation.AnimationEnabled,
		IconStyle:         cloud.Presentation.IconStyle,
		GlowColorOverride: cloud.Presentation.GlowColorOverride,
		SavedAt:           formatAPITime(cloud.UpdatedAt),
	}
}

func presentationFromView(view stackIdentityView) (*controlplane.StackIdentityPresentation, error) {
	presentation := controlplane.StackIdentityPresentation{
		CharacterID:       strings.TrimSpace(view.CharacterID),
		AnimationStyle:    strings.TrimSpace(view.AnimationStyle),
		AnimationEnabled:  view.AnimationEnabled,
		IconStyle:         strings.TrimSpace(view.IconStyle),
		GlowColorOverride: view.GlowColorOverride,
	}
	if presentation.CharacterID == "" && presentation.AnimationStyle == "" && presentation.IconStyle == "" &&
		presentation.AnimationEnabled == nil && presentation.GlowColorOverride == nil {
		return nil, nil
	}
	if err := validatePresentation(presentation); err != nil {
		return nil, err
	}
	return &presentation, nil
}

// validatePresentation mirrors StackIdentityV1: catalog ids, a closed icon
// style set and a hex glow color. Nothing else reaches the homelab row.
func validatePresentation(p controlplane.StackIdentityPresentation) error {
	for _, id := range []string{p.CharacterID, p.AnimationStyle} {
		if id != "" && !catalogID(id) {
			return errors.New("Stack Identity character and animation must be catalog ids")
		}
	}
	switch p.IconStyle {
	case "", "filled", "glass", "gradient", "outlined":
	default:
		return errors.New("Stack Identity icon style is not supported")
	}
	if p.GlowColorOverride != nil && !hexColor(*p.GlowColorOverride) {
		return errors.New("Stack Identity glow color must be a hex color")
	}
	return nil
}

func catalogID(value string) bool {
	if len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func hexColor(value string) bool {
	if len(value) != 7 && len(value) != 9 || value[0] != '#' {
		return false
	}
	for _, r := range value[1:] {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') && !(r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func presentationPtr(p controlplane.StackIdentityPresentation) *controlplane.StackIdentityPresentation {
	return &p
}

// pushBody is the Cloud patch for the local copy. Cloud requires a character
// to create a record, so a name-only identity gets the default character on
// its first push.
func pushBody(homelab *controlplane.Homelab, ifMatch int) cloudStackIdentityPatch {
	body := cloudStackIdentityPatch{Name: homelab.Name}
	if p := homelab.Identity.Presentation; p != nil {
		body.CharacterID = p.CharacterID
		body.AnimationStyle = p.AnimationStyle
		body.AnimationEnabled = p.AnimationEnabled
		body.IconStyle = p.IconStyle
		body.GlowColorOverride = p.GlowColorOverride
	}
	if ifMatch == 0 && body.CharacterID == "" {
		body.CharacterID = stackidentity.DefaultCharacterID
	}
	return body
}

func stackIdentityUnavailable(e *httpx.Event) error {
	return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
		"Stack Identity requires the Postgres control plane", map[string]any{
			detailsKeyReasonCode: "stack_identity_store_unavailable",
			detailsKeyRetryable:  false,
		})
}
