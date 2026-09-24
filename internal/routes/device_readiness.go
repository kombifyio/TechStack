package routes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/devicereadiness"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// Device readiness over HTTP, for the case where the control plane itself is
// inside the network the device is on.
//
// These routes are deliberately not scoped to a server: readiness happens
// before a device is enrolled, so there is usually no server to scope them to.
// They are also deliberately not registered on a hosted control plane. A
// service somewhere else cannot reach a machine in someone's home, and a shared
// service that lent devices a route out would be an open relay rather than a
// repair. The same capability reaches those devices through `techstack device`
// on the operator's own computer.

// DeviceReadinessRouteConfig decides whether the routes exist at all.
type DeviceReadinessRouteConfig struct {
	// LocalDeployment is true when this control plane runs where the operator
	// is, which is the only place these routes make sense.
	LocalDeployment bool
	// Entitled reports whether this owner may prepare devices. A nil checker
	// denies, because an unanswerable entitlement question is not a yes.
	Entitled func(ctx context.Context, ownerID, capability string) (bool, error)
	// ControlPlaneHost is the origin a probed device is asked to reach, so the
	// report says whether the device could actually enroll.
	ControlPlaneHost string
}

// Capabilities gated here. Both are cost-free but neither is harmless: one runs
// commands on a machine, the other lends it a way onto the network.
const (
	CapabilityDevicePrepare = "techstack.device.prepare"
	CapabilityDeviceEgress  = "techstack.device.egress"
)

type deviceReadinessHandlers struct {
	cfg DeviceReadinessRouteConfig
}

// RegisterDeviceReadinessRoutes wires the readiness surface when this
// deployment is one that can reach the operator's devices.
func RegisterDeviceReadinessRoutes(r *httpx.Router, cfg DeviceReadinessRouteConfig) {
	if !cfg.LocalDeployment {
		return
	}
	h := deviceReadinessHandlers{cfg: cfg}
	r.POST("/api/v1/device-readiness/probe", h.probe)
	r.POST("/api/v1/device-readiness/prepare", h.prepare)
}

