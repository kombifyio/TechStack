package main

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/kombifyio/techstack/internal/managedstackkit"
	"github.com/kombifyio/techstack/pkg/kombifywallet"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/stackkits/pkg/hostdelivery"
)

// managedBackupWalletItem names the stack's backup-target item in the owner's
// Wallet. It carries no personal data.
const managedBackupWalletItem = "Managed off-site backup target"

// walletBackupTarget stores managed backup credentials in the owner's kombify
// Wallet and has the Wallet deliver them to the node.
type walletBackupTarget struct {
	wallet    *kombifywallet.Client
	authority kombifywallet.HostAuthority
}

// managedBackupWallet returns nil unless both the Wallet client and its
// delivering key are configured, so Wallet-mode backup delivery fails closed.
func managedBackupWallet(wallet *kombifywallet.Client, log *logger.Logger) managedstackkit.BackupWallet {
	authority, ok, err := kombifywallet.HostAuthorityFromEnv(os.Getenv)
	if !wallet.Enabled() || err != nil || !ok {
		log.Warn("managed_backup_wallet_unavailable", "reason", "wallet_or_delivering_key_not_configured", "error", err)
		return nil
	}
	return walletBackupTarget{wallet: wallet, authority: authority}
}

func (w walletBackupTarget) Authority() (string, string, error) {
	return w.authority.Key, w.authority.KeyID, nil
}

func (w walletBackupTarget) StoreBackupTarget(ctx context.Context, owner, stackID string, material []byte) (string, error) {
	scope, ok := kombifywallet.ProjectScope(stackID)
	if !ok {
		return "", errors.New("the stack id is not a kombify Wallet project reference")
	}
	result, err := w.wallet.Ensure(ctx, owner, kombifywallet.ItemSpec{
		Type: kombifywallet.ItemTypeGenericSecret, DisplayName: managedBackupWalletItem, Scope: scope,
		Description: "S3 target and repository passphrase of the kombify-managed off-site backup; delivered only to the stack's server.",
		Value:       material,
	})
	if err != nil {
		return "", err
	}
	return result.ItemID, nil
}

func (w walletBackupTarget) DeliverBackupTarget(ctx context.Context, owner, itemID, operation string, target managedstackkit.HostTarget, statement []byte, challenge string, expiresAt time.Time) ([]byte, error) {
	delivered, err := w.wallet.HostDeliverBackupTarget(ctx, owner, itemID, operation == hostdelivery.OperationBackupTargetRebind, target.SiteRef, target.NodeRef, target.PlacementRef, statement, challenge, expiresAt)
	if err != nil {
		return nil, err
	}
	return delivered.Envelope, nil
}
