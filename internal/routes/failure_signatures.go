package routes

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/failureknowledge"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// RegisterFailureSignatureRoutes exposes the tenant's own class A failure
// occurrences grouped by signature (knowledge-search plan section 5.8). Every
// read is tenant-scoped; no other tenant's occurrence is ever visible.
func RegisterFailureSignatureRoutes(r *httpx.Router, store controlplane.FailureOccurrenceStore) {
	if store == nil {
		panic("RegisterFailureSignatureRoutes: failure occurrence store is required")
	}
	// GET /api/v1/failures/signatures - The tenant's failure signatures.
	r.GET("/api/v1/failures/signatures", func(e *httpx.Event) error {
		ownerID, authErr := requireAuth(e)
		if authErr != nil {
			return authErr
		}
		tenantID, err := requireJobRouteTenant(e, ownerID, "techstack.failures.read")
		if err != nil {
			return err
		}
		limit, invalid := failureQueryInt(e, "limit", 50, 1, 200)
		if invalid != "" {
			return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, invalid, nil)
		}
		windowDays, invalid := failureQueryInt(e, "window_days", 7, 1, 90)
		if invalid != "" {
			return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, invalid, nil)
		}
		items, err := store.ListFailureSignatures(e.Request.Context(), tenantID, limit, time.Duration(windowDays)*24*time.Hour)
		if err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch failure signatures", nil)
		}
		return httpx.Success(e, http.StatusOK, map[string]any{
			"items":              items,
			"normalizer_version": failureknowledge.NormalizerVersion,
			"window_days":        windowDays,
		})
	})
}

// RegisterFailureOutcomeRoutes records and reads the outcomes of failure
// resolutions applied in the tenant (plan section 5.8 step 7). The outcome is
// the terminal state of the procedure job, read in the tenant; the caller
// only attributes the job to a resolution and a signature.
func RegisterFailureOutcomeRoutes(r *httpx.Router, store controlplane.FailureOutcomeStore) {
	if store == nil {
		panic("RegisterFailureOutcomeRoutes: failure outcome store is required")
	}
	// POST /api/v1/failures/outcomes - Record the outcome of an applied resolution.
	r.POST("/api/v1/failures/outcomes", func(e *httpx.Event) error {
		ownerID, authErr := requireAuth(e)
		if authErr != nil {
			return authErr
		}
		tenantID, err := requireJobRouteTenant(e, ownerID, "techstack.failures.write")
		if err != nil {
			return err
		}
		var req struct {
			ResolutionID string `json:"resolution_id"`
			Signature    string `json:"signature"`
			Procedure    string `json:"procedure"`
			JobID        string `json:"job_id"`
		}
		if bindErr := e.BindBody(&req); bindErr != nil {
			return httpx.BadRequest(e, "Invalid request body", nil)
		}
		outcome, created, err := store.RecordFailureOutcome(e.Request.Context(), tenantID, controlplane.RecordFailureOutcomeRequest{
			ResolutionID: req.ResolutionID,
			Signature:    req.Signature,
			Procedure:    req.Procedure,
			JobID:        req.JobID,
			RecordedBy:   ownerID,
		})
		switch {
		case errors.Is(err, controlplane.ErrFailureOutcomeInvalid):
			return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, strings.TrimPrefix(err.Error(), controlplane.ErrFailureOutcomeInvalid.Error()+": "), nil)
		case errors.Is(err, controlplane.ErrNotFound):
			return httpx.Error(e, http.StatusNotFound, ksapi.ErrCodeNotFound, "Job not found", nil)
		case errors.Is(err, controlplane.ErrFailureOutcomeJobNotTerminal):
			return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The procedure job has not completed or failed", nil)
		case errors.Is(err, controlplane.ErrFailureOutcomeNoOccurrence):
			return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "The tenant has no failure occurrence with this signature before the job, on its stack", nil)
		case err != nil:
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to record the failure outcome", nil)
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		return httpx.Success(e, status, map[string]any{"outcome": outcome, "created": created})
	})

	// GET /api/v1/failures/outcomes - The tenant's resolution outcomes and counts.
	r.GET("/api/v1/failures/outcomes", func(e *httpx.Event) error {
		ownerID, authErr := requireAuth(e)
		if authErr != nil {
			return authErr
		}
		tenantID, err := requireJobRouteTenant(e, ownerID, "techstack.failures.read")
		if err != nil {
			return err
		}
		limit, invalid := failureQueryInt(e, "limit", 50, 1, 200)
		if invalid != "" {
			return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, invalid, nil)
		}
		items, summaries, err := store.ListFailureOutcomes(e.Request.Context(), tenantID, e.Request.URL.Query().Get("resolution_id"), limit)
		if errors.Is(err, controlplane.ErrFailureOutcomeInvalid) {
			return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, strings.TrimPrefix(err.Error(), controlplane.ErrFailureOutcomeInvalid.Error()+": "), nil)
		}
		if err != nil {
			return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to fetch failure outcomes", nil)
		}
		return httpx.Success(e, http.StatusOK, map[string]any{"items": items, "resolutions": summaries})
	})
}

// failureQueryInt reads an optional integer query parameter; a value out of
// range returns the validation message.
func failureQueryInt(e *httpx.Event, name string, fallback, low, high int) (int, string) {
	raw := e.Request.URL.Query().Get(name)
	if raw == "" {
		return fallback, ""
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < low || parsed > high {
		return 0, name + " must be between " + strconv.Itoa(low) + " and " + strconv.Itoa(high)
	}
	return parsed, ""
}
