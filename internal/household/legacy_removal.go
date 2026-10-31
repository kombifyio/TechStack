package household

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

// A revisioned removal of a person without a projection binding is
// acknowledged as a no-op (projectRevision): there is no target and the node
// projection refuses to turn a legacy name into deletion authority. Accounts
// that the old unrevisioned add created (2026-10-20, platform-y0502) therefore
// stayed on the node. RemoveLegacyAccount is the fenced follow-up for exactly
// that acknowledged removal (platform-nt9a1), the revisioned counterpart of
// removeUnreferenced.

// ErrLegacyRemovalRefused means the legacy cleanup found a fact it cannot
// decide safely; nothing on that stack is removed.
var ErrLegacyRemovalRefused = errors.New("household: legacy account removal refused")

// LegacyAttempt is the retained command attempt of one legacy cleanup on one
// stack. Its key is distinct from every projection key.
type LegacyAttempt struct {
	Key      string
	Revision uint64
}

// LegacyRemovalRuntime lists a node's household accounts and removes one of
// them by username. Each command is retained under its LegacyAttempt, so an
// unknown earlier removal blocks a new command for the same person and stack.
type LegacyRemovalRuntime interface {
	ListLegacyHouseholdUsers(ctx context.Context, target Target, attempt LegacyAttempt) ([]HouseholdUser, error)
	RemoveLegacyHouseholdUser(ctx context.Context, target Target, attempt LegacyAttempt, username string) error
}

// ProjectionBindings reads an owner's retained projection bindings.
type ProjectionBindings interface {
	ListHouseholdProjectionBindings(ctx context.Context, tenantID, ownerSubject string) ([]controlplane.HouseholdProjectionBinding, error)
}

// LegacyRemoval is one acknowledged revisioned removal, as Cloud holds it.
type LegacyRemoval struct {
	OwnerSubject string
	Subject      string
	Email        string
	Revision     uint64
	// StillMember is true when Cloud lists the person as a live member of any
	// of the owner's households.
	StillMember bool
	// MemberEmails holds the lowercased addresses of the owner's live
	// household members, the owner included.
	MemberEmails map[string]bool
	// Recheck runs right before each node removal and must fail when Cloud
	// holds a newer lifecycle event for the pair or lists the person again.
	Recheck func(context.Context) error
}

// LegacyOutcome is the audit record of one stack.
type LegacyOutcome struct {
	StackID  string
	Username string
	// Result is removed or absent.
	Result string
}

