package routes

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/clientpairing"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

const (
	clientPairingIssuePath  = "/api/v1/client-pairing/issue"
	clientPairingMaxBody    = 4096
	clientPairingStatusDone = "redeemed"
)

type ClientPairingRouteConfig struct {
	Profile              ClientConnectionProfileConfig
	Store                clientpairing.Store
	DefaultTenantID      string
	TLSFingerprintSHA256 string
	Lifetime             time.Duration
	Clock                func() time.Time
}

type clientPairingRedeemRequest struct {
	Version              string `json:"version"`
	InstanceID           string `json:"instance_id"`
	TLSFingerprintSHA256 string `json:"tls_fingerprint_sha256"`
	OneTimeCode          string `json:"one_time_code"`
}

type clientPairingRedeemResponse struct {
	Version        string                   `json:"version"`
	Status         string                   `json:"status"`
	InstanceID     string                   `json:"instance_id"`
	WorkspaceID    string                   `json:"workspace_id"`
	AccountHandoff clientPairingAuthHandoff `json:"account_handoff"`
}

type clientPairingAuthHandoff struct {
	Kind     string   `json:"kind"`
	Issuer   string   `json:"issuer"`
	ClientID string   `json:"client_id"`
	Audience string   `json:"audience,omitempty"`
	Scopes   []string `json:"scopes"`
	Flow     string   `json:"flow"`
}

type clientPairingErrorEnvelope struct {
	ErrorCode    string                     `json:"error_code"`
	ReasonCode   string                     `json:"reason_code"`
	Retryable    bool                       `json:"retryable"`
	UserGuidance clientPairingErrorGuidance `json:"user_guidance"`
}

type clientPairingErrorGuidance struct {
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	NextSteps []string `json:"next_steps"`
}

// RegisterClientPairingRoutes publishes the client-pairing capability. It is
// enabled only for a fully configured HTTPS self-hosted profile with normal
// OIDC account authority. The local single-owner bootstrap path fails closed.
func RegisterClientPairingRoutes(r *httpx.Router, cfg ClientPairingRouteConfig) {
	if r == nil {
		return
	}
	r.POST(clientPairingIssuePath, clientPairingIssueHandler(cfg))
	r.POST(clientpairing.RedeemPath, clientPairingRedeemHandler(cfg))
}

func clientPairingIssueHandler(cfg ClientPairingRouteConfig) httpx.HandlerFunc {
	return func(e *httpx.Event) error {
		userID, isAdmin, authenticated := authenticatedUser(e)
		if !authenticated {
			return writeClientPairingError(e.Response, http.StatusUnauthorized,
				"client_pairing_unauthorized", "authentication_required", false,
				"Sign in required",
				"Only a signed-in instance administrator can create a client pairing code.",
				"Sign in with the owner or administrator account and try again.")
		}
		if !isAdmin {
			return writeClientPairingError(e.Response, http.StatusForbidden,
				"client_pairing_forbidden", "admin_role_required", false,
				"Administrator access required",
				"Your account cannot authorize another client for this instance.",
				"Ask an instance administrator to create the pairing code.")
		}

		profile, service, err := clientPairingRuntime(cfg, e.Request)
		if err != nil {
			return writeClientPairingUnavailable(e.Response, cfg.Profile.DeploymentMode)
		}
		tenantID, err := clientPairingTenant(e, cfg.DefaultTenantID)
		if err != nil {
			return writeClientPairingError(e.Response, http.StatusForbidden,
				"client_pairing_forbidden", "tenant_binding_mismatch", false,
				"Tenant binding rejected",
				"The signed-in account does not belong to this instance's configured account authority.",
				"Sign in through the OIDC provider configured for this TechStack instance.")
		}

		envelope, err := service.Issue(e.Request.Context(), clientpairing.IssueRequest{
			TenantID:             tenantID,
			InstanceID:           profile.InstanceID,
			IssuedBySubjectID:    userID,
			Endpoint:             profile.BaseURL + clientpairing.RedeemPath,
			TLSFingerprintSHA256: strings.TrimSpace(cfg.TLSFingerprintSHA256),
			Lifetime:             cfg.Lifetime,
		})
		if err != nil {
			return writeClientPairingError(e.Response, http.StatusServiceUnavailable,
				"client_pairing_unavailable", "pairing_code_issue_failed", true,
				"Pairing code could not be created",
				"The instance could not persist a short-lived pairing capability.",
				"Retry once. If the problem continues, ask the instance operator to check the control-plane database.")
		}

		e.Response.Header().Set("Content-Type", "application/json")
		e.Response.Header().Set("Cache-Control", "no-store")
		e.Response.WriteHeader(http.StatusCreated)
		return json.NewEncoder(e.Response).Encode(envelope)
	}
}

