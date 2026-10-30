package controlplane

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Household identity references implement the link authority of Kombify
// Connect decision D10: Techstack alone ties a Cloud (Auth0) subject to a
// homelab identity, because only Techstack reaches the node through its Guard.
// The homelab identity provider stays the authority for the person; a reference
// holds no credential, passkey, session or activation link.
const (
	HouseholdIdPPocketID = "pocket-id"
	HouseholdIdPTinyAuth = "tinyauth"

	HouseholdRoleOwner  = "owner"
	HouseholdRoleMember = "member"

	// An invitation alone never links. Only a verified Pocket ID OIDC login of
	// the invited person moves a reference to linked.
	HouseholdLinkInvited = "invited"
	HouseholdLinkLinked  = "linked"
)

// HomelabIdentityRef is the HomelabIdentityRefV1 record with its cloud subject
// and link state.
type HomelabIdentityRef struct {
	ReferenceID    string
	TenantID       string
	StackID        string
	IdP            string
	Username       string
	Role           string
	OwnerSubjectID string
	CloudSubject   string
	Email          string
	DisplayName    string
	Status         string
	InvitedAt      time.Time
	LinkedAt       *time.Time
	UpdatedAt      time.Time
}

// HouseholdLinkStore persists household identity references per tenant.
type HouseholdLinkStore interface {
	// UpsertHouseholdInvite records one invited member. A repeated invite for
	// the same cloud subject refreshes the mutable fields and never downgrades a
	// linked reference. A different cloud subject on the same username is a
	// conflict: a Pocket ID username belongs to exactly one person.
	UpsertHouseholdInvite(ctx context.Context, ref HomelabIdentityRef) (*HomelabIdentityRef, error)
	// MarkHouseholdLinked compares the immutable incarnation and proof-bound
	// identity atomically. A removed/recreated reference is never linked by
	// an old callback; missing or changed expectations return ErrNotFound.
	MarkHouseholdLinked(ctx context.Context, expected HomelabIdentityRef, at time.Time) (*HomelabIdentityRef, error)
	// DeleteHouseholdRef removes the reference of one cloud subject and returns
	// it, or ErrNotFound.
	DeleteHouseholdRef(ctx context.Context, tenantID, stackID, cloudSubject string) (*HomelabIdentityRef, error)
	// ListHouseholdRefsByOwner returns every reference of the owner's stacks,
	// ordered by stack and username.
	ListHouseholdRefsByOwner(ctx context.Context, tenantID, ownerSubjectID string) ([]HomelabIdentityRef, error)
}

// ErrHouseholdUsernameTaken means the Pocket ID username already belongs to a
// different cloud subject on this stack.
var ErrHouseholdUsernameTaken = errors.New("controlplane: household username belongs to another person")

func normalizeHouseholdRef(ref HomelabIdentityRef) (HomelabIdentityRef, error) {
	ref.TenantID = strings.TrimSpace(ref.TenantID)
	ref.StackID = strings.TrimSpace(ref.StackID)
	ref.Username = strings.TrimSpace(ref.Username)
	ref.OwnerSubjectID = strings.TrimSpace(ref.OwnerSubjectID)
	ref.CloudSubject = strings.TrimSpace(ref.CloudSubject)
	ref.Email = strings.TrimSpace(ref.Email)
	ref.DisplayName = strings.TrimSpace(ref.DisplayName)
	if ref.IdP == "" {
		ref.IdP = HouseholdIdPPocketID
	}
	if ref.Role == "" {
		ref.Role = HouseholdRoleMember
	}
	if ref.TenantID == "" || ref.StackID == "" || ref.Username == "" || ref.OwnerSubjectID == "" ||
		ref.CloudSubject == "" || ref.Email == "" {
		return HomelabIdentityRef{}, fmt.Errorf("controlplane: household reference requires tenant, stack, username, owner, cloud subject and email")
	}
	if ref.IdP != HouseholdIdPPocketID && ref.IdP != HouseholdIdPTinyAuth {
		return HomelabIdentityRef{}, fmt.Errorf("controlplane: unsupported household identity provider %q", ref.IdP)
	}
	if ref.Role != HouseholdRoleMember {
		return HomelabIdentityRef{}, fmt.Errorf("controlplane: only member references are projected")
	}
	ref.Status = HouseholdLinkInvited
	ref.LinkedAt = nil
	return ref, nil
}