// RemoveLegacyAccount removes the one household account with the person's
// e-mail address from each of the owner's identity stacks, only when the
// person has no projection binding or reference, is no live member in Cloud,
// and the account is in the node's household group (the node lists and removes
// only those; StackKits refuses the owner and administrators). Several matches,
// a username another person holds and an address a live member uses are
// refused. An absent account counts as done.
func (s *Service) RemoveLegacyAccount(ctx context.Context, req LegacyRemoval) ([]LegacyOutcome, error) {
	owner, subject := strings.TrimSpace(req.OwnerSubject), strings.TrimSpace(req.Subject)
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if owner == "" || subject == "" || owner == subject || req.Revision == 0 || !strings.Contains(email, "@") {
		return nil, fmt.Errorf("%w: incomplete removal", ErrLegacyRemovalRefused)
	}
	if req.StillMember || req.MemberEmails[email] {
		return nil, fmt.Errorf("%w: the person or the address is a live household member", ErrLegacyRemovalRefused)
	}
	if req.Recheck == nil {
		return nil, fmt.Errorf("%w: no recheck", ErrLegacyRemovalRefused)
	}
	runtime, ok := s.Identity.(LegacyRemovalRuntime)
	if !ok {
		return nil, ErrNodeUnreachable
	}
	bindingStore, ok := s.Links.(ProjectionBindings)
	if !ok {
		return nil, controlplane.ErrHouseholdProjectionUnavailable
	}
	resolver, ok := s.Identity.(ProjectionTargetResolver)
	if !ok {
		return nil, ErrNodeUnreachable
	}
	var tenantID string
	var err error
	if system, ok := s.Tenants.(interface {
		ResolveProjectionOwnerTenant(context.Context, string, string) (string, error)
	}); ok {
		tenantID, err = system.ResolveProjectionOwnerTenant(ctx, owner, subject)
	} else {
		tenantID, err = s.Tenants.ResolveOwnerTenant(ctx, "", owner)
	}
	if err != nil {
		return nil, err
	}
	bindings, err := bindingStore.ListHouseholdProjectionBindings(ctx, tenantID, owner)
	if err != nil {
		return nil, err
	}
	refs, err := s.Links.ListHouseholdRefsByOwner(ctx, tenantID, owner)
	if err != nil {
		return nil, err
	}
	// Every username a binding or reference names, this person's own binding
	// included, is never removed: a projected account always carries its
	// binding's username, so a concurrent revisioned add is never the match.
	held := map[string]bool{}
	for _, binding := range bindings {
		held[strings.ToLower(binding.Username)] = true
	}
	for _, ref := range refs {
		if ref.CloudSubject == subject {
			return nil, fmt.Errorf("%w: the person has a saved reference", ErrLegacyRemovalRefused)
		}
		held[strings.ToLower(ref.Username)] = true
	}
	if err := unbound(bindings, subject, req.Revision); err != nil {
		return nil, err
	}
	homelab, err := s.Homelabs.GetHomelabByOwner(ctx, tenantID, owner)
	if errors.Is(err, controlplane.ErrNotFound) || (err == nil && homelab == nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	stacks, err := s.identityStacks(ctx, tenantID, owner, homelab.ID)
	if err != nil {
		return nil, err
	}
	outcomes := []LegacyOutcome{}
	var pending error
	for _, stack := range stacks {
		target, err := resolver.ResolveProjectionTarget(ctx, Target{TenantID: tenantID, OwnerID: owner, StackID: stack.ID})
		if err != nil {
			pending = errors.Join(pending, fmt.Errorf("%w: %v", ErrNodeUnreachable, err))
			continue
		}
		if target.TenantID != tenantID || target.OwnerID != owner || target.StackID != stack.ID || target.AgentID == "" || target.StackKitInstanceID == "" {
			pending = errors.Join(pending, controlplane.ErrHouseholdProjectionConflict)
			continue
		}
		// Separate attempts for the read and the removal, bound to the runtime:
		// a re-enrolled node or an unsettled read never blocks the removal.
		attempt := func(step string) LegacyAttempt {
			key := sha256.Sum256([]byte("household-legacy-" + step + "\n" + owner + "\n" + subject + "\n" + stack.ID + "\n" + target.AgentID + "\n" + target.StackKitInstanceID))
			return LegacyAttempt{Key: hex.EncodeToString(key[:]), Revision: req.Revision}
		}
		users, err := runtime.ListLegacyHouseholdUsers(ctx, target, attempt("list"))
		if err != nil {
			pending = errors.Join(pending, fmt.Errorf("%w: %v", ErrNodeUnreachable, err))
			continue
		}
		matches := []string{}
		for _, user := range users {
			if strings.ToLower(strings.TrimSpace(user.Email)) == email && strings.TrimSpace(user.Username) != "" {
				matches = append(matches, strings.TrimSpace(user.Username))
			}
		}
		switch {
		case len(matches) == 0:
			outcomes = append(outcomes, LegacyOutcome{StackID: stack.ID, Result: "absent"})
			continue
		case len(matches) > 1:
			pending = errors.Join(pending, fmt.Errorf("%w: stack %s holds %d household accounts with the address", ErrLegacyRemovalRefused, stack.ID, len(matches)))
			continue
		case held[strings.ToLower(matches[0])]:
			pending = errors.Join(pending, fmt.Errorf("%w: stack %s: a binding or reference names the username", ErrLegacyRemovalRefused, stack.ID))
			continue
		}
		// The list took a node round trip: confirm again that Cloud holds no
		// newer event and that the person still has no projection target.
		if err := req.Recheck(ctx); err != nil {
			return outcomes, errors.Join(pending, fmt.Errorf("%w: %v", ErrLegacyRemovalRefused, err))
		}
		current, err := bindingStore.ListHouseholdProjectionBindings(ctx, tenantID, owner)
		if err != nil {
			return outcomes, errors.Join(pending, err)
		}
		if err := unbound(current, subject, req.Revision); err != nil {
			return outcomes, errors.Join(pending, err)
		}
		if err := runtime.RemoveLegacyHouseholdUser(ctx, target, attempt("remove"), matches[0]); err != nil {
			pending = errors.Join(pending, fmt.Errorf("%w: %v", ErrNodeUnreachable, err))
			continue
		}
		outcomes = append(outcomes, LegacyOutcome{StackID: stack.ID, Username: matches[0], Result: "removed"})
	}
	return outcomes, pending
}

// unbound refuses unless the person's retained binding, if any, has no target
// and still records this removal's revision.
func unbound(bindings []controlplane.HouseholdProjectionBinding, subject string, revision uint64) error {
	for _, binding := range bindings {
		if binding.CloudSubject != subject {
			continue
		}
		if binding.StackID != "" {
			return fmt.Errorf("%w: the person has a projection target", ErrLegacyRemovalRefused)
		}
		if binding.Revision != revision {
			return controlplane.ErrHouseholdProjectionStale
		}
	}
	return nil
}
