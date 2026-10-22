package guardreach

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/kombifyio/techstack/pkg/guardidentity"
)

// Document states published to a Guard and to the server read model.
const (
	StateNotEnrolled = "not_enrolled"
	// StatePending is reach_pending: enrollment is fine, reach is not done yet.
	StatePending = "pending"
	// StateFailed is reach_failed: a registration was refused; the reason code
	// says which. Guard enrollment and execution are unaffected.
	StateFailed = "failed"
	// StateReady is "Reachable and governed": kombify.me answers for the
	// server and Connect lists it.
	StateReady = "ready"
	// StateNotEnabled is a server the owner has not published at a kombify.me
	// address; the panel offers "Enable".
	StateNotEnabled = "not_enabled"
	StateRevoked    = "revoked"
)

var relayReasonPattern = regexp.MustCompile(`^[a-z0-9_.-]{0,64}$`)

// ProofSubmission is one host proof a Guard mints for a verifier audience.
type ProofSubmission struct {
	Audience   string `json:"audience"`
	Generation int64  `json:"generation"`
	Proof      string `json:"proof"`
}

// RelayStatus is the Guard's own report of its relay connection.
type RelayStatus struct {
	State      string `json:"state"`
	ReasonCode string `json:"reason_code,omitempty"`
	Generation int64  `json:"generation,omitempty"`
}

// ExchangeRequest is what a Guard sends on each reach exchange.
type ExchangeRequest struct {
	Proofs []ProofSubmission `json:"proofs,omitempty"`
	Relay  *RelayStatus      `json:"relay,omitempty"`
}

// ProofRequest asks the Guard to mint one host proof with exactly these claims.
type ProofRequest struct {
	Audience   string `json:"audience"`
	TenantID   string `json:"tenant_id"`
	ServerID   string `json:"server_id"`
	WorkerID   string `json:"worker_id"`
	Generation int64  `json:"generation"`
}

// RelayTarget tells the Guard where and as whom to connect its relay. It is
// published only while kombify.me holds the binding for the current generation.
type RelayTarget struct {
	State      string `json:"state"`
	RelayURL   string `json:"relay_url,omitempty"`
	Audience   string `json:"audience,omitempty"`
	TenantID   string `json:"tenant_id,omitempty"`
	ServerID   string `json:"server_id,omitempty"`
	WorkerID   string `json:"worker_id,omitempty"`
	Generation int64  `json:"generation,omitempty"`
	Address    string `json:"address,omitempty"`
	// TargetPort is the one loopback port the owner published; the Guard
	// relays to it and nothing else on the host.
	TargetPort int `json:"target_port,omitempty"`
}

// Document is the reach document a Guard receives. It carries no credential.
type Document struct {
	State         string         `json:"state"`
	Generation    int64          `json:"generation,omitempty"`
	KombifyMe     *RelayTarget   `json:"kombify_me,omitempty"`
	ProofRequests []ProofRequest `json:"proof_requests,omitempty"`
}

// Exchange handles one Guard exchange: it records the Guard's relay report,
// verifies and forwards any requested host proof, and answers with the
// current reach document. A failure downstream is recorded on the row and
// never turned into an error for the Guard.
func (s *Service) Exchange(ctx context.Context, tenantID, workerID string, req ExchangeRequest) (*Document, error) {
	row, err := s.cfg.Store.Get(ctx, tenantID, workerID)
	if errors.Is(err, ErrNotFound) {
		return &Document{State: StateNotEnrolled}, nil
	}
	if err != nil {
		return nil, err
	}
	now := s.now()
	if report := req.Relay; report != nil {
		if _, err := s.cfg.Store.Update(ctx, tenantID, workerID, func(r *Row) bool {
			return applyRelayReport(r, *report, now)
		}); err != nil {
			return nil, err
		}
	}
	for _, submission := range req.Proofs {
		if submission.Audience != guardidentity.AudienceConnect || submission.Generation != row.Generation {
			continue
		}
		if err := s.registerConnect(ctx, tenantID, workerID, submission.Proof); err != nil {
			s.log.Warn("guard_reach_connect_registration_failed", "worker_id", workerID, "error", err.Error())
		}
		break
	}
	row, err = s.cfg.Store.Get(ctx, tenantID, workerID)
	if err != nil {
		return nil, err
	}
	return s.document(*row, now), nil
}

func applyRelayReport(r *Row, report RelayStatus, now time.Time) bool {
	switch report.State {
	case "connected", "connecting", "denied", "stopped":
	default:
		return false
	}
	if report.Generation != r.Generation || !relayReasonPattern.MatchString(report.ReasonCode) {
		return false
	}
	r.Relay = RelayReport{State: report.State, ReasonCode: report.ReasonCode, Generation: report.Generation, ReportedAt: &now}
	return true
}

func (s *Service) document(row Row, now time.Time) *Document {
	doc := &Document{State: Summary(row), Generation: row.Generation}
	if row.Desired == DesiredRevoked || row.Generation < 1 {
		return doc
	}
	if row.Public.Enabled && row.Me.registeredAt(row.Generation) {
		doc.KombifyMe = &RelayTarget{
			State: string(TargetRegistered), RelayURL: s.cfg.RelayURL, Audience: guardidentity.AudienceRelay,
			TenantID: row.TenantID, ServerID: row.ServerID, WorkerID: row.WorkerID, Generation: row.Generation, Address: row.Me.Ref,
			TargetPort: row.Public.Port,
		}
	}
	if row.ProofWanted(now) {
		doc.ProofRequests = []ProofRequest{{
			Audience: guardidentity.AudienceConnect, TenantID: row.ConnectTenantID, ServerID: row.ServerID,
			WorkerID: row.WorkerID, Generation: row.Generation,
		}}
	}
	return doc
}

