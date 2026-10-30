// Package household is the Techstack side of the Companion Feed household:
// the owner-approved projection of a Cloud household member to the owner's
// homelab, and the Kombify Connect D10 link between a Cloud (Auth0) subject and
// a Pocket ID identity on one stack.
//
// Cloud holds household membership (a Space). Techstack alone ties a Cloud
// subject to a homelab identity, because only Techstack reaches the node through
// its Guard. The homelab identity provider stays the authority for the person:
// an invitation alone never links, and nothing here stores a credential.
package household

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/oidcclient"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// Projection actions.
const (
	ActionAdd    = "add"
	ActionRemove = "remove"
	// ActionUpdateApps replaces the node apps a member may sign into.
	ActionUpdateApps = "update_apps"
)

// NodeAppCatalogPrefix prefixes a node app id in the Cloud catalog.
const NodeAppCatalogPrefix = "homelab.app."

// ErrAppInvalid means a node app id is not a grantable Cloud catalog id.
var ErrAppInvalid = errors.New("household: node app is not a grantable homelab app")

// AppGrant is the node apps a member may sign into. Set false means all apps.
// Set true with no apps means none.
type AppGrant struct {
	Set  bool
	Apps []string
}

// ParseNodeApps maps Cloud catalog ids (homelab.app.<id>) to de-duplicated node
// app ids, keeping first-seen order. Unknown ids are refused.
func ParseNodeApps(catalogIDs []string) ([]string, error) {
	apps := make([]string, 0, len(catalogIDs))
	for _, raw := range catalogIDs {
		id, ok := strings.CutPrefix(strings.TrimSpace(raw), NodeAppCatalogPrefix)
		if !ok || !slices.Contains(stackkitcommand.HouseholdGrantableApps, id) {
			return nil, fmt.Errorf("%w: %q", ErrAppInvalid, raw)
		}
		if !slices.Contains(apps, id) {
			apps = append(apps, id)
		}
	}
	return apps, nil
}

// Projection statuses, the values Cloud reads (Cloud household service):
// projected means the invite or removal was issued to the node, none means the
// owner has no homelab, pending means the node could not take it now. Techstack
// keeps no queue: Cloud holds the pending projection and re-sends it, which is
// idempotent.
const (
	StatusProjected  = "projected"
	StatusNone       = "none"
	StatusPending    = "pending"
	scopeIdentityMax = 64
)

// DefaultLinkClientID is the OIDC client id kombify registers on a node's
// Pocket ID for the link proof (public client, authorization code with PKCE).
// It follows the StackKits governed client id shape stackkit-<ref>.
const DefaultLinkClientID = "stackkit-kombify-link"

// LinkScopes are the OIDC scopes of the link proof.
const LinkScopes = "openid profile email"

var (
	// ErrNodeUnreachable means the identity command could not be delivered to or
	// completed on the node now; the caller may retry.
	ErrNodeUnreachable = errors.New("household: homelab identity runtime unavailable")
	// ErrOwnerNotAuthorized means the signed owner holds no active membership.
	ErrOwnerNotAuthorized = errors.New("household: owner is not authorized in a tenant")
	// ErrMemberInvalid means the projected member cannot be a Pocket ID user.
	ErrMemberInvalid = errors.New("household: member needs a subject, an e-mail address and a name")
	// ErrOwnerMember means the owner was named as a household member.
	ErrOwnerMember = errors.New("household: the owner is not a projected member")
	// ErrAmbiguousAccount means a node holds several household accounts for the
	// removed person's e-mail address, so none is removed without a reference.
	ErrAmbiguousAccount = errors.New("household: ambiguous household account")
	// ErrNoReference means no invited reference exists for the member.
	ErrNoReference = errors.New("household: no invited reference for this member")
	// ErrAmbiguousStack means the member is invited on several stacks and the
	// request names none.
	ErrAmbiguousStack = errors.New("household: stack_id is required")
	// ErrProofInvalid means the Pocket ID login proof did not verify or does not
	// belong to the invited person.
	ErrProofInvalid = errors.New("household: Pocket ID login proof rejected")
	// ErrIssuerUnavailable means the stack has no reachable Pocket ID issuer.
	ErrIssuerUnavailable = errors.New("household: the stack's Pocket ID issuer is unavailable")
)

// Target is one node's identity authority.
type Target struct {
	TenantID string
	OwnerID  string
	StackID  string
	// Managed projections bind these before their first dispatch. A current
	// inventory change must not silently select a different runtime.
	AgentID            string
	StackKitInstanceID string
}

