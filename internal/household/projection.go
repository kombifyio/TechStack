package household

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

func (s *Service) projectRevision(ctx context.Context, tenantID, owner string, req ProjectionRequest) (ProjectionResult, error) {
	store, ok := s.Links.(controlplane.HouseholdProjectionStore)
	if !ok {
		return ProjectionResult{}, controlplane.ErrHouseholdProjectionUnavailable
	}
	if req.Action != ActionAdd && req.Action != ActionRemove {
		return ProjectionResult{}, controlplane.ErrHouseholdProjectionConflict
	}
	member := req.Member
	member.Subject = strings.TrimSpace(member.Subject)
	member.Email = strings.TrimSpace(member.Email)
	member.DisplayName = strings.TrimSpace(member.DisplayName)
	if req.Action == ActionAdd && (member.Email == "" || !strings.Contains(member.Email, "@") || len(member.Email) > 254 ||
		strings.ContainsAny(member.Email, "\r\n") || len(member.DisplayName) > 128 || strings.ContainsAny(member.DisplayName, "\r\n")) {
		return ProjectionResult{}, ErrMemberInvalid
	}
	// Access is a set; profile edits are not new authorization effects. The
	// selected username and runtime are independently immutable in the binding.
	apps := AppGrant{}
	if req.Action == ActionAdd && req.Apps.Set {
		apps.Set = true
		apps.Apps = slices.Clone(req.Apps.Apps)
		slices.Sort(apps.Apps)
		apps.Apps = slices.Compact(apps.Apps)
	}
	payload, err := json.Marshal(struct {
		Action string   `json:"action"`
		Set    bool     `json:"set"`
		Apps   []string `json:"apps,omitempty"`
	}{req.Action, apps.Set, apps.Apps})
	if err != nil {
		return ProjectionResult{}, err
	}
	digest := sha256.Sum256(payload)
	// Commit the high-water mark before inventory/issuer lookup. In particular,
	// a remove without a reference still prevents an older delayed add.
	binding, err := store.AcceptHouseholdProjection(ctx, controlplane.HouseholdProjectionIntent{
		TenantID: tenantID, OwnerSubjectID: owner, CloudSubject: member.Subject,
		Revision: req.Revision, Action: req.Action, EffectDigest: hex.EncodeToString(digest[:]),
	})
	if err != nil {
		return ProjectionResult{}, err
	}
	if binding.StackID == "" && req.Action == ActionRemove {
		return ProjectionResult{Status: StatusNone}, nil
	}
	if binding.AgentID == "" {
		stackID := binding.StackID
		if stackID == "" {
			homelab, err := s.Homelabs.GetHomelabByOwner(ctx, tenantID, owner)
			if errors.Is(err, controlplane.ErrNotFound) || (err == nil && homelab == nil) {
				return ProjectionResult{Status: StatusNone}, nil
			}
			if err != nil {
				return ProjectionResult{}, err
			}
			stacks, err := s.identityStacks(ctx, tenantID, owner, homelab.ID)
			if err != nil {
				return ProjectionResult{}, err
			}
			if len(stacks) == 0 {
				return ProjectionResult{Status: StatusNone}, nil
			}
			stackID = stacks[0].ID
		}
		resolver, ok := s.Identity.(ProjectionTargetResolver)
		if !ok {
			return ProjectionResult{}, ErrNodeUnreachable
		}
		target, err := resolver.ResolveProjectionTarget(ctx, Target{TenantID: tenantID, OwnerID: owner, StackID: stackID})
		if err != nil {
			return ProjectionResult{}, fmt.Errorf("%w: %v", ErrNodeUnreachable, err)
		}
		if target.TenantID != tenantID || target.OwnerID != owner || target.StackID != stackID {
			return ProjectionResult{}, controlplane.ErrHouseholdProjectionConflict
		}
		binding, err = store.BindHouseholdProjectionTarget(ctx, *binding, stackID, target.AgentID, target.StackKitInstanceID)
		if err != nil {
			return ProjectionResult{}, err
		}
	}
	runtime, ok := s.Identity.(RevisionedIdentityRuntime)
	if !ok {
		return ProjectionResult{}, ErrNodeUnreachable
	}
	key := sha256.Sum256([]byte(owner + "\n" + member.Subject))
	projection := IdentityProjection{Key: hex.EncodeToString(key[:]), Revision: req.Revision, Action: req.Action, Username: binding.Username}
	if req.Action == ActionAdd {
		projection.Email, projection.DisplayName, projection.Apps = member.Email, member.DisplayName, apps
	}
	// Never fall back to a different stack or enrolled runtime after this
	// binding, even when the first dispatch's effect/response is unknown.
	invitation, err := runtime.Project(ctx, Target{TenantID: tenantID, OwnerID: owner, StackID: binding.StackID,
		AgentID: binding.AgentID, StackKitInstanceID: binding.StackKitInstanceID}, projection)
	if err != nil {
		return ProjectionResult{}, fmt.Errorf("%w: %v", ErrNodeUnreachable, err)
	}
	if req.Action == ActionAdd && invitation.Username != binding.Username {
		return ProjectionResult{}, fmt.Errorf("%w: projection identity mismatch", ErrNodeUnreachable)
	}
	// The generation check and D10 reference mutation are one transaction. A
	// late add response cannot resurrect a reference after a newer removal.
	ref, err := store.CompleteHouseholdProjection(ctx, *binding, member.Email, member.DisplayName)
	if err != nil {
		return ProjectionResult{}, err
	}
	if req.Action == ActionRemove {
		return ProjectionResult{Status: StatusProjected, RemovedStacks: []string{binding.StackID}}, nil
	}
	return ProjectionResult{Status: StatusProjected, StackID: binding.StackID, Username: ref.Username, LinkStatus: ref.Status,
		ActivationURL: invitation.ActivationURL, ExpiresAt: invitation.ExpiresAt}, nil
}