// MemoryHouseholdLinkStore is the in-memory HouseholdLinkStore of tests and the
// embedded development loop without a database.
type MemoryHouseholdLinkStore struct {
	mu   sync.Mutex
	now  func() time.Time
	refs map[string]HomelabIdentityRef
}

func NewMemoryHouseholdLinkStore() *MemoryHouseholdLinkStore {
	return &MemoryHouseholdLinkStore{now: func() time.Time { return time.Now().UTC() }, refs: map[string]HomelabIdentityRef{}}
}

func householdKey(tenantID, stackID, cloudSubject string) string {
	return tenantID + "\x00" + stackID + "\x00" + cloudSubject
}

func (s *MemoryHouseholdLinkStore) UpsertHouseholdInvite(_ context.Context, ref HomelabIdentityRef) (*HomelabIdentityRef, error) {
	ref, err := normalizeHouseholdRef(ref)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.refs {
		if existing.TenantID == ref.TenantID && existing.StackID == ref.StackID &&
			existing.Username == ref.Username && existing.CloudSubject != ref.CloudSubject {
			return nil, ErrHouseholdUsernameTaken
		}
	}
	key := householdKey(ref.TenantID, ref.StackID, ref.CloudSubject)
	now := s.now()
	if existing, ok := s.refs[key]; ok {
		existing.Username, existing.Email, existing.DisplayName = ref.Username, ref.Email, ref.DisplayName
		existing.OwnerSubjectID, existing.UpdatedAt = ref.OwnerSubjectID, now
		s.refs[key] = existing
		out := existing
		return &out, nil
	}
	ref.ReferenceID = uuid.NewString()
	ref.InvitedAt, ref.UpdatedAt = now, now
	s.refs[key] = ref
	out := ref
	return &out, nil
}

func (s *MemoryHouseholdLinkStore) MarkHouseholdLinked(_ context.Context, expected HomelabIdentityRef, at time.Time) (*HomelabIdentityRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := householdKey(expected.TenantID, expected.StackID, expected.CloudSubject)
	ref, ok := s.refs[key]
	if !ok || expected.ReferenceID == "" || ref.ReferenceID != expected.ReferenceID ||
		ref.OwnerSubjectID != expected.OwnerSubjectID || ref.Username != expected.Username || ref.IdP != expected.IdP || ref.Email != expected.Email {
		return nil, ErrNotFound
	}
	if ref.Status != HouseholdLinkLinked {
		at = at.UTC()
		ref.Status, ref.LinkedAt, ref.UpdatedAt = HouseholdLinkLinked, &at, s.now()
		s.refs[key] = ref
	}
	out := ref
	return &out, nil
}

func (s *MemoryHouseholdLinkStore) DeleteHouseholdRef(_ context.Context, tenantID, stackID, cloudSubject string) (*HomelabIdentityRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := householdKey(strings.TrimSpace(tenantID), strings.TrimSpace(stackID), strings.TrimSpace(cloudSubject))
	ref, ok := s.refs[key]
	if !ok {
		return nil, ErrNotFound
	}
	delete(s.refs, key)
	return &ref, nil
}

func (s *MemoryHouseholdLinkStore) ListHouseholdRefsByOwner(_ context.Context, tenantID, ownerSubjectID string) ([]HomelabIdentityRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []HomelabIdentityRef
	for _, ref := range s.refs {
		if ref.TenantID == strings.TrimSpace(tenantID) && ref.OwnerSubjectID == strings.TrimSpace(ownerSubjectID) {
			out = append(out, ref)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StackID != out[j].StackID {
			return out[i].StackID < out[j].StackID
		}
		return out[i].Username < out[j].Username
	})
	return out, nil
}
