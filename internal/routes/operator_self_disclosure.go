package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	kscore "github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/unifier"
	"github.com/google/uuid"
)

// The operator self-disclosure surface (CREATION-EXPERIENCE-STANDARD §4).
//
// A signed-in operator may tell Techstack about themselves: how hands-on they
// want to be, what the homelab is for, where it runs and who uses it. The
// Cloud dashboard captures these answers and hands them to the creation
// wizard, which stores them here; Techstack's own surfaces may do the same.
// The body carries raw answers only. The capability profile in every response
// is derived by the Unifier, so no client can store a score.

const (
	maxSelfDisclosureBodyBytes = 16 << 10
	selfDisclosureCapability   = "techstack.operator.self_disclosure"

	selfDisclosureReasonInvalid        = "self_disclosure_invalid"
	selfDisclosureReasonNoControlPlane = "self_disclosure_requires_control_plane"
)

// selfDisclosureWriteRequest is the closed wire contract of one write.
type selfDisclosureWriteRequest struct {
	Source     string                        `json:"source"`
	Disclosure kscore.OperatorSelfDisclosure `json:"disclosure"`
}

func (api *UnifierAPI) handleGetSelfDisclosure(e *httpx.Event) error {
	tenantID, ownerID, err := api.selfDisclosureScope(e)
	if err != nil {
		return err
	}
	stored, err := api.selfDisclosures.GetOperatorSelfDisclosure(e.Request.Context(), tenantID, ownerID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return httpx.Success(e, http.StatusOK, kscore.OperatorSelfDisclosureRecord{})
	}
	if err != nil {
		return httpx.InternalError(e, "failed to read self-disclosure")
	}
	return httpx.Success(e, http.StatusOK, selfDisclosureRecord(stored))
}

func (api *UnifierAPI) handlePutSelfDisclosure(e *httpx.Event) error {
	tenantID, ownerID, err := api.selfDisclosureScope(e)
	if err != nil {
		return err
	}
	body, tooLarge, readErr := readRequestBodyLimited(e.Request.Body, maxSelfDisclosureBodyBytes)
	if readErr != nil {
		return httpx.BadRequest(e, "failed to read self-disclosure")
	}
	if tooLarge {
		return httpx.Error(e, http.StatusRequestEntityTooLarge, ksapi.ErrCodeBadRequest, "request body exceeds size limit", nil)
	}

	var request selfDisclosureWriteRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&request)
	if decodeErr == nil {
		var trailing any
		if trailingErr := decoder.Decode(&trailing); trailingErr != io.EOF {
			decodeErr = errors.New("request must contain one JSON object")
		}
	}
	if decodeErr == nil {
		decodeErr = request.Disclosure.Normalize()
	}
	source := strings.ToLower(strings.TrimSpace(request.Source))
	if decodeErr == nil && source != controlplane.OperatorSelfDisclosureSourceCloud && source != controlplane.OperatorSelfDisclosureSourceTechstack {
		decodeErr = errors.New("source must be cloud or techstack")
	}
	if decodeErr != nil {
		return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeBadRequest,
			"Self-disclosure is not valid: "+decodeErr.Error(), map[string]any{
				"reason_code": selfDisclosureReasonInvalid,
				"retryable":   false,
			})
	}

	saved, err := api.selfDisclosures.UpsertOperatorSelfDisclosure(e.Request.Context(), controlplane.OperatorSelfDisclosure{
		ID:             uuid.NewString(),
		TenantID:       tenantID,
		OwnerSubjectID: ownerID,
		Source:         source,
		Disclosure:     request.Disclosure,
	})
	if err != nil {
		return httpx.InternalError(e, "failed to store self-disclosure")
	}
	return httpx.Success(e, http.StatusOK, selfDisclosureRecord(saved))
}

// selfDisclosureScope authenticates the caller, resolves the tenant and fails
// closed when there is nowhere to persist the answers.
func (api *UnifierAPI) selfDisclosureScope(e *httpx.Event) (string, string, error) {
	ownerID, err := requireUnifierAuth(e)
	if err != nil {
		return "", "", err
	}
	tenantID, err := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, selfDisclosureCapability)
	if err != nil {
		return "", "", err
	}
	if api.selfDisclosures == nil {
		return "", "", httpx.Reject(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Self-disclosure requires the control-plane database", map[string]any{
				"reason_code": selfDisclosureReasonNoControlPlane,
				"retryable":   true,
			})
	}
	return tenantID, ownerID, nil
}

// loadSelfDisclosure returns the principal's stored answers for the
// recommendation authority. A missing or unreadable disclosure never blocks a
// recommendation: the observed baseline remains the fallback.
func (api *UnifierAPI) loadSelfDisclosure(ctx context.Context, tenantID, ownerID string) (*kscore.OperatorSelfDisclosure, time.Time) {
	if api.selfDisclosures == nil {
		return nil, time.Time{}
	}
	stored, err := api.selfDisclosures.GetOperatorSelfDisclosure(ctx, tenantID, ownerID)
	if err != nil || stored == nil {
		return nil, time.Time{}
	}
	disclosure := stored.Disclosure
	return &disclosure, stored.UpdatedAt
}

func selfDisclosureRecord(stored *controlplane.OperatorSelfDisclosure) kscore.OperatorSelfDisclosureRecord {
	if stored == nil {
		return kscore.OperatorSelfDisclosureRecord{}
	}
	disclosure := stored.Disclosure
	return kscore.OperatorSelfDisclosureRecord{
		Disclosure: disclosure,
		Profile:    unifier.OperatorCapabilityFromSelfDisclosure(&disclosure, stored.UpdatedAt),
		Source:     stored.Source,
		UpdatedAt:  stored.UpdatedAt,
	}
}