// deviceConnection is how the caller says which machine to look at. The
// credential is used for one request and never persisted: this control plane
// stores no way back into a machine it does not own.
type deviceConnection struct {
	Host       string `json:"host"`
	Port       int    `json:"port,omitempty"`
	User       string `json:"user"`
	PrivateKey string `json:"private_key,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
	Password   string `json:"password,omitempty"`
	// HostKeyFingerprint pins the key the machine must present. The first
	// request may omit it, and the response then carries what was seen so the
	// operator can confirm it and pin it on the next one.
	HostKeyFingerprint string `json:"host_key_fingerprint,omitempty"`
}

type deviceProbeRequest struct {
	Connection deviceConnection `json:"connection"`
}

type devicePrepareRequest struct {
	Connection deviceConnection `json:"connection"`
	// Resolutions are catalog ids. Anything else is refused: the catalog is
	// what bounds what this can do to a machine.
	Resolutions []string `json:"resolutions"`
	// Interface is the interface a fix should act on, when one asks for it.
	Interface string `json:"interface,omitempty"`
	// Egress asks for a temporary way out for the machine while the fixes run.
	// It is a separate capability because it is a separate kind of authority.
	Egress bool `json:"egress,omitempty"`
}

type deviceProbeResponse struct {
	Report               devicereadiness.Report `json:"report"`
	DeviceKeyFingerprint string                 `json:"device_key_fingerprint"`
}

type devicePrepareResponse struct {
	Receipts             []devicereadiness.Receipt `json:"receipts"`
	Report               devicereadiness.Report    `json:"report"`
	DeviceKeyFingerprint string                    `json:"device_key_fingerprint"`
}

func (h deviceReadinessHandlers) probe(e *httpx.Event) error {
	ownerID, ok := h.authorize(e, CapabilityDevicePrepare)
	if !ok {
		return nil
	}
	_ = ownerID

	var body deviceProbeRequest
	if err := decodeExactJSON(e, &body); err != nil {
		return err
	}

	session, fingerprint, err := h.connect(e, body.Connection)
	if err != nil {
		return h.connectionFailure(e, err)
	}
	defer func() { _ = session.Close() }()

	facts, err := session.Probe(e.Request.Context(), h.probeEnvironment())
	if err != nil {
		return httpx.Error(e, http.StatusBadGateway, ksapi.ErrCodeUnavailable, "The device could not be looked at: "+err.Error(), nil)
	}
	return httpx.Success(e, http.StatusOK, deviceProbeResponse{
		Report:               devicereadiness.Evaluate(facts),
		DeviceKeyFingerprint: fingerprint(),
	})
}

func (h deviceReadinessHandlers) prepare(e *httpx.Event) error {
	ownerID, ok := h.authorize(e, CapabilityDevicePrepare)
	if !ok {
		return nil
	}
	_ = ownerID

	var body devicePrepareRequest
	if err := decodeExactJSON(e, &body); err != nil {
		return err
	}
	if len(body.Resolutions) == 0 {
		return httpx.BadRequest(e, "Name at least one fix to carry out", nil)
	}
	if body.Egress {
		if _, ok := h.authorize(e, CapabilityDeviceEgress); !ok {
			return nil
		}
	}

	session, fingerprint, err := h.connect(e, body.Connection)
	if err != nil {
		return h.connectionFailure(e, err)
	}
	defer func() { _ = session.Close() }()

	ctx := e.Request.Context()
	facts, err := session.Probe(ctx, h.probeEnvironment())
	if err != nil {
		return httpx.Error(e, http.StatusBadGateway, ksapi.ErrCodeUnavailable, "The device could not be looked at: "+err.Error(), nil)
	}
	if body.Egress {
		if _, err := session.OpenEgress(ctx, devicereadiness.DefaultEgressPolicy(h.cfg.ControlPlaneHost),
			facts.Proxy.Environment || facts.Proxy.APT); err != nil {
			return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, err.Error(), nil)
		}
	}

	params := devicereadiness.Parameters{Interface: body.Interface, OperatorTime: time.Now().UTC()}
	receipts := make([]devicereadiness.Receipt, 0, len(body.Resolutions))
	for _, id := range body.Resolutions {
		receipt, applyErr := session.Apply(ctx, id, params)
		receipts = append(receipts, receipt)
		if applyErr != nil {
			// The receipts already collected are the answer to what happened,
			// so they are returned rather than discarded with the error.
			return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, applyErr.Error(), map[string]any{
				"receipts": receipts,
			})
		}
	}

	// Looking again is the only honest way to say whether it worked.
	after, err := session.Probe(ctx, h.probeEnvironment())
	if err != nil {
		return httpx.Error(e, http.StatusBadGateway, ksapi.ErrCodeUnavailable,
			"The fixes completed, but the device could not be checked again: "+err.Error(), map[string]any{
				"reason_code":            "device_observation_failed",
				"receipts":               receipts,
				"device_key_fingerprint": fingerprint(),
			})
	}
	return httpx.Success(e, http.StatusOK, devicePrepareResponse{
		Receipts:             receipts,
		Report:               devicereadiness.Evaluate(after),
		DeviceKeyFingerprint: fingerprint(),
	})
}

// authorize answers who is asking and whether they may. It writes the denial
// itself, so a handler that forgets to stop is not the difference between
// denied and allowed.
func (h deviceReadinessHandlers) authorize(e *httpx.Event, capability string) (string, bool) {
	ownerID, _, ok := authenticatedUser(e)
	if !ok {
		_ = httpx.Unauthorized(e, "Authentication required")
		return "", false
	}
	if _, err := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, capability); err != nil {
		_ = err
		return "", false
	}
	if h.cfg.Entitled == nil {
		_ = httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
			"Preparing a device is not available on this deployment", map[string]any{
				"reason_code":    "entitlement_unavailable",
				"user_guidance":  "This deployment cannot answer whether device preparation is included, so it is refused.",
				"required_scope": capability,
			})
		return "", false
	}
	allowed, err := h.cfg.Entitled(e.Request.Context(), ownerID, capability)
	switch {
	case err != nil:
		_ = httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
			"Whether you may prepare a device could not be established", map[string]any{
				"reason_code":    "entitlement_check_failed",
				"user_guidance":  "Try again; if it keeps failing, this deployment cannot reach its entitlement source.",
				"required_scope": capability,
			})
		return "", false
	case !allowed:
		_ = httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden,
			"Preparing a device is not included in your plan", map[string]any{
				"reason_code":    "entitlement_missing",
				"user_guidance":  "Device preparation is part of a plan this account does not have.",
				"required_scope": capability,
			})
		return "", false
	}
	return ownerID, true
}

func (h deviceReadinessHandlers) probeEnvironment() devicereadiness.ProbeEnvironment {
	return devicereadiness.ProbeEnvironment{ControlPlaneHost: h.cfg.ControlPlaneHost}
}

// connect opens the session and hands back a way to read the key the device
// presented, so the caller can pin it next time.
func (h deviceReadinessHandlers) connect(e *httpx.Event, conn deviceConnection) (*devicereadiness.Session, func() string, error) {
	if strings.TrimSpace(conn.Host) == "" || strings.TrimSpace(conn.User) == "" {
		return nil, nil, errors.New("the device address and user are required")
	}
	auth := devicereadiness.Auth{
		PrivateKeyPEM: []byte(conn.PrivateKey),
		Passphrase:    []byte(conn.Passphrase),
		Password:      conn.Password,
	}
	if strings.TrimSpace(conn.PrivateKey) == "" {
		auth.PrivateKeyPEM = nil
	}
	if strings.TrimSpace(conn.Passphrase) == "" {
		auth.Passphrase = nil
	}

	var seen string
	policy := devicereadiness.HostKeyPolicy{
		ExpectedFingerprint: strings.TrimSpace(conn.HostKeyFingerprint),
		// Nothing here can ask a person anything, so an unpinned key is
		// recorded and returned rather than confirmed. The caller decides
		// whether to trust it and pins it on the next request.
		Observe: func(fingerprint string) error {
			seen = fingerprint
			return nil
		},
	}
	session, err := devicereadiness.Connect(e.Request.Context(), devicereadiness.Target{
		Host: conn.Host, Port: conn.Port, User: conn.User,
	}, auth, policy)
	if err != nil {
		return nil, nil, err
	}
	return session, func() string {
		if seen != "" {
			return seen
		}
		return session.HostKeyFingerprint()
	}, nil
}

func (h deviceReadinessHandlers) connectionFailure(e *httpx.Event, err error) error {
	return httpx.Error(e, http.StatusBadGateway, ksapi.ErrCodeUnavailable,
		"The device could not be reached: "+err.Error(), map[string]any{
			"reason_code":   "device_unreachable",
			"user_guidance": "Check the address, the user and the credential, and that this machine can reach the device.",
		})
}

// decodeExactJSON refuses anything but one well-formed object with no fields we
// do not know. A request against a machine deserves no guessing.
func decodeExactJSON(e *httpx.Event, target any) error {
	decoder := json.NewDecoder(io.LimitReader(e.Request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return httpx.BadRequest(e, "The request body could not be read", nil)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return httpx.BadRequest(e, "The request must contain exactly one JSON object", nil)
	}
	return nil
}