// Invitation is the node's answer to an invite. ActivationURL is the one-time
// passkey enrolment link; it is returned to the caller once and never stored.
type Invitation struct {
	Username      string
	Status        string
	ActivationURL string
	ExpiresAt     *time.Time
}

// IdentityRuntime runs the owner-approved StackKit identity operations on a
// node. The owner approved them by sending the invitation or the removal.
type IdentityRuntime interface {
	Invite(ctx context.Context, target Target, username, email, displayName string, apps AppGrant) (Invitation, error)
	Remove(ctx context.Context, target Target, username string) error
	UpdateApps(ctx context.Context, target Target, username string, apps AppGrant) error
}

// HouseholdUser is one household identity a node holds.
type HouseholdUser struct {
	Username string
	Email    string
}

// IdentityLister lists the household identities a node holds. A removal uses
// it to find an account that no saved reference names.
type IdentityLister interface {
	ListHouseholdUsers(ctx context.Context, target Target) ([]HouseholdUser, error)
}

// RevisionedIdentityRuntime fences the actual node effect, including removals
// without a saved invite reference. No legacy command may substitute for it.
type RevisionedIdentityRuntime interface {
	Project(context.Context, Target, IdentityProjection) (Invitation, error)
}

// ProjectionTargetResolver resolves the exact approved runtime before the
// durable projection target is committed. It performs no node mutation.
type ProjectionTargetResolver interface {
	ResolveProjectionTarget(context.Context, Target) (Target, error)
}

type IdentityProjection struct {
	Key, Action, Username, Email, DisplayName string
	Revision                                  uint64
	Apps                                      AppGrant
}

// Homelabs reads the owner's homelab.
type Homelabs interface {
	GetHomelabByOwner(ctx context.Context, tenantID, ownerSubjectID string) (*controlplane.Homelab, error)
}

// Stacks lists the stacks of a tenant.
type Stacks interface {
	ListStacksByTenant(ctx context.Context, tenantID string) ([]controlplane.Stack, error)
}

// OwnerTenants resolves and authorizes the tenant of an owner subject. The hint
// is the tenant the signed Cloud identity named, empty when it named none.
type OwnerTenants interface {
	ResolveOwnerTenant(ctx context.Context, hintTenant, ownerSubject string) (string, error)
}

// Issuers resolves the Pocket ID issuer of a stack from Techstack's own service
// inventory. A token's own issuer claim is never trusted for this.
type Issuers interface {
	PocketIDIssuer(ctx context.Context, tenantID, stackID string) (string, error)
}

// IDTokenVerifier verifies an ID token against one issuer's published keys.
type IDTokenVerifier interface {
	Verify(ctx context.Context, issuer, clientID, rawToken string) (*oidcclient.Claims, error)
}

// Service implements the projection and the D10 link.
type Service struct {
	Tenants      OwnerTenants
	Homelabs     Homelabs
	Stacks       Stacks
	Links        controlplane.HouseholdLinkStore
	Identity     IdentityRuntime
	Issuers      Issuers
	Verifier     IDTokenVerifier
	LinkClientID string
	Now          func() time.Time
}

// Member is the person Cloud projects.
type Member struct {
	Subject     string
	Email       string
	DisplayName string
}

// ProjectionRequest is one projection from Cloud.
type ProjectionRequest struct {
	HintTenant   string
	OwnerSubject string
	Member       Member
	Action       string
	// Apps are the node app ids of add and update_apps (see AppGrant).
	Apps     AppGrant
	Revision uint64
}

// ProjectionResult is the outcome of a projection.
type ProjectionResult struct {
	Status        string
	StackID       string
	Username      string
	LinkStatus    string
	ActivationURL string
	ExpiresAt     *time.Time
	RemovedStacks []string
}

