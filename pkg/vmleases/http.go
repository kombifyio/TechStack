package vmleases

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/kombifyio/go-common/servicecall"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

const (
	serviceCallerCloud = "cloud"
)

type HandlerConfig struct {
	ServiceAuthSecret string
	ServiceAuthNext   string
}

type Handler struct {
	service *Service
	cfg     HandlerConfig
}

func NewHandler(service *Service, cfg HandlerConfig) *Handler {
	return &Handler{service: service, cfg: cfg}
}

// ServeHTTP exposes only Cloud's fail-closed lease inventory read surface.
// Lease creation, provider execution, validation, patching, desired-spec
// retrieval, and executor callbacks have no HTTP entry point in this handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	serviceAuthSecret := strings.TrimSpace(h.cfg.ServiceAuthSecret)
	if serviceAuthSecret == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "service_auth_not_configured"})
		return
	}
	authCfg := servicecall.Config{
		ServiceName:    "techstack",
		Secret:         serviceAuthSecret,
		SecretNext:     strings.TrimSpace(h.cfg.ServiceAuthNext),
		AllowedCallers: []string{serviceCallerCloud},
		Enabled:        true,
	}
	servicecall.RequireServiceAuth(authCfg)(http.HandlerFunc(h.serveAuthed)).ServeHTTP(w, r)
}

func (h *Handler) serveAuthed(w http.ResponseWriter, r *http.Request) {
	if !cloudRuntimeLeaseSurface(r) {
		writeCloudRuntimeLeaseDenial(w, http.StatusForbidden, "cloud_surface_not_allowed", false,
			"Use the TechStack-native provider-control API for managed runtime operations.")
		return
	}
	if !h.authorizeCloudRuntimeLeaseRequest(w, r) {
		return
	}

	const prefix = "/api/v1/internal/vm-leases"
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/")
	switch {
	case r.Method == http.MethodGet && path != "" && !strings.Contains(path, "/"):
		h.handleGet(w, r, vmlease.LeaseID(path))
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
	}
}

func cloudRuntimeLeaseSurface(r *http.Request) bool {
	if r == nil {
		return false
	}
	const prefix = "/api/v1/internal/vm-leases"
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/")
	return strings.HasPrefix(r.URL.Path, prefix+"/") && r.Method == http.MethodGet && path != "" && !strings.Contains(path, "/")
}

func (h *Handler) authorizeCloudRuntimeLeaseRequest(w http.ResponseWriter, r *http.Request) bool {
	caller := servicecall.FromContext(r.Context())
	if caller == nil || caller.Service != serviceCallerCloud || caller.OnBehalfOf == nil ||
		strings.TrimSpace(caller.OnBehalfOf.Sub) == "" || strings.TrimSpace(caller.OnBehalfOf.OrgID) == "" {
		writeCloudRuntimeLeaseDenial(w, http.StatusForbidden, "tenant_context_required", false,
			"Select a Cloud workspace and retry through the governed runtime inventory API.")
		return false
	}
	if r.URL.RawQuery != "" {
		writeCloudRuntimeLeaseDenial(w, http.StatusForbidden, "tenant_injection_denied", false,
			"Do not send tenant query parameters; TechStack derives the tenant from the signed Cloud service identity.")
		return false
	}
	return true
}

func writeCloudRuntimeLeaseDenial(w http.ResponseWriter, status int, reasonCode string, retryable bool, nextStep string) {
	writeJSON(w, status, map[string]any{
		"error_code":        "techstack.runtime_lease." + reasonCode,
		"reason_code":       reasonCode,
		"required_features": []string{},
		"missing_features":  []string{},
		"retryable":         retryable,
		"user_guidance": map[string]any{
			"title":      "Runtime lease request denied",
			"body":       "TechStack could not accept this Cloud runtime lease request safely.",
			"next_steps": []string{nextStep},
		},
	})
}

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request, id vmlease.LeaseID) {
	tenantID, err := tenantIDFromRequest(r, "")
	if err != nil {
		writeJSON(w, httpStatusForError(err), map[string]string{"error": err.Error()})
		return
	}
	lease, err := h.service.Get(r.Context(), tenantID, id)
	if err != nil {
		writeJSON(w, httpStatusForError(err), map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, lease)
}

func tenantIDFromRequest(r *http.Request, explicit string) (string, error) {
	explicit = strings.TrimSpace(explicit)
	queryTenant := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	if caller := servicecall.FromContext(r.Context()); caller != nil && caller.OnBehalfOf != nil {
		oboTenant := strings.TrimSpace(caller.OnBehalfOf.OrgID)
		if oboTenant != "" {
			if explicit != "" && explicit != oboTenant {
				return "", tenantMismatchError(explicit, oboTenant)
			}
			if queryTenant != "" && queryTenant != oboTenant {
				return "", tenantMismatchError(queryTenant, oboTenant)
			}
			return oboTenant, nil
		}
	}
	if explicit != "" {
		return explicit, nil
	}
	if queryTenant != "" {
		return queryTenant, nil
	}
	return "", nil
}

func tenantMismatchError(requestTenant, tokenTenant string) error {
	return fmt.Errorf("%w: requested tenant %q does not match servicecall org %q", ErrTenantMismatch, strings.TrimSpace(requestTenant), strings.TrimSpace(tokenTenant))
}

func httpStatusForError(err error) int {
	switch {
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrTenantMismatch):
		return http.StatusForbidden
	case errors.Is(err, ErrLeaseCancelled), errors.Is(err, ErrResourceGenerationSuperseded), errors.Is(err, ErrDecommissionClaimImmutable):
		return http.StatusConflict
	case errors.Is(err, ErrUnsupportedProvider), errors.Is(err, ErrTenantRequired):
		return http.StatusBadRequest
	case errors.Is(err, ErrLeaseExecutionAuthorityUnbound):
		return http.StatusServiceUnavailable
	case errors.Is(err, ErrLeaseExecutionAuthorityConflict):
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
