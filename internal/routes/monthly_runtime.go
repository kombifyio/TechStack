package routes

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

func RegisterMonthlyRuntimeRoutes(r *httpx.Router, svc *monthlyruntime.Service) {
	if r == nil || svc == nil {
		return
	}
	r.GET("/api/v1/monthly-runtimes/offerings", monthlyRuntimeOfferingsHandler(svc))
	r.GET("/api/v1/monthly-runtimes/{id}", monthlyRuntimeActionHandler(svc, serverruntime.RuntimeActionStatus))
	r.POST("/api/v1/monthly-runtimes/{id}/start", monthlyRuntimeActionHandler(svc, serverruntime.RuntimeActionStart))
	r.POST("/api/v1/monthly-runtimes/{id}/stop", monthlyRuntimeActionHandler(svc, serverruntime.RuntimeActionStop))
	r.POST("/api/v1/monthly-runtimes/{id}/enable-ssh", monthlyRuntimeActionHandler(svc, serverruntime.RuntimeActionEnableSSH))
	r.POST("/api/v1/monthly-runtimes/{id}/disable-ssh", monthlyRuntimeActionHandler(svc, serverruntime.RuntimeActionDisableSSH))
	r.POST("/api/v1/monthly-runtimes/{id}/decommission", monthlyRuntimeDecommissionHandler(svc))
	r.POST("/api/v1/monthly-runtimes/{id}/resolve-custody", monthlyRuntimeResolveCustodyHandler(svc))
	r.POST("/api/v1/monthly-runtimes/{id}/reconnect", monthlyRuntimeReconnectHandler(svc))
	r.GET("/api/v1/monthly-runtimes/{id}/ssh", monthlyRuntimeActionHandler(svc, serverruntime.RuntimeActionSSHInfo))
	r.GET("/api/v1/monthly-runtimes/{id}/operations", monthlyRuntimeOperationsHandler(svc))
	r.GET("/api/v1/monthly-runtimes/{id}/cleanup-readback", monthlyRuntimeCleanupReadbackHandler(svc))
}

// monthlyRuntimeCleanupReadbackHandler exposes only owner-scoped, redacted
// provider-control completion facts. It does not invoke a provider or agent.
func monthlyRuntimeCleanupReadbackHandler(svc *monthlyruntime.Service) func(*httpx.Event) error {
	return func(e *httpx.Event) error {
		userID, ok := authenticatedUserID(e)
		if !ok {
			return httpx.Unauthorized(e, "Authentication required")
		}
		response, err := svc.CleanupStatus(e.Request.Context(), monthlyRuntimeTenantID(e, userID), userID,
			vmlease.LeaseID(strings.TrimSpace(e.Request.PathValue("id"))))
		if err != nil {
			return monthlyRuntimeError(e, err)
		}
		return httpx.Success(e, http.StatusOK, response)
	}
}

type monthlyRuntimeResolveCustodyInput struct {
	ProviderCleanupConfirmed bool `json:"provider_cleanup_confirmed"`
}

