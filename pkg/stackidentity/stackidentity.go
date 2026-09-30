// Package stackidentity holds the Stack Identity rules Techstack applies to its
// local copy (the homelab row) and to the sync with kombify Cloud.
//
// Authority (STACK-IDENTITY-CUSTOMIZATION-STANDARD §8):
//   - Without kombify Cloud, Techstack is the only authority. Local edits are
//     final and nothing is ever pushed.
//   - Connected to Cloud, Cloud's StackIdentityV1 record is the account
//     authority and Techstack keeps a revisioned copy. There is exactly one
//     name: the homelab name is the Stack Identity name.
package stackidentity

import (
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

// MaxNameLength is StackIdentityV1's name bound (controlplane owns the rule
// because the homelab rename shares it).
const MaxNameLength = controlplane.MaxStackIdentityNameLength

// DefaultCharacterID completes a name-only identity for Cloud, which requires
// a character on create. It matches the open-core default identity.
const DefaultCharacterID = "rocket"

// Local is the Techstack copy as the homelab row stores it.
type Local struct {
	Name string
	// Named is false while the homelab still carries its generated name; a
	// generated name is never offered to Cloud.
	Named         bool
	Presentation  *controlplane.StackIdentityPresentation
	EditedAt      time.Time
	CloudRevision int
	Pending       bool
}

// Cloud is kombify Cloud's StackIdentityV1 record.
type Cloud struct {
	Revision     int
	Name         string
	Presentation controlplane.StackIdentityPresentation
	UpdatedAt    time.Time
}

// Action is what the sync must do next.
type Action string

const (
	// ActionNone: nothing to sync (no Cloud record and no chosen local name).
	ActionNone Action = "none"
	// ActionInSync: both sides carry the same identity; record the revision.
	ActionInSync Action = "in_sync"
	// ActionAdoptCloud: overwrite the local copy with Cloud's record.
	ActionAdoptCloud Action = "adopt_cloud"
	// ActionPushLocal: write the local copy to Cloud with If-Match: IfMatch.
	ActionPushLocal Action = "push_local"
)

// Decision is the outcome of Reconcile.
type Decision struct {
	Action  Action
	IfMatch int
	Reason  string
}

// Reconcile decides one sync step between the local copy and Cloud's record
// (nil when Cloud has none). The rules, in order:
//
//  1. Same content on both sides: in sync. This is also how a successful push
//     is confirmed - the client passes Cloud's response back.
//  2. Cloud has no record: a chosen local name is pushed with If-Match "0"
//     (Techstack-first naming adopts the local name into Cloud instead of
//     creating a second one); a generated name stays local.
//  3. First link (never matched a Cloud revision) and Cloud has a record:
//     Cloud wins. The account identity other products already show is not
//     silently renamed by a local instance.
//  4. Linked, no local edit pending: Cloud's newer record is adopted.
//  5. Linked, local edit pending, Cloud unchanged since the last match: the
//     offline edit is pushed.
//  6. Both sides changed: the later edit wins (local named_at against Cloud's
//     updated_at); a tie goes to Cloud.
func Reconcile(local Local, cloud *Cloud) Decision {
	if cloud == nil {
		if local.Named {
			return Decision{Action: ActionPushLocal, IfMatch: 0, Reason: "cloud_identity_missing"}
		}
		return Decision{Action: ActionNone, Reason: "no_identity"}
	}
	if sameIdentity(local, *cloud) {
		return Decision{Action: ActionInSync, Reason: "identical"}
	}
	switch {
	case local.CloudRevision == 0:
		return Decision{Action: ActionAdoptCloud, Reason: "first_link_cloud_wins"}
	case !local.Pending:
		return Decision{Action: ActionAdoptCloud, Reason: "cloud_newer"}
	case cloud.Revision == local.CloudRevision:
		return Decision{Action: ActionPushLocal, IfMatch: cloud.Revision, Reason: "local_edit_pending"}
	case local.EditedAt.After(cloud.UpdatedAt):
		return Decision{Action: ActionPushLocal, IfMatch: cloud.Revision, Reason: "conflict_local_later"}
	default:
		return Decision{Action: ActionAdoptCloud, Reason: "conflict_cloud_later"}
	}
}

// sameIdentity compares what a user sees. Presentation fields the local copy
// never set are not a difference: Cloud fills them with defaults.
func sameIdentity(local Local, cloud Cloud) bool {
	if !local.Named || strings.TrimSpace(local.Name) != strings.TrimSpace(cloud.Name) {
		return false
	}
	if local.Presentation == nil {
		return true
	}
	p := local.Presentation
	c := cloud.Presentation
	if p.CharacterID != "" && p.CharacterID != c.CharacterID {
		return false
	}
	if p.AnimationStyle != "" && p.AnimationStyle != c.AnimationStyle {
		return false
	}
	if p.IconStyle != "" && p.IconStyle != c.IconStyle {
		return false
	}
	if p.AnimationEnabled != nil && (c.AnimationEnabled == nil || *p.AnimationEnabled != *c.AnimationEnabled) {
		return false
	}
	return glowEqual(p.GlowColorOverride, c.GlowColorOverride)
}

func glowEqual(local, cloud *string) bool {
	if local == nil {
		return true
	}
	return cloud != nil && strings.EqualFold(*local, *cloud)
}