var (
	usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	unsafeUsername  = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

// Project applies one projection. An owner without a homelab is not an error:
// there is nothing to project.
func (s *Service) Project(ctx context.Context, req ProjectionRequest) (ProjectionResult, error) {
	owner := strings.TrimSpace(req.OwnerSubject)
	subject := strings.TrimSpace(req.Member.Subject)
	if owner == "" || subject == "" {
		return ProjectionResult{}, ErrMemberInvalid
	}
	if owner == subject {
		return ProjectionResult{}, ErrOwnerMember
	}
	var tenantID string
	var err error
	if system, ok := s.Tenants.(interface {
		ResolveProjectionOwnerTenant(context.Context, string, string) (string, error)
	}); ok && req.Revision > 0 {
		tenantID, err = system.ResolveProjectionOwnerTenant(ctx, owner, subject)
	} else {
		tenantID, err = s.Tenants.ResolveOwnerTenant(ctx, req.HintTenant, owner)
	}
	if err != nil {
		return ProjectionResult{}, err
	}
	if req.Revision > 0 {
		return s.projectRevision(ctx, tenantID, owner, req)
	}
	switch req.Action {
	case ActionAdd:
		return s.add(ctx, tenantID, owner, req.Member, req.Apps)
	case ActionUpdateApps:
		return s.updateApps(ctx, tenantID, owner, subject, req.Apps)
	case ActionRemove:
		return s.remove(ctx, tenantID, owner, req.Member)
	default:
		return ProjectionResult{}, fmt.Errorf("household: unsupported projection action %q", req.Action)
	}
}

func (s *Service) add(ctx context.Context, tenantID, owner string, member Member, apps AppGrant) (ProjectionResult, error) {
	member.Subject, member.Email, member.DisplayName = strings.TrimSpace(member.Subject), strings.TrimSpace(member.Email), strings.TrimSpace(member.DisplayName)
	if member.Email == "" || !strings.Contains(member.Email, "@") || len(member.Email) > 254 ||
		strings.ContainsAny(member.Email, "\r\n") || len(member.DisplayName) > 128 || strings.ContainsAny(member.DisplayName, "\r\n") {
		return ProjectionResult{}, ErrMemberInvalid
	}
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
	existing, err := s.Links.ListHouseholdRefsByOwner(ctx, tenantID, owner)
	if err != nil {
		return ProjectionResult{}, err
	}
	var lastErr error
	for _, stack := range stacks {
		username := s.usernameFor(existing, stack.ID, member)
		target := Target{TenantID: tenantID, OwnerID: owner, StackID: stack.ID}
		invitation, inviteErr := s.Identity.Invite(ctx, target, username, member.Email, member.DisplayName, apps)
		if inviteErr != nil {
			lastErr = inviteErr
			continue
		}
		if apps.Set && len(apps.Apps) == 0 {
			// The invite command cannot express "none"; close the member's
			// apps right after the invite. A failure is retried by Cloud.
			granted := strings.TrimSpace(invitation.Username)
			if granted == "" {
				granted = username
			}
			if noneErr := s.Identity.UpdateApps(ctx, target, granted, apps); noneErr != nil {
				lastErr = noneErr
				continue
			}
		}
		if strings.TrimSpace(invitation.Username) != "" {
			username = strings.TrimSpace(invitation.Username)
		}
		ref, storeErr := s.Links.UpsertHouseholdInvite(ctx, controlplane.HomelabIdentityRef{
			TenantID: tenantID, StackID: stack.ID, IdP: controlplane.HouseholdIdPPocketID, Username: username,
			Role: controlplane.HouseholdRoleMember, OwnerSubjectID: owner, CloudSubject: member.Subject,
			Email: member.Email, DisplayName: member.DisplayName,
		})
		if storeErr != nil {
			return ProjectionResult{}, storeErr
		}
		return ProjectionResult{
			Status: StatusProjected, StackID: stack.ID, Username: ref.Username, LinkStatus: ref.Status,
			ActivationURL: invitation.ActivationURL, ExpiresAt: invitation.ExpiresAt,
		}, nil
	}
	return ProjectionResult{}, fmt.Errorf("%w: %v", ErrNodeUnreachable, lastErr)
}

// identityStacks returns the owner's stacks of the homelab, preferring the ones
// that run a Pocket ID, oldest first.
func (s *Service) identityStacks(ctx context.Context, tenantID, owner, homelabID string) ([]controlplane.Stack, error) {
	all, err := s.Stacks.ListStacksByTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	var candidates []controlplane.Stack
	for _, stack := range all {
		if stack.HomelabID == homelabID && stack.OwnerSubjectID == owner {
			candidates = append(candidates, stack)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].CreatedAt.Before(candidates[j].CreatedAt) })
	if s.Issuers == nil {
		return candidates, nil
	}
	var withIdentity []controlplane.Stack
	for _, stack := range candidates {
		if issuer, issuerErr := s.Issuers.PocketIDIssuer(ctx, tenantID, stack.ID); issuerErr == nil && issuer != "" {
			withIdentity = append(withIdentity, stack)
		}
	}
	if len(withIdentity) > 0 {
		return withIdentity, nil
	}
	return candidates, nil
}

