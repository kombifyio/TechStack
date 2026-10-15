package kombifywallet

import (
	"context"
	"crypto/subtle"
	"slices"

	"github.com/kombifyio/techstack/internal/gocommon/walletservice"
)

// Item types Techstack creates, re-exported so callers import one package.
const (
	ItemTypeGenericSecret = walletservice.ItemTypeGenericSecret
	ItemTypeConfiguration = walletservice.ItemTypeConfiguration
	ItemTypeNodeAccess    = walletservice.ItemTypeNodeAccessProfile
)

// ItemSpec describes one Wallet item Techstack writes for an owner. Its
// identity inside the owner's Wallet is the triple scope, type and display
// name (the Name for a configuration item), so a repeated write converges on
// one item.
type ItemSpec struct {
	Type string
	// DisplayName is shown in the Wallet. It must not carry personal data.
	DisplayName string
	Description string
	// Name is required for configuration items and refused for the others.
	Name string
	// Scope is account or project:<stack>; see ProjectScope.
	Scope string
	// Value is sealed to the Wallet's capture key. The caller owns the slice.
	Value []byte
}

// EnsureResult says what Ensure did.
type EnsureResult struct {
	ItemID string
	// Created is true when the write made a new item; Versioned when it added a
	// version to an existing one because the value had changed.
	Created   bool
	Versioned bool
}

// Create stores a new item for the owner and returns its Wallet item id. The
// Gateway grants Techstack use and manage on the new item.
func (c *Client) Create(ctx context.Context, owner string, spec ItemSpec) (string, error) {
	wallet, err := c.service()
	if err != nil {
		return "", err
	}
	receipt, err := wallet.Create(ctx, createInput(owner, spec))
	if err != nil {
		return "", Denied(err)
	}
	return receipt.ItemID, nil
}

func createInput(owner string, spec ItemSpec) walletservice.CreateInput {
	return walletservice.CreateInput{
		Owner: owner, ItemType: spec.Type, DisplayName: spec.DisplayName, Description: spec.Description,
		Name: spec.Name, Scope: spec.Scope, Value: spec.Value,
	}
}

// Ensure makes sure the owner's Wallet holds the item with this value. It
// lists the items Techstack may manage, finds the one with the spec's identity
// and either creates it, leaves it alone, or adds a version when the stored
// value differs. A concurrent Ensure of the same spec may create twice; the
// next call finds the earlier one first and converges on it.
func (c *Client) Ensure(ctx context.Context, owner string, spec ItemSpec) (EnsureResult, error) {
	wallet, err := c.service()
	if err != nil {
		return EnsureResult{}, err
	}
	items, err := wallet.List(ctx, owner)
	if err != nil {
		return EnsureResult{}, Denied(err)
	}
	existing := findItem(items, spec)
	if existing == nil {
		itemID, err := c.Create(ctx, owner, spec)
		return EnsureResult{ItemID: itemID, Created: err == nil}, err
	}
	current, err := wallet.Deliver(ctx, owner, existing.ItemID)
	if err != nil {
		return EnsureResult{}, Denied(err)
	}
	defer current.Wipe()
	if subtle.ConstantTimeCompare(current.Bytes(), spec.Value) == 1 {
		return EnsureResult{ItemID: existing.ItemID}, nil
	}
	if _, err := wallet.AddVersion(ctx, walletservice.VersionInput{
		Owner: owner, ItemID: existing.ItemID, BasedOn: current.Version, Value: spec.Value,
	}); err != nil {
		return EnsureResult{}, Denied(err)
	}
	return EnsureResult{ItemID: existing.ItemID, Versioned: true}, nil
}

// findItem returns the active item with the spec's identity.
func findItem(items []walletservice.Item, spec ItemSpec) *walletservice.Item {
	index := slices.IndexFunc(items, func(item walletservice.Item) bool {
		if item.ItemType != spec.Type || item.Scope != spec.Scope || (item.State != "" && item.State != "active") {
			return false
		}
		if spec.Name != "" {
			return item.Name == spec.Name
		}
		return item.DisplayName == spec.DisplayName
	})
	if index < 0 {
		return nil
	}
	return &items[index]
}

// Items lists the owner's Wallet items Techstack holds a right on: display
// metadata, scope, state and rights, never a value. The migration finds the
// items an interrupted run left behind with it.
func (c *Client) Items(ctx context.Context, owner string) ([]walletservice.Item, error) {
	wallet, err := c.service()
	if err != nil {
		return nil, err
	}
	items, err := wallet.List(ctx, owner)
	if err != nil {
		return nil, Denied(err)
	}
	return items, nil
}

// Reveal delivers the current value of an item Techstack wrote, for example the
// recovery hash an owner spec hands to a StackKit. The caller wipes the result.
func (c *Client) Reveal(ctx context.Context, owner, itemID string) (walletservice.Secret, error) {
	wallet, err := c.service()
	if err != nil {
		return walletservice.Secret{}, err
	}
	delivered, err := wallet.Deliver(ctx, owner, itemID)
	if err != nil {
		return walletservice.Secret{}, Denied(err)
	}
	return delivered.Secret, nil
}
