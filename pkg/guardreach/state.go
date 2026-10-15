// Package guardreach is the control-plane side of kombify Guard reach: for a
// Guard whose host key is bound it makes the installation reachable at its
// kombify.me address (kombify-Me relay binding) and lists the server as a
// Connect resource (Gateway techstack-node), and it revokes both everywhere
// when the Guard is detached, retired or revoked from either side.
//
// The control plane keeps execution authority: nothing here executes on a
// server, and a reach failure never blocks Guard enrollment; it is reported as
// reach_pending or reach_failed on the server read model.
package guardreach

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Desired is the lifecycle the control plane wants for one Guard's reach.
type Desired string

const (
	DesiredActive  Desired = "active"
	DesiredRevoked Desired = "revoked"
)

// TargetState is the state of one downstream registration.
type TargetState string

const (
	TargetPending TargetState = "pending"
	// TargetNotEnabled is a kombify.me address the owner has not published: no
	// route exists for the server and nothing is served at a public address.
	TargetNotEnabled TargetState = "not_enabled"
	TargetRegistered TargetState = "registered"
	TargetFailed     TargetState = "failed"
	TargetRevoked    TargetState = "revoked"
)

// Stable reason codes recorded on a target. They name the failed invariant and
// never carry a credential, proof or response body.
const (
	ReasonKeyNotBound         = "guard_key_not_bound"
	ReasonTenantUnmappable    = "connect_tenant_unmappable"
	ReasonProofPending        = "host_proof_pending"
	ReasonProofRejected       = "host_proof_rejected"
	ReasonMeUnavailable       = "kombify_me_unavailable"
	ReasonMeDenied            = "kombify_me_denied"
	ReasonGatewayUnavailable  = "connect_unavailable"
	ReasonGatewayDenied       = "connect_denied"
	ReasonLayoutUnsupported   = "reach_layout_unsupported"
	ReasonRevokeIncomplete    = "revocation_incomplete"
	ReasonIdentityUnresolved  = "identity_unresolved"
	ReasonNotEnabled          = "public_reach_not_enabled"
	maxReasonLength           = 96
	placementManaged          = "managed"
	placementBYO              = "byo"
	placementLocal            = "local"
	ownerRolesMax             = 8
	connectTenantPersonalPref = "usr:"
)