// usernameFor keeps the username a person already has on the stack, and
// otherwise derives a Pocket ID username from the e-mail address or name.
func (s *Service) usernameFor(existing []controlplane.HomelabIdentityRef, stackID string, member Member) string {
	taken := map[string]string{}
	for _, ref := range existing {
		if ref.StackID != stackID {
			continue
		}
		if ref.CloudSubject == member.Subject {
			return ref.Username
		}
		taken[strings.ToLower(ref.Username)] = ref.CloudSubject
	}
	base := member.Email
	if at := strings.Index(base, "@"); at > 0 {
		base = base[:at]
	}
	if base == "" {
		base = member.DisplayName
	}
	base = strings.Trim(unsafeUsername.ReplaceAllString(strings.ToLower(base), "-"), "-._")
	if base == "" {
		base = "member"
	}
	if len(base) > scopeIdentityMax-5 {
		base = base[:scopeIdentityMax-5]
	}
	candidate := base
	if _, clash := taken[strings.ToLower(candidate)]; clash {
		digest := sha256.Sum256([]byte(member.Subject))
		candidate = base + "-" + hex.EncodeToString(digest[:])[:4]
	}
	if !usernamePattern.MatchString(candidate) {
		digest := sha256.Sum256([]byte(member.Subject))
		candidate = "member-" + hex.EncodeToString(digest[:])[:8]
	}
	return candidate
}

// updateApps replaces the node apps of a member who already has a household
// identity on the owner's stack. It is idempotent. A member without one is
// pending (Cloud re-sends after the add); an owner without a homelab is none.
func (s *Service) updateApps(ctx context.Context, tenantID, owner, subject string, apps AppGrant) (ProjectionResult, error) {
	homelab, err := s.Homelabs.GetHomelabByOwner(ctx, tenantID, owner)
	if errors.Is(err, controlplane.ErrNotFound) || (err == nil && homelab == nil) {
		return ProjectionResult{Status: StatusNone}, nil
	}
	if err != nil {
		return ProjectionResult{}, err
	}
	refs, err := s.Links.ListHouseholdRefsByOwner(ctx, tenantID, owner)
	if err != nil {
		return ProjectionResult{}, err
	}
	result := ProjectionResult{Status: StatusPending}
	for _, ref := range refs {
		if ref.CloudSubject != subject {
			continue
		}
		target := Target{TenantID: tenantID, OwnerID: owner, StackID: ref.StackID}
		if err := s.Identity.UpdateApps(ctx, target, ref.Username, apps); err != nil {
			return ProjectionResult{}, fmt.Errorf("%w: %v", ErrNodeUnreachable, err)
		}
		if result.Status != StatusProjected {
			result = ProjectionResult{Status: StatusProjected, StackID: ref.StackID, Username: ref.Username, LinkStatus: ref.Status}
		}
	}
	return result, nil
}

func (s *Service) remove(ctx context.Context, tenantID, owner string, member Member) (ProjectionResult, error) {
	subject := strings.TrimSpace(member.Subject)
	refs, err := s.Links.ListHouseholdRefsByOwner(ctx, tenantID, owner)
	if err != nil {
		return ProjectionResult{}, err
	}
	result := ProjectionResult{Status: StatusProjected, RemovedStacks: []string{}}
	// Every stack is attempted on its own; one unreachable node never keeps
	// another node's account. Any failure leaves the removal pending, and the
	// caller's retry is idempotent.
	var pending error
	referenced := map[string]bool{}
	for _, ref := range refs {
		if ref.CloudSubject != subject {
			continue
		}
		referenced[ref.StackID] = true
		target := Target{TenantID: tenantID, OwnerID: owner, StackID: ref.StackID}
		if err := s.Identity.Remove(ctx, target, ref.Username); err != nil {
			pending = errors.Join(pending, fmt.Errorf("%w: %v", ErrNodeUnreachable, err))
			continue
		}
		if _, err := s.Links.DeleteHouseholdRef(ctx, tenantID, ref.StackID, subject); err != nil && !errors.Is(err, controlplane.ErrNotFound) {
			return ProjectionResult{}, err
		}
		result.RemovedStacks = append(result.RemovedStacks, ref.StackID)
	}
	unreferenced, err := s.removeUnreferenced(ctx, tenantID, owner, member, refs, referenced)
	result.RemovedStacks = append(result.RemovedStacks, unreferenced...)
	if pending = errors.Join(pending, err); pending != nil {
		return ProjectionResult{}, pending
	}
	return result, nil
}

