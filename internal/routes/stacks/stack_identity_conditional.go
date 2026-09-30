package stacks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// This endpoint is opt-in for a single future offline operation. The legacy
// online PUT stays compatible while every writer advances the same revision.
func (h crudRouteHandlers) updateStackIdentityConditional(e *httpx.Event) error {
	tenantID, ownerID, err := h.stackIdentityScope(e, "techstack.stack_identity.write")
	if err != nil {
		return err
	}
	store, ok := h.homelabStore.(controlplane.ConditionalStackIdentityStore)
	if !ok || tenantID == "" {
		return stackIdentityUnavailable(e)
	}
	expected, err := parseStackIdentityIfMatch(e.Request.Header.Get("If-Match"))
	if err != nil {
		return httpx.Error(e, http.StatusPreconditionRequired, ksapi.ErrCodeBadRequest,
			"A strong Stack Identity If-Match is required", map[string]any{detailsKeyReasonCode: "stack_identity_if_match_required"})
	}
	mutationID := strings.TrimSpace(e.Request.Header.Get("Idempotency-Key"))
	if !validStackIdentityMutationID(mutationID) {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest,
			"A stable mutation id is required", map[string]any{detailsKeyReasonCode: "stack_identity_mutation_id_invalid"})
	}
	if e.Request.Body == nil {
		return httpx.BadRequest(e, "Invalid Stack Identity JSON")
	}
	raw, err := io.ReadAll(io.LimitReader(e.Request.Body, 4097))
	if err != nil || len(raw) > 4096 {
		return httpx.BadRequest(e, "Invalid Stack Identity JSON")
	}
	var request stackIdentityView
	if err := json.Unmarshal(raw, &request); err != nil {
		return httpx.BadRequest(e, "Invalid Stack Identity JSON")
	}
	name, err := controlplane.NormalizeStackIdentityName(request.Name)
	if err != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation,
			"Stack Identity name must be between 1 and 30 characters",
			map[string]any{detailsKeyReasonCode: "stack_identity_name_invalid"})
	}
	presentation, err := presentationFromView(request)
	if err != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, err.Error(),
			map[string]any{detailsKeyReasonCode: "stack_identity_presentation_invalid"})
	}
	// A retry must repeat the exact JSON bytes and original If-Match. The
	// receipt binds both, so a reused key with different bytes is a conflict.
	digest := sha256.Sum256(append(append([]byte(nil), raw...), []byte("\n"+strconv.FormatInt(expected, 10))...))
	result, err := store.ApplyStackIdentityMutation(e.Request.Context(), controlplane.StackIdentityMutation{
		TenantID: tenantID, OwnerSubjectID: ownerID,
		HomelabID:  deterministicHomelabID(tenantID, ownerID),
		MutationID: mutationID, PayloadSHA256: hex.EncodeToString(digest[:]),
		ExpectedRevision: expected,
		Write: controlplane.HomelabStackIdentityWrite{
			Name: name, Presentation: presentation, Pending: true, EditedAt: time.Now().UTC(),
		},
	})
	if errors.Is(err, controlplane.ErrIdentityMutationConflict) {
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"Mutation id was used for a different Stack Identity edit",
			map[string]any{detailsKeyReasonCode: "stack_identity_mutation_conflict"})
	}
	if errors.Is(err, controlplane.ErrIdentityRevisionConflict) {
		return httpx.Error(e, http.StatusPreconditionFailed, ksapi.ErrCodeConflict,
			"Stack Identity revision does not match If-Match",
			map[string]any{detailsKeyReasonCode: "stack_identity_revision_conflict"})
	}
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"Failed to save the Stack Identity", nil)
	}
	writeStackIdentityRevisionHeaders(e, result.Revision)
	response := h.stackIdentityResponseFor(result.Homelab)
	response.LocalRevision = &result.Revision
	response.Mutation = &stackIdentityMutationReceipt{ID: mutationID, TenantID: tenantID, OwnerSubjectID: ownerID}
	return httpx.Success(e, http.StatusOK, response)
}

func parseStackIdentityIfMatch(raw string) (int64, error) {
	if len(raw) < 3 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return 0, errors.New("strong revision required")
	}
	revision, err := strconv.ParseInt(raw[1:len(raw)-1], 10, 64)
	if err != nil || revision < 0 || strconv.FormatInt(revision, 10) != raw[1:len(raw)-1] {
		return 0, errors.New("invalid revision")
	}
	return revision, nil
}

func validStackIdentityMutationID(value string) bool {
	if len(value) < 16 || len(value) > 128 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func writeStackIdentityRevisionHeaders(e *httpx.Event, revision int64) {
	e.Response.Header().Set("ETag", fmt.Sprintf(`"%d"`, revision))
	e.Response.Header().Set("Cache-Control", "no-store")
}