func clientPairingRedeemHandler(cfg ClientPairingRouteConfig) httpx.HandlerFunc {
	return func(e *httpx.Event) error {
		profile, service, err := clientPairingRuntime(cfg, e.Request)
		if err != nil {
			return writeClientPairingUnavailable(e.Response, cfg.Profile.DeploymentMode)
		}
		var request clientPairingRedeemRequest
		if err := decodeClientPairingBody(e.Request, &request); err != nil || request.Version != clientpairing.Version {
			return writeClientPairingError(e.Response, http.StatusBadRequest,
				"client_pairing_rejected", "invalid_pairing_request", false,
				"Pairing request is invalid",
				"The pairing payload is incomplete, unsupported, or contains unknown fields.",
				"Scan the QR code again and retry with the complete v1 pairing payload.")
		}
		if request.InstanceID != profile.InstanceID || request.TLSFingerprintSHA256 != strings.TrimSpace(cfg.TLSFingerprintSHA256) {
			return writeClientPairingRedeemError(e.Response, clientpairing.ErrBindingMismatch)
		}

		redeemed, err := service.Redeem(e.Request.Context(), clientpairing.RedeemRequest{
			OneTimeCode:          request.OneTimeCode,
			ExpectedTenantID:     strings.TrimSpace(cfg.DefaultTenantID),
			InstanceID:           request.InstanceID,
			TLSFingerprintSHA256: request.TLSFingerprintSHA256,
		})
		if err != nil {
			return writeClientPairingRedeemError(e.Response, err)
		}

		response := clientPairingRedeemResponse{
			Version:     clientpairing.Version,
			Status:      clientPairingStatusDone,
			InstanceID:  profile.InstanceID,
			WorkspaceID: redeemed.TenantID,
			AccountHandoff: clientPairingAuthHandoff{
				Kind:     "oidc",
				Issuer:   profile.OIDC.Issuer,
				ClientID: profile.OIDC.ClientID,
				Audience: profile.OIDC.Audience,
				Scopes:   append([]string(nil), profile.OIDC.Scopes...),
				Flow:     profile.OIDC.Flow,
			},
		}
		e.Response.Header().Set("Content-Type", "application/json")
		e.Response.Header().Set("Cache-Control", "no-store")
		e.Response.WriteHeader(http.StatusOK)
		return json.NewEncoder(e.Response).Encode(response)
	}
}

func clientPairingRuntime(cfg ClientPairingRouteConfig, request *http.Request) (clientConnectionProfile, *clientpairing.Service, error) {
	profile, err := buildClientConnectionProfile(cfg.Profile, request)
	if err != nil || profile.DeploymentMode != clientDeploymentModeSelfHosted {
		return clientConnectionProfile{}, nil, errors.New("client pairing requires self-hosted OIDC mode")
	}
	if strings.TrimSpace(cfg.DefaultTenantID) == "" || cfg.Store == nil {
		return clientConnectionProfile{}, nil, errors.New("client pairing authority is not configured")
	}
	if err := clientpairing.ValidateBinding(profile.InstanceID, cfg.TLSFingerprintSHA256); err != nil {
		return clientConnectionProfile{}, nil, err
	}
	options := make([]clientpairing.Option, 0, 1)
	if cfg.Clock != nil {
		options = append(options, clientpairing.WithClock(cfg.Clock))
	}
	service, err := clientpairing.New(cfg.Store, options...)
	if err != nil {
		return clientConnectionProfile{}, nil, err
	}
	return profile, service, nil
}