func monthlyRuntimeResolveCustodyHandler(svc *monthlyruntime.Service) func(*httpx.Event) error {
	return func(e *httpx.Event) error {
		userID, ok := authenticatedUserID(e)
		if !ok {
			return httpx.Unauthorized(e, "Authentication required")
		}
		var input monthlyRuntimeResolveCustodyInput
		decoder := json.NewDecoder(http.MaxBytesReader(e.Response, e.Request.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			return httpx.BadRequest(e, "Invalid custody resolution request")
		}
		resp, err := svc.ResolveCustody(e.Request.Context(), monthlyruntime.CustodyResolutionRequest{
			TenantID: monthlyRuntimeTenantID(e, userID), UserID: userID,
			LeaseID:                  vmlease.LeaseID(strings.TrimSpace(e.Request.PathValue("id"))),
			ProviderCleanupConfirmed: input.ProviderCleanupConfirmed,
		})
		if err != nil {
			return monthlyRuntimeError(e, err)
		}
		return httpx.Success(e, http.StatusOK, resp)
	}
}

func monthlyRuntimeOfferingsHandler(svc *monthlyruntime.Service) func(*httpx.Event) error {
	return func(e *httpx.Event) error {
		if _, ok := authenticatedUserID(e); !ok {
			return httpx.Unauthorized(e, "Authentication required")
		}
		return httpx.Success(e, http.StatusOK, svc.Offerings())
	}
}

func monthlyRuntimeActionHandler(svc *monthlyruntime.Service, action serverruntime.RuntimeAction) func(*httpx.Event) error {
	return func(e *httpx.Event) error {
		userID, ok := authenticatedUserID(e)
		if !ok {
			return httpx.Unauthorized(e, "Authentication required")
		}
		tenantID := monthlyRuntimeTenantID(e, userID)
		resp, err := svc.Action(e.Request.Context(), monthlyruntime.ActionRequest{
			TenantID: tenantID,
			UserID:   userID,
			LeaseID:  vmlease.LeaseID(strings.TrimSpace(e.Request.PathValue("id"))),
			Action:   action,
		})
		if err != nil {
			return monthlyRuntimeError(e, err)
		}
		return httpx.Success(e, http.StatusOK, resp)
	}
}

// monthlyRuntimeDecommissionHandler decommissions a managed runtime. It accepts
// an optional {"force": true} body or ?force=true query param: force cancels the
// lease and reconciles the provider resource out-of-band even when the runtime
// is unreachable. Without force, an unreachable runtime yields 409 + a structured
// force offer instead of a bare failure.
func monthlyRuntimeDecommissionHandler(svc *monthlyruntime.Service) func(*httpx.Event) error {
	return func(e *httpx.Event) error {
		userID, ok := authenticatedUserID(e)
		if !ok {
			return httpx.Unauthorized(e, "Authentication required")
		}
		leaseID := strings.TrimSpace(e.Request.PathValue("id"))
		decommission, parseErr := monthlyRuntimeDecommissionRequested(e)
		if parseErr != nil {
			return httpx.BadRequest(e, "Invalid decommission request")
		}
		resp, err := svc.Action(e.Request.Context(), monthlyruntime.ActionRequest{
			TenantID:                         monthlyRuntimeTenantID(e, userID),
			UserID:                           userID,
			LeaseID:                          vmlease.LeaseID(leaseID),
			Action:                           serverruntime.RuntimeActionDecommission,
			Force:                            decommission.Force,
			ExpectedResourceGenerationDigest: decommission.ExpectedResourceGenerationDigest,
		})
		if err != nil {
			return monthlyRuntimeDecommissionError(e, svc, err, leaseID)
		}
		status := http.StatusOK
		if resp != nil && resp.ObservedState == "reconciliation_pending" {
			// Native decommission only establishes durable provider-reconciliation
			// custody here. It is not a provider teardown receipt, so the honest
			// HTTP outcome is accepted/pending until the exact generation-bound
			// job seals provider-native absence evidence.
			status = http.StatusAccepted
		}
		return httpx.Success(e, status, resp)
	}
}

// monthlyRuntimeReconnectHandler re-runs enrollment for a stalled managed
// runtime, converging it back toward enrolled.
func monthlyRuntimeReconnectHandler(svc *monthlyruntime.Service) func(*httpx.Event) error {
	return func(e *httpx.Event) error {
		userID, ok := authenticatedUserID(e)
		if !ok {
			return httpx.Unauthorized(e, "Authentication required")
		}
		resp, err := svc.Reconnect(e.Request.Context(), monthlyruntime.ActionRequest{
			TenantID: monthlyRuntimeTenantID(e, userID),
			UserID:   userID,
			LeaseID:  vmlease.LeaseID(strings.TrimSpace(e.Request.PathValue("id"))),
		})
		if err != nil {
			return monthlyRuntimeError(e, err)
		}
		return httpx.Success(e, http.StatusOK, resp)
	}
}

// monthlyRuntimeDecommissionRequested parses the optional force/generation
// request. A missing or empty body keeps force=false for backward-compatible
// bodyless requests; malformed, ambiguous, oversized, or unknown JSON is
// rejected rather than silently downgraded to a non-force action.
type monthlyRuntimeDecommissionInput struct {
	Force                            bool   `json:"force"`
	ExpectedResourceGenerationDigest string `json:"expected_resource_generation_digest"`
}

func monthlyRuntimeDecommissionRequested(e *httpx.Event) (monthlyRuntimeDecommissionInput, error) {
	var input monthlyRuntimeDecommissionInput
	if e == nil || e.Request == nil {
		return input, errors.New("monthly runtime decommission request is unavailable")
	}
	if e.Request.Body != nil {
		raw, err := io.ReadAll(http.MaxBytesReader(e.Response, e.Request.Body, 4096))
		if err != nil {
			return input, err
		}
		raw = bytes.TrimSpace(raw)
		if len(raw) > 0 {
			if raw[0] != '{' {
				return input, errors.New("monthly runtime decommission body must be a JSON object")
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&input); err != nil {
				return input, err
			}
			var trailing any
			if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
				return input, errors.New("monthly runtime decommission body must contain one JSON object")
			}
		}
	}
	input.ExpectedResourceGenerationDigest = strings.TrimSpace(input.ExpectedResourceGenerationDigest)
	if raw := strings.TrimSpace(e.Request.URL.Query().Get("force")); raw != "" {
		force, err := strconv.ParseBool(raw)
		if err != nil {
			return input, err
		}
		input.Force = force
	}
	return input, nil
}