// Row is the persisted reach state of one Guard worker (one server).
type Row struct {
	TenantID       string
	WorkerID       string
	ServerID       string
	StackID        string
	OwnerSubjectID string
	DisplayName    string
	Placement      string
	// ConnectTenantID is the tenant id Gateway derives for this owner; empty
	// when the tenant has no Connect namespace (the server then is not listed).
	ConnectTenantID string
	// OwnerRoles is the staff-role subset of the owner's Auth0 roles, captured
	// when the owner paired the server. Gateway needs it only while the Connect
	// rollout cohort is staff.
	OwnerRoles []string
	Desired    Desired

	KeyThumbprint   string
	CredentialEpoch int64
	// Generation is the binding generation sent to both registries and minted
	// into every host proof. It rises whenever the Guard key or pairing
	// changes, never falls.
	Generation int64

	Me      Target
	Connect Target

	// Public is the owner's decision to publish this server at a kombify.me
	// address. It is off until the owner enables it and names the service.
	Public PublicReach

	// Relay is what the Guard last reported about its own relay connection.
	Relay RelayReport

	// RevokeReason is the reason both registries receive when the row is revoked.
	RevokeReason  string
	Attempts      int
	NextAttemptAt time.Time
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// PublicReach is the owner's opt-in to publish one server at a public kombify.me
// address. Nothing is published, and no route exists, until Enabled is set with
// an explicit service and port: there is no default origin. The relay
// authenticates the Guard, never the visitor, so the owner must decide what
// answers on the named port.
type PublicReach struct {
	Enabled bool
	// Service is the owner-chosen route label (kombify.me route is
	// <base>-<service>-<server suffix>).
	Service string
	// Scheme is http or https as the service listens on the Guard host.
	Scheme string
	// Port is the TCP port of the service on the Guard host's loopback.
	Port      int
	EnabledBy string
	EnabledAt *time.Time
}

var (
	publicServicePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,12}[a-z0-9]$|^[a-z]$`)
	// ErrPublicReachInvalid is a validation failure of an owner's reach request.
	ErrPublicReachInvalid = errors.New("guardreach: public reach needs a service name and a port between 1 and 65535")
)

// Normalize validates an enabled request and fills the scheme default. A
// disabled request carries no service or port.
func (p PublicReach) Normalize() (PublicReach, error) {
	if !p.Enabled {
		return PublicReach{}, nil
	}
	p.Service = strings.ToLower(strings.TrimSpace(p.Service))
	p.Scheme = strings.ToLower(strings.TrimSpace(p.Scheme))
	if p.Scheme == "" {
		p.Scheme = "http"
	}
	if !publicServicePattern.MatchString(p.Service) || (p.Scheme != "http" && p.Scheme != "https") || p.Port < 1 || p.Port > 65535 {
		return PublicReach{}, ErrPublicReachInvalid
	}
	return p, nil
}

// TargetAddr is the loopback origin the route forwards to. The host is fixed:
// reach publishes a service of the Guard host, never another machine.
func (p PublicReach) TargetAddr() string {
	return p.Scheme + "://127.0.0.1:" + strconv.Itoa(p.Port)
}

// Target is the state of one downstream registration for one generation.
type Target struct {
	State      TargetState
	Generation int64
	// Ref is the kombify.me address for Me and the node id for Connect.
	Ref        string
	ReasonCode string
	UpdatedAt  *time.Time
	// RetryAt gates a retry that is driven from outside the reconciler (the
	// Connect registration waits for a Guard-minted proof).
	RetryAt *time.Time
}

// RelayReport is the Guard's report of its relay connection.
type RelayReport struct {
	State      string
	ReasonCode string
	Generation int64
	ReportedAt *time.Time
}

// Registered reports whether the target serves the row's current generation.
func (t Target) registeredAt(generation int64) bool {
	return t.State == TargetRegistered && t.Generation == generation
}

// ProofWanted reports whether the control plane waits for the Guard to hand it
// a Connect host proof for the current generation.
func (r Row) ProofWanted(now time.Time) bool {
	return r.Desired == DesiredActive && r.Generation >= 1 && r.ConnectTenantID != "" &&
		!r.Connect.registeredAt(r.Generation) && r.Connect.State != TargetRevoked &&
		(r.Connect.State == TargetPending || r.Connect.RetryAt == nil || !now.Before(*r.Connect.RetryAt))
}

var (
	// ErrTenantUnmappable means the tenant has no Connect namespace.
	ErrTenantUnmappable = errors.New("guardreach: tenant has no Connect namespace")

	connectStackIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	staffRoles            = map[string]struct{}{"global_admin": {}, "admin": {}, "developer": {}, "manager": {}}
)

// ConnectTenant maps a Techstack tenant to the tenant id Gateway derives for
// the same owner: the Auth0 organization id when the Gateway JWT carries one
// (org_*), otherwise usr:<subject>. The legacy naked-subject tenant is the
// pre-standardization form of usr:<subject>. Any other tenant (for example the
// shared demo tenant) has no Connect namespace, so the mapping fails closed
// instead of guessing a tenant that would list the server under another scope.
func ConnectTenant(tenantID, ownerSubject string) (string, error) {
	tenantID, ownerSubject = strings.TrimSpace(tenantID), strings.TrimSpace(ownerSubject)
	if tenantID == "" || ownerSubject == "" {
		return "", ErrTenantUnmappable
	}
	switch {
	case strings.HasPrefix(tenantID, "org_"):
		return tenantID, nil
	case tenantID == connectTenantPersonalPref+ownerSubject:
		return tenantID, nil
	case tenantID == ownerSubject:
		return connectTenantPersonalPref + ownerSubject, nil
	}
	return "", ErrTenantUnmappable
}

// StaffRoles keeps the owner roles Gateway's staff cohort gate reads and drops
// everything else, so the row stores no more identity than the gate needs.
func StaffRoles(roles []string) []string {
	out := make([]string, 0, len(roles))
	seen := map[string]struct{}{}
	for _, role := range roles {
		role = strings.ToLower(strings.TrimSpace(role))
		if _, ok := staffRoles[role]; !ok {
			continue
		}
		if _, dup := seen[role]; dup {
			continue
		}
		seen[role] = struct{}{}
		out = append(out, role)
		if len(out) == ownerRolesMax {
			break
		}
	}
	return out
}

// gatewayStackID is the stack id Connect stores; Gateway accepts a narrower
// alphabet than Techstack stack names, so anything else is replaced by a
// stable digest instead of being sent and refused.
func gatewayStackID(stackID, serverID string) string {
	stackID = strings.TrimSpace(stackID)
	if connectStackIDPattern.MatchString(stackID) {
		return stackID
	}
	if connectStackIDPattern.MatchString(strings.TrimSpace(serverID)) && stackID == "" {
		return strings.TrimSpace(serverID)
	}
	sum := sha256.Sum256([]byte("kombify.guard/connect-stack/v1\x00" + stackID + "\x00" + serverID))
	return "s-" + hex.EncodeToString(sum[:])[:32]
}

// routeServiceName is the kombify.me service label of one server's public
// route: the owner's service name plus a short per-server digest, so two servers
// of one stack can both publish "web" without colliding, and no name or account
// data is derived.
func routeServiceName(tenantID, serverID, service string) string {
	sum := sha256.Sum256([]byte("kombify.guard/reach-route/v1\x00" + tenantID + "\x00" + serverID))
	return service + "-" + hex.EncodeToString(sum[:])[:4]
}

func clampReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if len(reason) > maxReasonLength {
		return reason[:maxReasonLength]
	}
	return reason
}

func normalizePlacement(placement string) string {
	switch strings.ToLower(strings.TrimSpace(placement)) {
	case placementManaged:
		return placementManaged
	case placementLocal:
		return placementLocal
	}
	return placementBYO
}