func clientPairingTenant(e *httpx.Event, configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return "", errors.New("configured tenant is required")
	}
	if e == nil || e.Request == nil {
		return "", errors.New("request is required")
	}
	id := identity.FromContext(e.Request.Context())
	if id == nil || !id.IsAuthenticated() {
		return "", errors.New("identity is required")
	}
	if requested := strings.TrimSpace(id.OrgID); requested != "" && requested != configured {
		return "", fmt.Errorf("identity tenant %q differs from configured tenant", requested)
	}
	return configured, nil
}

func decodeClientPairingBody(request *http.Request, target any) error {
	if request == nil || request.Body == nil {
		return errors.New("request body is required")
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, clientPairingMaxBody+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("request must contain one JSON object")
	}
	return nil
}

func writeClientPairingRedeemError(w http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, clientpairing.ErrExpired):
		return writeClientPairingError(w, http.StatusGone,
			"client_pairing_rejected", "pairing_code_expired", false,
			"Pairing code expired",
			"The short-lived pairing window has ended and the code cannot be reused.",
			"Ask an instance administrator to create a new pairing code.")
	case errors.Is(err, clientpairing.ErrAlreadyConsumed):
		return writeClientPairingError(w, http.StatusConflict,
			"client_pairing_rejected", "pairing_code_already_consumed", false,
			"Pairing code already used",
			"This one-time code has already completed a redemption and every replay is denied.",
			"Ask an instance administrator to create a new pairing code for this device.")
	case errors.Is(err, clientpairing.ErrBindingMismatch):
		return writeClientPairingError(w, http.StatusConflict,
			"client_pairing_rejected", "pairing_binding_mismatch", false,
			"Instance verification failed",
			"The instance, tenant, or TLS fingerprint does not match the issued pairing envelope.",
			"Do not continue. Verify the HTTPS certificate fingerprint and scan a fresh QR code from the intended instance.")
	case errors.Is(err, clientpairing.ErrInvalidCode), errors.Is(err, clientpairing.ErrInvalidRequest):
		return writeClientPairingError(w, http.StatusBadRequest,
			"client_pairing_rejected", "invalid_pairing_code", false,
			"Pairing code is invalid",
			"The code is malformed or was not issued by this account authority.",
			"Scan a fresh QR code from the intended TechStack instance.")
	default:
		return writeClientPairingError(w, http.StatusServiceUnavailable,
			"client_pairing_unavailable", "pairing_redemption_failed", true,
			"Pairing could not be completed",
			"The instance could not atomically verify and consume the pairing code.",
			"Retry once. If the problem continues, ask the instance operator to check the control-plane database.")
	}
}

func writeClientPairingUnavailable(w http.ResponseWriter, mode string) error {
	if strings.EqualFold(strings.TrimSpace(mode), clientDeploymentModeLocal) {
		return writeClientPairingError(w, http.StatusConflict,
			"client_pairing_unavailable", "local_multiuser_not_supported", false,
			"Team pairing is not available in local owner mode",
			"This installation currently has one local owner authority; pairing must not duplicate that owner into a second client session.",
			"Configure the HTTPS self-hosted OIDC team mode before adding a second client account.")
	}
	return writeClientPairingError(w, http.StatusServiceUnavailable,
		"client_pairing_unavailable", "client_pairing_not_configured", false,
		"Client pairing is not configured",
		"This instance is missing a safe HTTPS, TLS fingerprint, tenant, database, or native OIDC configuration.",
		"Ask the instance operator to complete the self-hosted client and account configuration.")
}

func writeClientPairingError(w http.ResponseWriter, status int, errorCode, reasonCode string, retryable bool, title, body, nextStep string) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(clientPairingErrorEnvelope{
		ErrorCode:  errorCode,
		ReasonCode: reasonCode,
		Retryable:  retryable,
		UserGuidance: clientPairingErrorGuidance{
			Title:     title,
			Body:      body,
			NextSteps: []string{nextStep},
		},
	})
}