// monthlyRuntimeDecommissionError maps decommission-specific service errors to
// structured HTTP responses, falling back to the generic mapper.
func monthlyRuntimeDecommissionError(e *httpx.Event, svc *monthlyruntime.Service, err error, leaseID string) error {
	switch {
	case errors.Is(err, monthlyruntime.ErrDecommissionBlockedProtected):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"This server is protected and cannot be decommissioned",
			monthlyruntime.DecommissionProtectedDetails("", leaseID))
	case errors.Is(err, monthlyruntime.ErrDecommissionBlockedUnreachable):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"Cannot reach the server to decommission it",
			monthlyruntime.DecommissionUnreachableDetails("", leaseID, svc.ForceDecommissionReady()))
	case errors.Is(err, monthlyruntime.ErrReconciliationUnavailable):
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Force decommission is temporarily unavailable", nil)
	case errors.Is(err, vmleases.ErrResourceGenerationSuperseded):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"The server resource generation changed; refresh and retry", nil)
	case errors.Is(err, vmleases.ErrResourceGenerationDigest):
		return httpx.BadRequest(e, "expected_resource_generation_digest must be a lowercase SHA-256 digest")
	default:
		return monthlyRuntimeError(e, err)
	}
}

func monthlyRuntimeOperationsHandler(svc *monthlyruntime.Service) func(*httpx.Event) error {
	return func(e *httpx.Event) error {
		userID, ok := authenticatedUserID(e)
		if !ok {
			return httpx.Unauthorized(e, "Authentication required")
		}
		limit := 50
		if raw := strings.TrimSpace(e.Request.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 {
				return httpx.BadRequest(e, "limit must be a positive integer")
			}
			limit = parsed
		}
		events, err := svc.Operations(e.Request.Context(), monthlyruntime.OperationsRequest{
			TenantID: monthlyRuntimeTenantID(e, userID),
			UserID:   userID,
			LeaseID:  vmlease.LeaseID(strings.TrimSpace(e.Request.PathValue("id"))),
			Limit:    limit,
		})
		if err != nil {
			return monthlyRuntimeError(e, err)
		}
		return httpx.Success(e, http.StatusOK, events)
	}
}

func monthlyRuntimeTenantID(e *httpx.Event, fallback string) string {
	if e == nil || e.Request == nil {
		return strings.TrimSpace(fallback)
	}
	if id := identity.FromContext(e.Request.Context()); id != nil && strings.TrimSpace(id.OrgID) != "" {
		return strings.TrimSpace(id.OrgID)
	}
	if e.Auth != nil {
		if orgID := strings.TrimSpace(e.Auth.GetString("org_id")); orgID != "" {
			return orgID
		}
	}
	return strings.TrimSpace(fallback)
}

func monthlyRuntimeError(e *httpx.Event, err error) error {
	switch {
	case errors.Is(err, monthlyruntime.ErrForbidden):
		return httpx.Forbidden(e, "Forbidden")
	case errors.Is(err, monthlyruntime.ErrDemoRestricted):
		return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
			"This action is disabled on the kombify demo account", demoRestrictedDetails("runtime_ssh"))
	case errors.Is(err, monthlyruntime.ErrDecommissionBlockedProtected):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"This server is protected and cannot be decommissioned",
			monthlyruntime.DecommissionProtectedDetails("", ""))
	case errors.Is(err, monthlyruntime.ErrFeatureDisabled):
		return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden, "Monthly Runtime is disabled", nil)
	case errors.Is(err, monthlyruntime.ErrInvalidLease):
		return httpx.BadRequest(e, "Lease is not a monthly runtime lease")
	case errors.Is(err, monthlyruntime.ErrEnrollmentPending):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Monthly Runtime enrollment is not complete", nil)
	case errors.Is(err, monthlyruntime.ErrCustodyResolutionConfirmation):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"Confirm that the provider resource was removed before resolving custody", nil)
	case errors.Is(err, monthlyruntime.ErrCustodyResolutionProviderManaged):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"Provider-managed custody must be decommissioned through the managed lifecycle", nil)
	case errors.Is(err, monthlyruntime.ErrRuntimeClient):
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Monthly Runtime runtime client is not configured", nil)
	case errors.Is(err, monthlyruntime.ErrCleanupReadbackUnavailable):
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Managed runtime cleanup readback is temporarily unavailable", nil)
	case errors.Is(err, monthlyruntime.ErrExecutionAuthorityInactive):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"Managed runtime is not under active provider-control custody", nil)
	case errors.Is(err, monthlyruntime.ErrNativeRuntimeActionUnsupported):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
			"Managed runtime action is not supported by the active provider lifecycle",
			monthlyruntime.NativeRuntimeActionUnsupportedDetails(err))
	case errors.Is(err, vmleases.ErrNotFound):
		return httpx.NotFound(e, "Lease not found")
	case errors.Is(err, vmleases.ErrTenantRequired):
		return httpx.BadRequest(e, "tenant_id required")
	case errors.Is(err, vmleases.ErrResourceGenerationSuperseded):
		return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Lease resource generation changed", nil)
	case errors.Is(err, vmleases.ErrResourceGenerationDigest):
		return httpx.BadRequest(e, "expected_resource_generation_digest must be a lowercase SHA-256 digest")
	default:
		return httpx.Error(e, http.StatusBadGateway, ksapi.ErrCodeUnavailable, "Monthly Runtime operation failed", nil)
	}
}
