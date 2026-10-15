package originca

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Authority is the Cloudflare side: issue from a CSR, revoke by id.
type Authority interface {
	Issue(ctx context.Context, hosts []string, csrPEM string) (Certificate, error)
	Revoke(ctx context.Context, certificateID string) error
}

// Renewal happens after two thirds of a certificate's lifetime.
const renewalNumerator, renewalDenominator = 2, 3

// Service joins the Cloudflare authority with Techstack custody. A nil
// authority (token missing) fails every issuance closed with authorityErr.
type Service struct {
	authority    Authority
	authorityErr error
	store        Store
	now          func() time.Time
}

// NewService returns a service over a store. authorityErr is the configuration
// error (typically *TokenMissingError) reported when authority is nil.
func NewService(authority Authority, authorityErr error, store Store) *Service {
	if authority == nil && authorityErr == nil {
		authorityErr = &TokenMissingError{}
	}
	return &Service{authority: authority, authorityErr: authorityErr, store: store, now: time.Now}
}

// Issue has Cloudflare sign the node's CSR. It fails closed without a token.
func (s *Service) Issue(ctx context.Context, hosts []string, csrPEM string) (Certificate, error) {
	if s == nil {
		return Certificate{}, ErrUnavailable
	}
	if s.authority == nil {
		return Certificate{}, s.authorityErr
	}
	return s.authority.Issue(ctx, hosts, csrPEM)
}

// Current returns the stack's record, if any.
func (s *Service) Current(ctx context.Context, tenantID, stackID string) (Record, bool, error) {
	record, err := s.store.Get(ctx, tenantID, stackID)
	if errors.Is(err, ErrNotFound) {
		return Record{}, false, nil
	}
	return record, err == nil, err
}

// Track persists a freshly issued certificate before it is installed, so its
// id can never leak. The previous current id moves to the retired list; it is
// revoked only after the node confirmed the new install (CompleteInstall).
func (s *Service) Track(ctx context.Context, tenantID, stackID string, certificate Certificate) error {
	previous, found, err := s.Current(ctx, tenantID, stackID)
	if err != nil {
		return err
	}
	retired := []string{}
	if found {
		retired = append(retired, previous.RetiredIDs...)
		if previous.CertificateID != certificate.ID {
			retired = append(retired, previous.CertificateID)
		}
	}
	return s.store.Put(ctx, Record{
		TenantID: tenantID, StackID: stackID, CertificateID: certificate.ID, Hosts: certificate.Hosts,
		NotAfter: certificate.ExpiresOn, RetiredIDs: retired,
	})
}

// CompleteInstall records the node's install confirmation and then revokes
// every retired certificate. A revocation failure is returned but leaves the
// id in the retired list, so the next rotation or the decommission retries it.
func (s *Service) CompleteInstall(ctx context.Context, tenantID, stackID, certificateID, certificateSHA256 string, installedAt time.Time) error {
	if err := s.store.MarkInstalled(ctx, tenantID, stackID, certificateID, certificateSHA256, installedAt); err != nil {
		return err
	}
	return s.RevokeRetired(ctx, tenantID, stackID)
}

// RevokeRetired revokes the superseded certificates of a stack.
func (s *Service) RevokeRetired(ctx context.Context, tenantID, stackID string) error {
	record, found, err := s.Current(ctx, tenantID, stackID)
	if err != nil || !found {
		return err
	}
	var failures []error
	for _, id := range record.RetiredIDs {
		if s.authority == nil {
			return s.authorityErr
		}
		if err := s.authority.Revoke(ctx, id); err != nil {
			failures = append(failures, err)
			continue
		}
		if err := s.store.ClearRetired(ctx, tenantID, stackID, id); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// NeedsRenewal reports whether the installed certificate passed two thirds of
// its lifetime, or does not cover hosts.
func (s *Service) NeedsRenewal(record Record, hosts []string) bool {
	if !SameHosts(record.Hosts, hosts) || record.InstalledAt.IsZero() {
		return true
	}
	lifetime := record.NotAfter.Sub(record.InstalledAt)
	return s.now().After(record.InstalledAt.Add(lifetime * renewalNumerator / renewalDenominator))
}

// Decommission revokes the stack's current and retired certificates at
// Cloudflare and removes the custody row. It is idempotent (an already revoked
// certificate is success) and surfaces every failure: a stack without a record
// needs no token and succeeds.
func (s *Service) Decommission(ctx context.Context, tenantID, stackID string) error {
	if s == nil {
		return nil
	}
	record, found, err := s.Current(ctx, tenantID, stackID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if s.authority == nil {
		return s.authorityErr
	}
	var failures []error
	for _, id := range append([]string{record.CertificateID}, record.RetiredIDs...) {
		if err := s.authority.Revoke(ctx, id); err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("revoke origin certificates: %w", errors.Join(failures...))
	}
	return s.store.Delete(ctx, tenantID, stackID)
}
