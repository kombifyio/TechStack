package authlocal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/authsession"
)

// Bootstrap ensures the break-glass admin record exists.
//
// On first run it creates a record with the configured BootstrapEmail and a
// freshly generated 18-byte password (~24 char base64), persists the bcrypt
// hash, and stores the plaintext in a 1h show-once envelope. On subsequent
// runs it is a no-op.
//
// Returns the generated plaintext password if a new record was created,
// otherwise the empty string.
func (s *Service) Bootstrap(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, err := s.cfg.Store.Get(ctx)
	if err != nil {
		return "", fmt.Errorf("get record: %w", err)
	}
	if rec != nil && rec.PasswordHash != "" {
		if rec.Claimed || rec.Locked || s.cfg.ClaimLocked {
			return "", nil
		}
		// Already bootstrapped; if the envelope expired, scrub it.
		if !rec.ShowUntil.IsZero() && s.cfg.Now().After(rec.ShowUntil) {
			next := *rec
			next.ShowPassword = ""
			next.ShowUntil = time.Time{}
			next.UpdatedAt = s.cfg.Now()
			if saveErr := s.compareAndSwap(ctx, rec, &next); saveErr != nil {
				return "", fmt.Errorf("scrub envelope: %w", saveErr)
			}
		}
		return "", nil
	}
	if s.cfg.ClaimLocked || rec != nil && rec.Locked {
		return "", ErrLockedByOperator
	}

	password, err := generateSecureToken(18)
	if err != nil {
		return "", err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return "", err
	}
	now := s.cfg.Now()
	fresh := &Record{
		Email:        s.cfg.BootstrapEmail,
		PasswordHash: hash,
		ShowPassword: password,
		ShowUntil:    now.Add(PasswordEnvelopeTTL),
		Claimed:      false,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.compareAndSwap(ctx, rec, fresh); err != nil {
		return "", fmt.Errorf("save record: %w", err)
	}
	return password, nil
}

// RevealResult is the show-once envelope payload.
type RevealResult struct {
	Email     string    `json:"email"`
	Password  string    `json:"password"`
	ExpiresAt time.Time `json:"expires_at"`
	Claimed   bool      `json:"claimed"`
}

// Reveal returns the bootstrap password if the envelope is still alive.
// Calling Reveal on an expired or already-consumed envelope returns
// [ErrPasswordExpired].
func (s *Service) Reveal(ctx context.Context) (*RevealResult, error) {
	if s.cfg.ClaimLocked {
		return nil, ErrLockedByOperator
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, err := s.cfg.Store.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("get record: %w", err)
	}
	if rec == nil || rec.PasswordHash == "" {
		return nil, ErrNotFound
	}
	if rec.Locked {
		return nil, ErrLockedByOperator
	}
	if rec.Claimed {
		return nil, ErrPasswordExpired
	}
	if rec.ShowPassword == "" {
		return nil, ErrPasswordExpired
	}
	if !rec.ShowUntil.IsZero() && s.cfg.Now().After(rec.ShowUntil) {
		// Auto-scrub expired envelope.
		next := *rec
		next.ShowPassword = ""
		next.ShowUntil = time.Time{}
		next.UpdatedAt = s.cfg.Now()
		if err := s.compareAndSwap(ctx, rec, &next); err != nil {
			return nil, fmt.Errorf("scrub envelope: %w", err)
		}
		return nil, ErrPasswordExpired
	}
	result := &RevealResult{
		Email:     rec.Email,
		Password:  rec.ShowPassword,
		ExpiresAt: rec.ShowUntil,
		Claimed:   rec.Claimed,
	}
	next := *rec
	next.ShowPassword = ""
	next.ShowUntil = time.Time{}
	next.UpdatedAt = s.cfg.Now()
	if err := s.compareAndSwap(ctx, rec, &next); err != nil {
		return nil, fmt.Errorf("consume envelope: %w", err)
	}
	return result, nil
}

// ReissueBootstrap replaces the credential of an existing, unclaimed bootstrap
// owner. The previous password stops authenticating immediately. It neither
// restores consumed plaintext nor creates a new owner. The returned password is
// already consumed locally: it is never placed in an HTTP reveal envelope.
//
// This is an operator-only primitive. Consumers must call it exclusively from
// their host-local administration command, after proving access to the existing
// self-hosted installation; never expose it through an anonymous HTTP handler.
func (s *Service) ReissueBootstrap(ctx context.Context) (string, error) {
	if s.cfg.ClaimLocked {
		return "", ErrLockedByOperator
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.cfg.Store.Get(ctx)
	if err != nil {
		return "", fmt.Errorf("get record: %w", err)
	}
	if rec == nil || rec.PasswordHash == "" {
		return "", ErrNotFound
	}
	if rec.Locked {
		return "", ErrLockedByOperator
	}
	if rec.Claimed {
		return "", ErrAlreadyClaimed
	}
	password, err := generateSecureToken(18)
	if err != nil {
		return "", err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return "", err
	}
	next := *rec
	next.PasswordHash = hash
	next.ShowPassword = ""
	next.ShowUntil = time.Time{}
	next.UpdatedAt = s.cfg.Now()
	if err := s.compareAndSwap(ctx, rec, &next); err != nil {
		return "", fmt.Errorf("reissue bootstrap: %w", err)
	}
	return password, nil
}

func (s *Service) compareAndSwap(ctx context.Context, expected, next *Record) error {
	store, ok := s.cfg.Store.(AtomicStore)
	if !ok {
		return ErrAtomicStoreRequired
	}
	saved, err := store.CompareAndSwap(ctx, expected, next)
	if err != nil {
		return err
	}
	if !saved {
		return ErrConcurrentUpdate
	}
	return nil
}

// ClaimRequest is the input to [Service.Claim].
type ClaimRequest struct {
	// CurrentPassword is the bootstrap password the operator received.
	CurrentPassword string
	// NewEmail optionally renames the admin to a personal address.
	NewEmail string
	// NewPassword is the operator's chosen permanent secret.
	NewPassword string
}

// Claim rotates the break-glass record from bootstrap state to its final
// operator-owned values. First-POST-wins: subsequent calls return
// [ErrAlreadyClaimed].
//
// If [Config.ClaimLocked] is set, claiming is refused even when the record
// is still in bootstrap state — used to harden production where the
// operator has already claimed and wants to make absolutely sure no second
// claim slips in.
func (s *Service) Claim(ctx context.Context, req ClaimRequest) error {
	if s.cfg.ClaimLocked {
		return ErrLockedByOperator
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	rec, err := s.cfg.Store.Get(ctx)
	if err != nil {
		return fmt.Errorf("get record: %w", err)
	}
	if rec == nil || rec.PasswordHash == "" {
		return ErrNotFound
	}
	if rec.Locked {
		return ErrLockedByOperator
	}
	if rec.Claimed {
		return ErrAlreadyClaimed
	}
	if passwordErr := verifyPassword(rec.PasswordHash, req.CurrentPassword); passwordErr != nil {
		return passwordErr
	}
	newPwd := strings.TrimSpace(req.NewPassword)
	if newPwd == "" {
		return fmt.Errorf("%w: new password required", ErrInvalidCreds)
	}
	hash, err := hashPassword(newPwd)
	if err != nil {
		return err
	}
	email := strings.TrimSpace(req.NewEmail)
	if email == "" {
		email = rec.Email
	}
	now := s.cfg.Now()
	next := *rec
	next.Email = email
	next.PasswordHash = hash
	next.ShowPassword = ""
	next.ShowUntil = time.Time{}
	next.Claimed = true
	next.UpdatedAt = now
	if err := s.compareAndSwap(ctx, rec, &next); err != nil {
		return fmt.Errorf("save record: %w", err)
	}
	return nil
}

// Authenticate verifies a login attempt and returns [authsession.Claims] if
// valid.
func (s *Service) Authenticate(ctx context.Context, email, password string) (authsession.Claims, error) {
	rec, err := s.cfg.Store.Get(ctx)
	if err != nil {
		return authsession.Claims{}, fmt.Errorf("get record: %w", err)
	}
	if rec == nil || rec.PasswordHash == "" {
		return authsession.Claims{}, ErrInvalidCreds
	}
	if !strings.EqualFold(strings.TrimSpace(email), strings.TrimSpace(rec.Email)) {
		return authsession.Claims{}, ErrInvalidCreds
	}
	if err := verifyPassword(rec.PasswordHash, password); err != nil {
		if errors.Is(err, ErrInvalidCreds) {
			return authsession.Claims{}, ErrInvalidCreds
		}
		return authsession.Claims{}, err
	}
	return authsession.Claims{
		Subject:  "breakglass:" + BreakGlassRecordID,
		TenantID: s.cfg.DefaultTenantID,
		Email:    rec.Email,
		Provider: DefaultProviderID,
		Role:     "admin",
	}, nil
}

// Status reports the current bootstrap/claim state used by the methods
// discovery endpoint and the setup wizard.
type Status struct {
	Initialized      bool      `json:"initialized"`
	Claimed          bool      `json:"claimed"`
	Email            string    `json:"email,omitempty"`
	HasPendingReveal bool      `json:"has_pending_reveal"`
	RevealExpiresAt  time.Time `json:"reveal_expires_at,omitempty"`
	Locked           bool      `json:"locked"`
}

// CurrentStatus inspects the record without exposing secrets.
func (s *Service) CurrentStatus(ctx context.Context) (Status, error) {
	rec, err := s.cfg.Store.Get(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("get record: %w", err)
	}
	if rec == nil || rec.PasswordHash == "" {
		return Status{Initialized: false, Locked: s.cfg.ClaimLocked}, nil
	}
	pending := !rec.Claimed && !rec.Locked && !s.cfg.ClaimLocked && rec.ShowPassword != "" && (rec.ShowUntil.IsZero() || s.cfg.Now().Before(rec.ShowUntil))
	return Status{
		Initialized:      true,
		Claimed:          rec.Claimed,
		Email:            rec.Email,
		HasPendingReveal: pending,
		RevealExpiresAt:  rec.ShowUntil,
		Locked:           s.cfg.ClaimLocked || rec.Locked,
	}, nil
}
