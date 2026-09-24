package substrate

import (
	"context"
	"encoding/hex"
	"strings"
	"time"
)

type OwnedGuestObservation struct {
	Identity    GuestIdentity    `json:"identity"`
	Observation GuestObservation `json:"observation"`
	SpecDigest  string           `json:"spec_digest"`
	ObservedAt  time.Time        `json:"observed_at"`
}

// GuardObservation is the secret-free inventory projection of local authority.
// It conveys capabilities, never a token, token path or certificate credential.
type GuardObservation struct {
	Node        string                        `json:"node"`
	MinGuestID  int                           `json:"min_guest_id"`
	MaxGuestID  int                           `json:"max_guest_id"`
	Images      map[string]Image              `json:"images"`
	Inventory   Inventory                     `json:"inventory"`
	OwnedGuests map[int]OwnedGuestObservation `json:"owned_guests,omitempty"`
}

// ObserveOwnedGuests augments the authenticated heartbeat with bounded, fresh
// node-local evidence. It never scans guest addresses or includes guest config.
// Missing/failed QGA observations remain absent; earlier evidence is not reused.
func (c *Client) ObserveOwnedGuests(ctx context.Context, inventory Inventory) map[int]OwnedGuestObservation {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	observed := map[int]OwnedGuestObservation{}
	attempted := 0
	for _, candidate := range inventory.Guests {
		if ctx.Err() != nil || attempted >= 32 {
			break
		}
		identity, ok := c.managedIdentity(candidate)
		if !ok {
			continue
		}
		attempted++
		func() {
			probe, stop := context.WithTimeout(ctx, 2*time.Second)
			defer stop()
			guest, err := c.Guest(probe, identity.ID)
			currentIdentity, currentOwned := c.managedIdentity(guest)
			if err != nil || !currentOwned || currentIdentity != identity {
				return
			}
			description, ok := guest.Config["description"].(string)
			if !ok || !strings.HasPrefix(description, "kombify-spec-sha256:") {
				return
			}
			digest := strings.TrimPrefix(description, "kombify-spec-sha256:")
			decoded, err := hex.DecodeString(digest)
			if err != nil || len(decoded) != 32 {
				return
			}
			state, err := c.ObserveGuest(probe, identity)
			if err != nil || !state.Present || state.Locked || state.Status != "running" || !state.DiskAttached || len(state.Addresses) == 0 {
				return
			}
			if len(state.Addresses) > 64 {
				return
			}
			observed[identity.ID] = OwnedGuestObservation{Identity: identity, Observation: state, SpecDigest: "sha256:" + digest, ObservedAt: time.Now().UTC()}
		}()
	}
	return observed
}

func (c *Client) managedIdentity(guest Guest) (GuestIdentity, bool) {
	identity := GuestIdentity{ID: guest.ID, Name: guest.Name}
	for _, tag := range strings.Split(guest.Tags, ";") {
		if operationTag.MatchString(tag) {
			if identity.OperationTag != "" {
				return GuestIdentity{}, false
			}
			identity.OperationTag = tag
		}
	}
	return identity, c.validateIdentity(identity) == nil && owns(identity, guest)
}

func (o GuardObservation) Validate() error {
	if !identifier.MatchString(o.Node) || o.MinGuestID < 100 || o.MaxGuestID < o.MinGuestID || o.MaxGuestID > 999999999 {
		return ErrCapability
	}
	for _, p := range Profiles() {
		pin, err := DefaultImage(p.ID)
		if err != nil || o.Images[p.ID] != pin {
			return ErrCapability
		}
	}
	return nil
}