// Summary is the overall reach state of a row.
func Summary(row Row) string {
	switch {
	case row.Desired == DesiredRevoked:
		return StateRevoked
	case row.Generation < 1:
		return StatePending
	case !row.Public.Enabled && row.Me.State != TargetFailed:
		return StateNotEnabled
	case row.Me.registeredAt(row.Generation) && row.Connect.registeredAt(row.Generation):
		return StateReady
	case row.Me.State == TargetFailed || row.Connect.State == TargetFailed:
		return StateFailed
	}
	return StatePending
}

// Applicability of one reach target to the server's readiness.
const (
	// Applicable: the target must be registered for the current generation
	// before the server reads as reachable and governed.
	Applicable = "applicable"
	// NotApplicable: the server is not held to the target. The kombify.me
	// address is not applicable to an intentionally private server (the owner
	// has not published it); Connect is not applicable to a tenant without a
	// Connect namespace.
	NotApplicable = "not_applicable"
)

// MeApplicability is the scope of the public kombify.me address: only an
// owner-published server is held to it, so a private server is never treated
// as exposed.
func MeApplicability(row Row) string {
	if row.Public.Enabled {
		return Applicable
	}
	return NotApplicable
}

// ConnectApplicability is the scope of the governed Connect route. Every
// server of a tenant with a Connect namespace is held to it, published or
// private: opting out of public reach never opts out of governance.
func ConnectApplicability(row Row) string {
	if row.ConnectTenantID != "" {
		return Applicable
	}
	return NotApplicable
}

// ReadinessState is the reach state of the server read model. It equals
// Summary except for a server the owner keeps private: Summary calls it
// not_enabled whatever its governed route does, while readiness calls it
// not_enabled only once the governed route is satisfied (registered for the
// current generation) or not applicable, and otherwise pending or failed by
// the Connect registration. The Guard document keeps Summary.
func ReadinessState(row Row) string {
	state := Summary(row)
	if state != StateNotEnabled || ConnectApplicability(row) != Applicable || row.Connect.registeredAt(row.Generation) {
		return state
	}
	if row.Connect.State == TargetFailed {
		return StateFailed
	}
	return StatePending
}

// View is the reach section of the server read model, the facts the Guard
// panel renders as "Reachable and governed".
type View struct {
	State      string     `json:"state"`
	Generation int64      `json:"generation,omitempty"`
	KombifyMe  ViewTarget `json:"kombify_me"`
	Connect    ViewTarget `json:"connect"`
	Relay      *ViewRelay `json:"relay,omitempty"`
	// Public is the owner's publication decision; Enabled=false is what the
	// panel shows as "Not enabled" with an enable action.
	Public    ViewPublic `json:"public"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// ViewPublic is the owner's choice of what a server publishes at kombify.me.
type ViewPublic struct {
	Enabled    bool       `json:"enabled"`
	Service    string     `json:"service,omitempty"`
	Scheme     string     `json:"scheme,omitempty"`
	TargetPort int        `json:"target_port,omitempty"`
	EnabledBy  string     `json:"enabled_by,omitempty"`
	EnabledAt  *time.Time `json:"enabled_at,omitempty"`
}

// ViewTarget is one registration as shown to the owner.
type ViewTarget struct {
	State string `json:"state"`
	// Applicability is Applicable or NotApplicable: whether readiness holds
	// the server to this target.
	Applicability string `json:"applicability"`
	Address       string `json:"address,omitempty"`
	NodeID        string `json:"node_id,omitempty"`
	ReasonCode    string `json:"reason_code,omitempty"`
}

// ViewRelay is the Guard's own report of its relay connection, with its age.
type ViewRelay struct {
	State      string     `json:"state"`
	ReasonCode string     `json:"reason_code,omitempty"`
	ReportedAt *time.Time `json:"reported_at,omitempty"`
}

// ViewOf renders one row for the owner. Stale registrations (an older
// generation than the binding) read as pending, never as reachable.
func ViewOf(row Row) View {
	view := View{State: ReadinessState(row), Generation: row.Generation, UpdatedAt: row.UpdatedAt}
	if row.Public.Enabled {
		view.Public = ViewPublic{
			Enabled: true, Service: row.Public.Service, Scheme: row.Public.Scheme, TargetPort: row.Public.Port,
			EnabledBy: row.Public.EnabledBy, EnabledAt: row.Public.EnabledAt,
		}
	}
	show := func(target Target) ViewTarget {
		out := ViewTarget{State: string(TargetPending), ReasonCode: target.ReasonCode}
		switch {
		case row.Desired == DesiredRevoked && target.State == TargetRevoked:
			out.State = string(TargetRevoked)
		case target.registeredAt(row.Generation):
			out.State = string(TargetRegistered)
		case target.State == TargetNotEnabled && !row.Public.Enabled:
			out.State = string(TargetNotEnabled)
		case target.State == TargetFailed:
			out.State = string(TargetFailed)
		}
		if out.State == string(TargetRegistered) {
			out.ReasonCode = ""
		}
		return out
	}
	view.KombifyMe, view.Connect = show(row.Me), show(row.Connect)
	view.KombifyMe.Applicability, view.Connect.Applicability = MeApplicability(row), ConnectApplicability(row)
	if view.KombifyMe.State == string(TargetRegistered) {
		view.KombifyMe.Address = row.Me.Ref
	}
	if view.Connect.State == string(TargetRegistered) {
		view.Connect.NodeID = row.Connect.Ref
	}
	if row.Relay.State != "" && row.Relay.Generation == row.Generation {
		view.Relay = &ViewRelay{State: row.Relay.State, ReasonCode: row.Relay.ReasonCode, ReportedAt: row.Relay.ReportedAt}
	}
	return view
}