// removeUnreferenced removes the household account of a person that no saved
// reference names on one of the owner's identity stacks. An invite can create
// the account on the node and still fail to answer (a cut connection, a
// failed setup link); no reference is saved then, and a removal that only
// followed references left the account with every member app (2026-10-20,
// platform-y0502). The node's own list names the account by e-mail address:
// exactly one household account with that address is removed, an account that
// a reference gives to another person is never touched, several matches are
// refused, and StackKits refuses the owner and administrators. It is not used
// by the revisioned projection, whose binding is committed before any node
// effect and whose removal is fenced by its revision.
func (s *Service) removeUnreferenced(
	ctx context.Context,
	tenantID, owner string,
	member Member,
	refs []controlplane.HomelabIdentityRef,
	skipStacks map[string]bool,
) ([]string, error) {
	email := strings.TrimSpace(member.Email)
	lister, ok := s.Identity.(IdentityLister)
	if !ok || email == "" || s.Homelabs == nil || s.Stacks == nil {
		return nil, nil
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
	removed := []string{}
	var pending error
	for _, stack := range stacks {
		if skipStacks[stack.ID] {
			continue
		}
		heldByOthers := map[string]bool{}
		for _, ref := range refs {
			if ref.StackID == stack.ID && ref.CloudSubject != member.Subject {
				heldByOthers[strings.ToLower(ref.Username)] = true
			}
		}
		target := Target{TenantID: tenantID, OwnerID: owner, StackID: stack.ID}
		users, listErr := lister.ListHouseholdUsers(ctx, target)
		if listErr != nil {
			pending = errors.Join(pending, fmt.Errorf("%w: %v", ErrNodeUnreachable, listErr))
			continue
		}
		matches := []string{}
		for _, user := range users {
			username := strings.TrimSpace(user.Username)
			if username != "" && strings.EqualFold(strings.TrimSpace(user.Email), email) {
				matches = append(matches, username)
			}
		}
		switch {
		case len(matches) == 0:
			continue
		case len(matches) > 1:
			pending = errors.Join(pending, fmt.Errorf("%w: stack %s holds %d household accounts with the member's e-mail address",
				ErrAmbiguousAccount, stack.ID, len(matches)))
			continue
		case heldByOthers[strings.ToLower(matches[0])]:
			continue
		}
		if err := s.Identity.Remove(ctx, target, matches[0]); err != nil {
			pending = errors.Join(pending, fmt.Errorf("%w: %v", ErrNodeUnreachable, err))
			continue
		}
		removed = append(removed, stack.ID)
	}
	return removed, pending
}

// LinkRequest names the member and the owner whose household they joined.
type LinkRequest struct {
	HintTenant    string
	OwnerSubject  string
	MemberSubject string
	StackID       string
}

// LinkBegin is what Cloud needs to run the OIDC login against the node.
type LinkBegin struct {
	ReferenceID string
	StackID     string
	Username    string
	Issuer      string
	ClientID    string
	Scope       string
}

// BeginLink resolves the invited reference and the stack's Pocket ID issuer.
func (s *Service) BeginLink(ctx context.Context, req LinkRequest) (LinkBegin, error) {
	_, ref, issuer, err := s.invitedRef(ctx, req)
	if err != nil {
		return LinkBegin{}, err
	}
	return LinkBegin{ReferenceID: ref.ReferenceID, StackID: ref.StackID, Username: ref.Username, Issuer: issuer, ClientID: s.clientID(), Scope: LinkScopes}, nil
}

// LinkProof is the Pocket ID login the member completed.
type LinkProof struct {
	LinkRequest
	ReferenceID string
	IDToken     string
	Nonce       string
}

// CompleteLink verifies the member's Pocket ID login and marks the reference
// linked. The ID token must verify against the issuer Techstack knows for the
// stack, carry the nonce Cloud used, and name the invited person: the Pocket ID
// username, or else the invited e-mail address.
func (s *Service) CompleteLink(ctx context.Context, proof LinkProof) (*controlplane.HomelabIdentityRef, error) {
	if strings.TrimSpace(proof.IDToken) == "" || strings.TrimSpace(proof.Nonce) == "" || proof.ReferenceID == "" || proof.StackID == "" {
		return nil, ErrProofInvalid
	}
	_, ref, issuer, err := s.invitedRef(ctx, proof.LinkRequest)
	if err != nil {
		return nil, err
	}
	if ref.ReferenceID != proof.ReferenceID {
		return nil, ErrNoReference
	}
	claims, err := s.Verifier.Verify(ctx, issuer, s.clientID(), proof.IDToken)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProofInvalid, err)
	}
	if nonce, _ := claims.Raw["nonce"].(string); subtle.ConstantTimeCompare([]byte(nonce), []byte(proof.Nonce)) != 1 {
		return nil, fmt.Errorf("%w: nonce mismatch", ErrProofInvalid)
	}
	if !claimsNameInvitee(claims, ref) {
		return nil, fmt.Errorf("%w: the login is not the invited person", ErrProofInvalid)
	}
	return s.Links.MarkHouseholdLinked(ctx, *ref, s.now())
}

