package stackidentity

import (
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

// The sync rules decide which side's name survives; a wrong decision silently
// renames the owner's homelab or leaves two names side by side.
func TestReconcileKeepsOneStackIdentity(t *testing.T) {
	t0 := time.Date(2026, 9, 24, 20, 51, 0, 0, time.UTC)
	rainbow := controlplane.StackIdentityPresentation{CharacterID: "unicorn", AnimationStyle: "identity-rainbow"}
	cloud := func(revision int, name string, updated time.Time) *Cloud {
		return &Cloud{Revision: revision, Name: name, Presentation: rainbow, UpdatedAt: updated}
	}

	cases := []struct {
		name   string
		local  Local
		cloud  *Cloud
		action Action
		match  int
	}{
		{"generated local name, no Cloud record", Local{Name: "homelab"}, nil, ActionNone, 0},
		{"Techstack-first name is adopted into Cloud", Local{Name: "Home Base", Named: true, Pending: true, EditedAt: t0}, nil, ActionPushLocal, 0},
		{"first link: Cloud wins over a generated name", Local{Name: "homelab"}, cloud(1, "Nebula Lab", t0), ActionAdoptCloud, 0},
		{"first link: Cloud wins over a local name", Local{Name: "Home Base", Named: true, Pending: true, EditedAt: t0.Add(time.Hour)}, cloud(3, "Nebula Lab", t0), ActionAdoptCloud, 0},
		{"push confirmation", Local{Name: "Nebula Lab", Named: true, Pending: true, CloudRevision: 0, EditedAt: t0}, cloud(1, "Nebula Lab", t0), ActionInSync, 0},
		{"Cloud renamed while linked", Local{Name: "Nebula Lab", Named: true, CloudRevision: 1, EditedAt: t0}, cloud(2, "Home Base", t0.Add(time.Hour)), ActionAdoptCloud, 0},
		{"offline local edit, Cloud unchanged", Local{Name: "Home Base", Named: true, Pending: true, CloudRevision: 2, EditedAt: t0.Add(time.Hour)}, cloud(2, "Nebula Lab", t0), ActionPushLocal, 2},
		{"both changed, local later", Local{Name: "Home Base", Named: true, Pending: true, CloudRevision: 2, EditedAt: t0.Add(2 * time.Hour)}, cloud(3, "Orbit", t0.Add(time.Hour)), ActionPushLocal, 3},
		{"both changed, Cloud later", Local{Name: "Home Base", Named: true, Pending: true, CloudRevision: 2, EditedAt: t0.Add(time.Hour)}, cloud(3, "Orbit", t0.Add(2*time.Hour)), ActionAdoptCloud, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Reconcile(tc.local, tc.cloud)
			if got.Action != tc.action || (got.Action == ActionPushLocal && got.IfMatch != tc.match) {
				t.Fatalf("Reconcile = %+v, want %s (If-Match %d)", got, tc.action, tc.match)
			}
		})
	}
}
