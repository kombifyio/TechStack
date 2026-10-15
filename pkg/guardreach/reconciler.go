package guardreach

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/kombifyio/techstack/pkg/guardidentity"
	"github.com/kombifyio/techstack/pkg/kombifyme"
)

const (
	revocationCursorName = "gateway-techstack-node-revocations"
	revocationPageSize   = 100

	defaultPassInterval   = 15 * time.Second
	defaultRevocationPoll = 5 * time.Second
	defaultLease          = 2 * time.Minute
	settledRecheck        = 10 * time.Minute
	waitingRecheck        = 30 * time.Second
	retryBase             = 15 * time.Second
	retryCap              = 15 * time.Minute
	connectRetryable      = 2 * time.Minute
	connectPermanent      = 15 * time.Minute
	maxRowsPerPass        = 500
	passBatch             = 50

	defaultRevokeReason = "detached"
	ownerRevokeReason   = "owner_request"
	ownerProxyPrefix    = "service:techstack:"
)

// Connect is the Gateway registration surface the control plane drives.
type Connect interface {
	PutNode(ctx context.Context, in NodeUpsert) (*Node, error)
	RevokeNode(ctx context.Context, owner, connectTenant string, roles []string, serverID, reason string) (*Node, error)
	Revocations(ctx context.Context, after int64, limit int) (*RevocationPage, error)
}

// Registry is the kombify.me surface the control plane drives.
type Registry interface {
	EnsureRoute(ctx context.Context, address kombifyme.ManagedAddress, service, localAddr, description string) (Route, error)
	RegisterServer(ctx context.Context, in MeRegistration) error
	RevokeServer(ctx context.Context, tenantID, serverID, reason string) error
}

// Subject is what the control plane knows right now about one Guard worker.
type Subject struct {
	Found bool
	// WorkerActive is false for a revoked or rejected worker and for a server
	// that is being or has been decommissioned (retired).
	WorkerActive    bool
	IdentityState   string
	Thumbprint      string
	CredentialEpoch int64
}

// Identities resolves the live worker and host-key binding.
type Identities interface {
	Resolve(ctx context.Context, tenantID, workerID, serverID string) (Subject, error)
}

// AccessRevoker ends a Guard's access in the control plane (credential, worker
// and host-key binding), the same effect as an owner detaching the key binding.
type AccessRevoker interface {
	RevokeGuardAccess(ctx context.Context, tenantID, workerID string, now time.Time) error
}

// Config wires the Service.
type Config struct {
	Store      Store
	Me         Registry
	Connect    Connect
	Identities Identities
	Access     AccessRevoker
	// RelayURL is the kombify.me relay WebSocket URL handed to Guards.
	RelayURL string

	PassInterval   time.Duration
	RevocationPoll time.Duration
	Lease          time.Duration
	Now            func() time.Time
	Logger         *slog.Logger
}

// Service reconciles Guard reach.
type Service struct {
	cfg Config
	log *slog.Logger
}

// New validates the configuration.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Me == nil || cfg.Connect == nil || cfg.Identities == nil || cfg.Access == nil {
		return nil, errors.New("guardreach: store, kombify.me, Connect, identity and access revocation are all required")
	}
	if strings.TrimSpace(cfg.RelayURL) == "" {
		cfg.RelayURL = kombifyme.DefaultRelayURL
	}
	if cfg.PassInterval <= 0 {
		cfg.PassInterval = defaultPassInterval
	}
	if cfg.RevocationPoll <= 0 {
		cfg.RevocationPoll = defaultRevocationPoll
	}
	if cfg.Lease <= 0 {
		cfg.Lease = defaultLease
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Service{cfg: cfg, log: logger.With("component", "guard-reach")}, nil
}

func (s *Service) now() time.Time { return s.cfg.Now().UTC() }

// Run drives both loops until the context ends.
func (s *Service) Run(ctx context.Context) {
	if s == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.loop(ctx, s.cfg.RevocationPoll, func(ctx context.Context) error { return s.ConsumeRevocations(ctx) }, "guard_reach_revocations_failed")
	}()
	s.loop(ctx, s.cfg.PassInterval, s.ReconcileOnce, "guard_reach_pass_failed")
	<-done
}