func claimsNameInvitee(claims *oidcclient.Claims, ref *controlplane.HomelabIdentityRef) bool {
	if username, _ := claims.Raw["preferred_username"].(string); username != "" {
		return strings.EqualFold(strings.TrimSpace(username), ref.Username)
	}
	return claims.Email != "" && strings.EqualFold(strings.TrimSpace(claims.Email), ref.Email)
}

func (s *Service) invitedRef(ctx context.Context, req LinkRequest) (string, *controlplane.HomelabIdentityRef, string, error) {
	owner, member := strings.TrimSpace(req.OwnerSubject), strings.TrimSpace(req.MemberSubject)
	if owner == "" || member == "" || owner == member {
		return "", nil, "", ErrNoReference
	}
	tenantID, err := s.Tenants.ResolveOwnerTenant(ctx, req.HintTenant, owner)
	if err != nil {
		return "", nil, "", err
	}
	refs, err := s.Links.ListHouseholdRefsByOwner(ctx, tenantID, owner)
	if err != nil {
		return "", nil, "", err
	}
	var matches []controlplane.HomelabIdentityRef
	for _, ref := range refs {
		if ref.CloudSubject == member && (req.StackID == "" || ref.StackID == strings.TrimSpace(req.StackID)) {
			matches = append(matches, ref)
		}
	}
	switch len(matches) {
	case 0:
		return "", nil, "", ErrNoReference
	case 1:
	default:
		return "", nil, "", ErrAmbiguousStack
	}
	if s.Issuers == nil {
		return "", nil, "", ErrIssuerUnavailable
	}
	issuer, err := s.Issuers.PocketIDIssuer(ctx, tenantID, matches[0].StackID)
	if err != nil || issuer == "" {
		return "", nil, "", ErrIssuerUnavailable
	}
	return tenantID, &matches[0], issuer, nil
}

// OwnerLinks is the household of one owner, per stack.
type OwnerLinks struct {
	OwnerSubject string
	Stacks       []StackLinks
}

// StackLinks lists the linked and the still invited members of one stack.
type StackLinks struct {
	StackID string
	Linked  []controlplane.HomelabIdentityRef
	Invited []controlplane.HomelabIdentityRef
}

// ListLinks returns the owner's household references grouped by stack. When
// onlyMember is set the answer holds only that person's references.
func (s *Service) ListLinks(ctx context.Context, hintTenant, ownerSubject, onlyMember string) (OwnerLinks, error) {
	owner := strings.TrimSpace(ownerSubject)
	tenantID, err := s.Tenants.ResolveOwnerTenant(ctx, hintTenant, owner)
	if err != nil {
		return OwnerLinks{}, err
	}
	refs, err := s.Links.ListHouseholdRefsByOwner(ctx, tenantID, owner)
	if err != nil {
		return OwnerLinks{}, err
	}
	out := OwnerLinks{OwnerSubject: owner, Stacks: []StackLinks{}}
	index := map[string]int{}
	for _, ref := range refs {
		if onlyMember != "" && ref.CloudSubject != onlyMember {
			continue
		}
		at, ok := index[ref.StackID]
		if !ok {
			at = len(out.Stacks)
			index[ref.StackID] = at
			out.Stacks = append(out.Stacks, StackLinks{StackID: ref.StackID, Linked: []controlplane.HomelabIdentityRef{}, Invited: []controlplane.HomelabIdentityRef{}})
		}
		if ref.Status == controlplane.HouseholdLinkLinked {
			out.Stacks[at].Linked = append(out.Stacks[at].Linked, ref)
		} else {
			out.Stacks[at].Invited = append(out.Stacks[at].Invited, ref)
		}
	}
	return out, nil
}

func (s *Service) clientID() string {
	if id := strings.TrimSpace(s.LinkClientID); id != "" {
		return id
	}
	return DefaultLinkClientID
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}
