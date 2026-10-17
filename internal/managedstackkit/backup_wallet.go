package managedstackkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"github.com/kombifyio/stackkits/pkg/hostdelivery"
)

// ErrBackupWalletUnavailable fails a managed backup delivery in Wallet mode
// when the Wallet client or its delivering key is not configured. Techstack
// never falls back to sealing the credentials itself there.
var ErrBackupWalletUnavailable = errors.New("managed backup delivery in kombify Wallet mode needs the Wallet client and its delivering key (" +
	"TECHSTACK_WALLET_HOST_DELIVERY_PUBLIC_KEY, TECHSTACK_WALLET_HOST_DELIVERY_KEY_ID); Techstack does not seal backup credentials itself in Wallet mode")

// BackupWallet is the kombify Wallet as managed backup delivery sees it.
type BackupWallet interface {
	// Authority is the Wallet's public delivering key a node must pin before
	// the Wallet seals to it.
	Authority() (key, keyID string, err error)
	// StoreBackupTarget keeps material as the stack's backup-target item in
	// the owner's Wallet (created or versioned) and returns its item id.
	StoreBackupTarget(ctx context.Context, owner, stackID string, material []byte) (string, error)
	// DeliverBackupTarget has the Wallet seal and sign the item to the node
	// whose statement answers challenge, as backup-target-import or
	// backup-target-rebind. The item must list the node as a host target.
	DeliverBackupTarget(ctx context.Context, owner, itemID, operation string, target HostTarget, statement []byte, challenge string, expiresAt time.Time) ([]byte, error)
}

// HostTarget is the public host target the owner lists on the Wallet item so
// the Wallet admits the node.
type HostTarget struct {
	SiteRef        string
	NodeRef        string
	TenantRef      string
	OwnerPublicKey string
	// PlacementRef names the server in the Wallet's delivery record so a
	// detach withdraws the node; it is not part of the host target.
	PlacementRef string
}

// WalletAuthority returns the delivering key the node must pin before a
// delivery. wallet is false outside Wallet mode; in Wallet mode without a
// configured Wallet it is ErrBackupWalletUnavailable.
func (d *BackupDelivery) WalletAuthority() (key, keyID string, wallet bool, err error) {
	if d == nil || !d.walletMode {
		return "", "", false, nil
	}
	if d.wallet == nil {
		return "", "", true, ErrBackupWalletUnavailable
	}
	key, keyID, err = d.wallet.Authority()
	if err == nil && (key == "" || keyID == "") {
		err = ErrBackupWalletUnavailable
	}
	return key, keyID, true, err
}

func (d *BackupDelivery) deliverThroughWallet(ctx context.Context, scope DeliveryScope, recipient Recipient, operation string, material []byte) ([]byte, error) {
	if scope.OwnerID == "" || len(recipient.Statement) == 0 || recipient.Challenge == "" {
		return nil, errors.New("managed backup delivery through the kombify Wallet requires the stack owner and the node's recipient statement")
	}
	_, keyID, _, err := d.WalletAuthority()
	if err != nil {
		return nil, err
	}
	var statement hostdelivery.Statement
	if err := json.Unmarshal(recipient.Statement, &statement); err != nil || statement.AuthorityKeyID != keyID {
		return nil, errors.New("the node has not pinned the kombify Wallet delivering key; its recipient statement names another or no delivering authority")
	}
	itemID, err := d.wallet.StoreBackupTarget(ctx, scope.OwnerID, scope.StackID, material)
	if err != nil {
		return nil, fmt.Errorf("store the managed backup target in the kombify Wallet: %w", err)
	}
	target := HostTarget{SiteRef: recipient.SiteRef, NodeRef: recipient.NodeRef, TenantRef: stackkitcommand.DeliveryTenantRef(scope.TenantID), OwnerPublicKey: statement.OwnerPublic, PlacementRef: recipient.PlacementRef}
	envelope, err := d.wallet.DeliverBackupTarget(ctx, scope.OwnerID, itemID, operation, target, recipient.Statement, recipient.Challenge, d.now().Add(deliveryLifetime))
	if err != nil {
		// The host target is public; the owner lists it on the item so the
		// Wallet admits this node.
		return nil, fmt.Errorf("the kombify Wallet did not deliver backup target item %s to this node (host target site_ref=%s node_ref=%s tenant_ref=%s owner_public_key=%s): %w",
			itemID, target.SiteRef, target.NodeRef, target.TenantRef, target.OwnerPublicKey, err)
	}
	return envelope, nil
}