func (s *Service) loop(ctx context.Context, interval time.Duration, pass func(context.Context) error, failure string) {
	// A dependency that stays down is logged when the error changes and once a
	// minute after that, not on every tick.
	var lastError string
	var lastLogged time.Time
	run := func() {
		err := pass(ctx)
		switch {
		case err == nil:
			lastError = ""
		case errors.Is(err, context.Canceled):
		case err.Error() != lastError || time.Since(lastLogged) >= time.Minute:
			lastError, lastLogged = err.Error(), time.Now()
			s.log.Warn(failure, "error", err.Error())
		}
	}
	run()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

// SetPublic records the owner's decision to publish, or stop publishing, one
// server at a public kombify.me address. Enabling needs an explicit service and
// port (there is no default); any change wakes the reconciler, which creates or
// withdraws the route. The server must belong to the owner the row names.
func (s *Service) SetPublic(ctx context.Context, tenantID, workerID, ownerSubject string, want PublicReach) (*Row, error) {
	normalized, err := want.Normalize()
	if err != nil {
		return nil, err
	}
	now := s.now()
	row, err := s.cfg.Store.Update(ctx, tenantID, workerID, func(r *Row) bool {
		if r.OwnerSubjectID != ownerSubject || r.Desired == DesiredRevoked {
			return false
		}
		if normalized.Enabled {
			normalized.EnabledBy, normalized.EnabledAt = ownerSubject, &now
		}
		if r.Public.Enabled == normalized.Enabled && r.Public.Service == normalized.Service &&
			r.Public.Scheme == normalized.Scheme && r.Public.Port == normalized.Port {
			return false
		}
		r.Public = normalized
		// The route changes: re-register (enable, change) or withdraw (disable).
		r.Me = Target{State: TargetPending, Generation: r.Me.Generation, Ref: r.Me.Ref}
		r.Attempts, r.NextAttemptAt = 0, now
		return true
	})
	if err != nil {
		return nil, err
	}
	if row.OwnerSubjectID != ownerSubject || row.Desired == DesiredRevoked {
		return nil, ErrNotFound
	}
	return row, nil
}

// Enroll records a Guard server for reach (desired active). It is idempotent
// and never blocks or fails enrollment of the Guard itself.
func (s *Service) Enroll(ctx context.Context, e Enrollment) error {
	if s == nil {
		return nil
	}
	return s.cfg.Store.Enroll(ctx, e)
}

// ReconcileOnce runs one bounded, keyset-paginated pass over due rows.
func (s *Service) ReconcileOnce(ctx context.Context) error {
	var after Key
	processed := 0
	for processed < maxRowsPerPass {
		keys, err := s.cfg.Store.ListDue(ctx, s.now(), after.TenantID, after.WorkerID, passBatch)
		if err != nil {
			return err
		}
		for _, key := range keys {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			after = key
			processed++
			if err := s.ReconcileKey(ctx, key); err != nil && !errors.Is(err, context.Canceled) {
				s.log.Warn("guard_reach_reconcile_failed", "tenant_id", key.TenantID, "worker_id", key.WorkerID, "error", err.Error())
			}
		}
		if len(keys) < passBatch {
			return nil
		}
	}
	return nil
}

// ReconcileKey leases one due row and reconciles it.
func (s *Service) ReconcileKey(ctx context.Context, key Key) error {
	row, claimed, err := s.cfg.Store.Claim(ctx, key.TenantID, key.WorkerID, s.now(), s.cfg.Lease)
	if err != nil || !claimed {
		return err
	}
	return s.reconcile(ctx, *row)
}

type action int

const (
	actionWait action = iota
	actionRegister
	actionRevoke
)

// decide is the pure reconciliation decision. A server is registered only
// while its host key is bound; everything that ended the Guard's identity
// (revoked, retired, missing) revokes; a revoked row returns only with a newly
// bound key or pairing.
func decide(row Row, subject Subject) action {
	active := subject.Found && subject.WorkerActive && subject.IdentityState != "revoked"
	bound := active && subject.IdentityState == "bound"
	if row.Desired == DesiredRevoked {
		if bound && (subject.Thumbprint != row.KeyThumbprint || subject.CredentialEpoch != row.CredentialEpoch) {
			return actionRegister
		}
		return actionRevoke
	}
	switch {
	case !active:
		return actionRevoke
	case !bound:
		return actionWait
	}
	return actionRegister
}

func backoff(attempts int) time.Duration {
	delay := retryBase
	for i := 1; i < attempts && delay < retryCap; i++ {
		delay *= 2
	}
	if delay > retryCap {
		delay = retryCap
	}
	return delay
}

func (s *Service) reconcile(ctx context.Context, snapshot Row) error {
	subject, err := s.cfg.Identities.Resolve(ctx, snapshot.TenantID, snapshot.WorkerID, snapshot.ServerID)
	if err != nil {
		return s.settle(ctx, snapshot, func(r *Row) { s.fail(r, ReasonIdentityUnresolved) })
	}
	switch decide(snapshot, subject) {
	case actionRevoke:
		return s.revoke(ctx, snapshot)
	case actionWait:
		return s.settle(ctx, snapshot, func(r *Row) {
			r.NextAttemptAt = s.now().Add(waitingRecheck)
			r.LastError = ReasonKeyNotBound
		})
	}
	return s.register(ctx, snapshot, subject)
}

// settle applies a guarded outcome: it is discarded when the row moved to
// another generation or desired state while the reconciler worked on it.
func (s *Service) settle(ctx context.Context, snapshot Row, apply func(*Row)) error {
	_, err := s.cfg.Store.Update(ctx, snapshot.TenantID, snapshot.WorkerID, func(r *Row) bool {
		if r.Generation != snapshot.Generation || r.Desired != snapshot.Desired {
			return false
		}
		apply(r)
		return true
	})
	return err
}

func (s *Service) fail(r *Row, reason string) {
	r.Attempts++
	r.LastError = reason
	r.NextAttemptAt = s.now().Add(backoff(r.Attempts))
}

func (s *Service) succeed(r *Row, next time.Duration) {
	r.Attempts = 0
	r.LastError = ""
	r.NextAttemptAt = s.now().Add(next)
}

func (s *Service) register(ctx context.Context, snapshot Row, subject Subject) error {
	now := s.now()
	row, err := s.cfg.Store.Update(ctx, snapshot.TenantID, snapshot.WorkerID, func(r *Row) bool {
		r.Desired, r.RevokeReason = DesiredActive, ""
		if r.Generation == 0 || r.KeyThumbprint != subject.Thumbprint || r.CredentialEpoch != subject.CredentialEpoch {
			// A new key or pairing is a new binding at a higher generation; the
			// registries replace the old binding and close its sessions.
			r.Generation++
			r.KeyThumbprint, r.CredentialEpoch = subject.Thumbprint, subject.CredentialEpoch
			// The previous route address is kept so a server that is not published
			// still has the old binding's route withdrawn.
			r.Me, r.Connect = Target{State: TargetPending, Ref: r.Me.Ref}, Target{State: TargetPending}
			r.Relay = RelayReport{}
			r.Attempts = 0
		}
		return true
	})
	if err != nil {
		return err
	}
	current := *row
	var failure string
	switch {
	case !current.Public.Enabled:
		failure = s.withdrawPublic(ctx, &current)
	case !current.Me.registeredAt(current.Generation):
		failure = s.registerMe(ctx, &current)
	}
	if current.ConnectTenantID == "" && current.Connect.State != TargetFailed {
		current.Connect = Target{State: TargetFailed, ReasonCode: ReasonTenantUnmappable, UpdatedAt: &now}
	}
	return s.settle(ctx, current, func(r *Row) {
		r.Me = current.Me
		if r.ConnectTenantID == "" && r.Connect.State != TargetFailed {
			r.Connect = current.Connect
		}
		switch {
		case failure != "":
			s.fail(r, failure)
		case !r.Connect.registeredAt(r.Generation) && r.ConnectTenantID != "":
			// Connect registers when the Guard hands over its host proof.
			s.succeed(r, waitingRecheck*2)
			r.LastError = ReasonProofPending
		default:
			s.succeed(r, settledRecheck)
		}
	})
}

// registerMe creates the server's tunnel route and binds the server, key and
// route at kombify.me. It returns a reason code on failure and updates the
// row copy in place on success.
func (s *Service) registerMe(ctx context.Context, row *Row) string {
	now := s.now()
	fail := func(reason string) string {
		row.Me = Target{State: TargetFailed, Generation: row.Generation, ReasonCode: reason, UpdatedAt: &now}
		return reason
	}
	address, err := kombifyme.NewManagedAddress(row.TenantID, row.OwnerSubjectID, firstNonEmpty(row.StackID, row.ServerID))
	if err != nil {
		return fail(ReasonMeDenied)
	}
	route, err := s.cfg.Me.EnsureRoute(ctx, address, routeServiceName(row.TenantID, row.ServerID, row.Public.Service), row.Public.TargetAddr(), "kombify Guard reach")
	if err != nil {
		return fail(meReason(err))
	}
	if err := s.cfg.Me.RegisterServer(ctx, MeRegistration{
		TenantID: row.TenantID, ServerID: row.ServerID, WorkerID: row.WorkerID, Address: address,
		KeyThumbprint: row.KeyThumbprint, Generation: row.Generation, Routes: []string{route.Name},
	}); err != nil {
		return fail(meReason(err))
	}
	row.Me = Target{State: TargetRegistered, Generation: row.Generation, Ref: route.FQDN, UpdatedAt: &now}
	return ""
}

// withdrawPublic keeps the server unpublished: it creates no route and, when a
// public route was assigned to the server's binding before (an earlier default
// route, or an owner who turned the address off), registers the binding with no
// route so nothing is served at a public address. A server that never had one
// needs no call. It returns a failure reason when the withdrawal did not land;
// the row then retries.
func (s *Service) withdrawPublic(ctx context.Context, row *Row) string {
	now := s.now()
	if row.Me.State == TargetNotEnabled && row.Me.Generation == row.Generation {
		return ""
	}
	if row.Me.State != TargetPending || row.Me.Ref != "" {
		address, err := kombifyme.NewManagedAddress(row.TenantID, row.OwnerSubjectID, firstNonEmpty(row.StackID, row.ServerID))
		if err == nil {
			err = s.cfg.Me.RegisterServer(ctx, MeRegistration{
				TenantID: row.TenantID, ServerID: row.ServerID, WorkerID: row.WorkerID, Address: address,
				KeyThumbprint: row.KeyThumbprint, Generation: row.Generation, Routes: []string{},
			})
		}
		var denial *MeError
		if err != nil && !(errors.As(err, &denial) && denial.ReasonCode == "binding_revoked") {
			reason := meReason(err)
			row.Me = Target{State: TargetFailed, Generation: row.Generation, ReasonCode: reason, UpdatedAt: &now}
			return reason
		}
	}
	row.Me = Target{State: TargetNotEnabled, Generation: row.Generation, ReasonCode: ReasonNotEnabled, UpdatedAt: &now}
	return ""
}

func meReason(err error) string {
	var denial *MeError
	if errors.As(err, &denial) {
		if denial.ReasonCode != "" {
			return "kombify_me_" + denial.ReasonCode
		}
		if denial.Retryable {
			return ReasonMeUnavailable
		}
		return ReasonMeDenied
	}
	return ReasonMeUnavailable
}

func connectReason(err error) (reason string, retryable bool) {
	var denial *GatewayError
	if errors.As(err, &denial) {
		if denial.ReasonCode != "" {
			return "connect_" + denial.ReasonCode, denial.Retryable
		}
		return ReasonGatewayDenied, denial.Retryable
	}
	return ReasonGatewayUnavailable, true
}

// revoke ends reach everywhere: kombify.me first (it closes live relay
// sessions), then Connect. Both calls are idempotent and repeat until both
// confirmed.
func (s *Service) revoke(ctx context.Context, snapshot Row) error {
	now := s.now()
	row, err := s.cfg.Store.Update(ctx, snapshot.TenantID, snapshot.WorkerID, func(r *Row) bool {
		if r.Desired == DesiredRevoked && r.RevokeReason != "" {
			return false
		}
		r.Desired = DesiredRevoked
		r.RevokeReason = firstNonEmpty(r.RevokeReason, defaultRevokeReason)
		return true
	})
	if err != nil {
		return err
	}
	current := *row
	var failure string
	if current.Me.State != TargetRevoked {
		if err := s.cfg.Me.RevokeServer(ctx, current.TenantID, current.ServerID, current.RevokeReason); err != nil {
			current.Me.State, current.Me.ReasonCode = TargetFailed, meReason(err)
			failure = ReasonRevokeIncomplete
		} else {
			current.Me = Target{State: TargetRevoked, Generation: current.Generation, UpdatedAt: &now}
		}
	}
	if current.Connect.State != TargetRevoked {
		if current.ConnectTenantID == "" {
			current.Connect = Target{State: TargetRevoked, Generation: current.Generation, UpdatedAt: &now}
		} else if _, err := s.cfg.Connect.RevokeNode(ctx, current.OwnerSubjectID, current.ConnectTenantID, current.OwnerRoles, current.ServerID, current.RevokeReason); err != nil {
			reason, _ := connectReason(err)
			current.Connect.State, current.Connect.ReasonCode = TargetFailed, reason
			failure = ReasonRevokeIncomplete
		} else {
			current.Connect = Target{State: TargetRevoked, Generation: current.Generation, UpdatedAt: &now}
		}
	}
	_, err = s.cfg.Store.Update(ctx, current.TenantID, current.WorkerID, func(r *Row) bool {
		if r.Desired != DesiredRevoked {
			// Paired again while the revoke ran: the new binding wins.
			return false
		}
		r.Me, r.Connect = current.Me, current.Connect
		if failure != "" {
			s.fail(r, failure)
		} else {
			s.succeed(r, settledRecheck)
		}
		return true
	})
	return err
}

// ConsumeRevocations reads the Gateway revocation cursor. An owner-initiated
// revoke ends the Guard's access in the control plane and revokes kombify.me;
// events below the current binding generation, for another worker or for a
// key this row never had are ignored.
func (s *Service) ConsumeRevocations(ctx context.Context) error {
	position, err := s.cfg.Store.Cursor(ctx, revocationCursorName)
	if err != nil {
		return err
	}
	for {
		page, err := s.cfg.Connect.Revocations(ctx, position, revocationPageSize)
		if err != nil {
			return err
		}
		for _, event := range page.Events {
			if err := s.applyRevocation(ctx, event); err != nil {
				// Never advance past an event that could not be applied.
				return fmt.Errorf("apply revocation %d: %w", event.Seq, err)
			}
		}
		next := page.NextCursor
		if next > position {
			if err := s.cfg.Store.AdvanceCursor(ctx, revocationCursorName, next); err != nil {
				return err
			}
			position = next
		}
		if !page.HasMore || len(page.Events) == 0 {
			return nil
		}
	}
}

func (s *Service) applyRevocation(ctx context.Context, event Revocation) error {
	keys, err := s.cfg.Store.FindByConnect(ctx, event.TenantID, event.ServerID)
	if err != nil {
		return err
	}
	for _, key := range keys {
		row, err := s.cfg.Store.Get(ctx, key.TenantID, key.WorkerID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if event.WorkerID != "" && event.WorkerID != row.WorkerID {
			continue
		}
		if event.BindingGeneration < row.Generation {
			continue
		}
		if event.BindingGeneration == row.Generation && event.KeyThumbprint != "" && event.KeyThumbprint != row.KeyThumbprint {
			continue
		}
		if row.Desired == DesiredRevoked && row.Connect.State == TargetRevoked {
			continue
		}
		if strings.HasPrefix(event.RevokedBy, ownerProxyPrefix) {
			// The echo of a revoke the control plane issued itself: it already
			// recorded the revocation before calling Gateway.
			continue
		}
		// Same effect as the owner detaching the key binding: credential,
		// worker and host-key binding end in one transaction, so the Guard
		// stops authenticating before anything else happens.
		if err := s.cfg.Access.RevokeGuardAccess(ctx, row.TenantID, row.WorkerID, s.now()); err != nil {
			return err
		}
		now := s.now()
		if _, err := s.cfg.Store.Update(ctx, row.TenantID, row.WorkerID, func(r *Row) bool {
			if event.BindingGeneration < r.Generation {
				return false
			}
			r.Desired = DesiredRevoked
			r.RevokeReason = firstNonEmpty(r.RevokeReason, revokeReasonFor(event))
			r.Connect = Target{State: TargetRevoked, Generation: r.Generation, UpdatedAt: &now}
			r.NextAttemptAt = now
			return true
		}); err != nil {
			return err
		}
	}
	return nil
}

func revokeReasonFor(event Revocation) string {
	switch event.Reason {
	case "owner_request", "detached", "stack_deleted", "guard_compromised", "other":
		return event.Reason
	}
	return ownerRevokeReason
}

// registerConnect verifies a Guard-minted host proof and registers the server
// as a Connect resource. It fails closed: it forwards nothing unless the proof
// answers the outstanding request for the row's current generation, is signed
// by the bound key and would pass Gateway's own checks.
func (s *Service) registerConnect(ctx context.Context, tenantID, workerID, proof string) error {
	now := s.now()
	row, err := s.cfg.Store.Get(ctx, tenantID, workerID)
	if err != nil {
		return err
	}
	if !row.ProofWanted(now) {
		return nil
	}
	verified, verifyErr := guardidentity.VerifyHostProof(proof, guardidentity.HostProofExpectation{
		Issuer: row.WorkerID, Subject: row.ServerID, TenantID: row.ConnectTenantID,
		Audience: guardidentity.AudienceConnect, Generation: row.Generation, Now: now,
	})
	if verifyErr == nil && verified.Thumbprint != row.KeyThumbprint {
		verifyErr = guardidentity.Denied(guardidentity.CodeProofInvalid, guardidentity.ReasonKeyMismatch)
	}
	if verifyErr != nil {
		s.log.Warn("guard_reach_host_proof_rejected", "worker_id", row.WorkerID, "error", verifyErr.Error())
		_, err := s.cfg.Store.Update(ctx, tenantID, workerID, func(r *Row) bool {
			if r.Generation != row.Generation || r.Connect.registeredAt(r.Generation) {
				return false
			}
			r.Connect.ReasonCode, r.Connect.UpdatedAt = ReasonProofRejected, &now
			return true
		})
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	node, putErr := s.cfg.Connect.PutNode(callCtx, NodeUpsert{
		OwnerSubject: row.OwnerSubjectID, ConnectTenantID: row.ConnectTenantID, Roles: row.OwnerRoles,
		HostProof: proof, KeyThumbprint: row.KeyThumbprint, DisplayName: connectDisplayName(row),
		StackID: gatewayStackID(row.StackID, row.ServerID), Placement: normalizePlacement(row.Placement),
	})
	revokeNow := false
	_, err = s.cfg.Store.Update(ctx, tenantID, workerID, func(r *Row) bool {
		if r.Generation != row.Generation {
			return false
		}
		if putErr != nil {
			reason, retryable := connectReason(putErr)
			retry := now.Add(connectPermanent)
			if retryable {
				retry = now.Add(connectRetryable)
			}
			r.Connect = Target{State: TargetFailed, Generation: r.Generation, ReasonCode: reason, UpdatedAt: &now, RetryAt: &retry}
			return true
		}
		if r.Desired == DesiredRevoked {
			// Detached while the registration was in flight: undo it below.
			revokeNow = true
			return false
		}
		r.Connect = Target{State: TargetRegistered, Generation: r.Generation, Ref: node.NodeID, UpdatedAt: &now}
		return true
	})
	if err != nil {
		return err
	}
	if revokeNow {
		_, revokeErr := s.cfg.Connect.RevokeNode(ctx, row.OwnerSubjectID, row.ConnectTenantID, row.OwnerRoles, row.ServerID, defaultRevokeReason)
		return revokeErr
	}
	return nil
}

func connectDisplayName(row *Row) string {
	name := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(row.DisplayName))
	if name == "" {
		name = "Server " + row.ServerID
	}
	if runes := []rune(name); len(runes) > 120 {
		name = strings.TrimSpace(string(runes[:120]))
	}
	return name
}

// ListByTenant serves the reach section of the server read model.
func (s *Service) ListByTenant(ctx context.Context, tenantID string) ([]Row, error) {
	return s.cfg.Store.ListByTenant(ctx, tenantID)
}
