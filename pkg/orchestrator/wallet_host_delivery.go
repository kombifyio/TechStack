package orchestrator

import (
	"context"
	"errors"
	"time"

	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/kombifywallet"
)

// walletHostDeliveryAdapter seals kombify Wallet items to StackKits nodes for
// ai_provider through the configured Wallet client.
type walletHostDeliveryAdapter struct {
	wallet    *kombifywallet.Client
	authority kombifywallet.HostAuthority
}

// walletHostDelivery returns nil unless both the Wallet and its delivering key
// are configured, so ai_provider fails closed.
func walletHostDelivery(wallet *kombifywallet.Client, authority *kombifywallet.HostAuthority) jobs.WalletHostDelivery {
	if !wallet.Enabled() || authority == nil {
		return nil
	}
	return walletHostDeliveryAdapter{wallet: wallet, authority: *authority}
}

func (a walletHostDeliveryAdapter) Authority() (string, string, error) {
	if a.authority.Key == "" || a.authority.KeyID == "" {
		return "", "", errors.New("kombify Wallet delivering key is not configured")
	}
	return a.authority.Key, a.authority.KeyID, nil
}

func (a walletHostDeliveryAdapter) DeliverItem(ctx context.Context, owner, itemID string, rotate bool, target jobs.WalletHostTarget, statement []byte, challenge string, expiresAt time.Time) (jobs.WalletHostEnvelope, error) {
	sealed, err := a.wallet.HostDeliver(ctx, owner, itemID, rotate, target.SiteRef, target.NodeRef, statement, challenge, expiresAt)
	if err != nil {
		return jobs.WalletHostEnvelope{}, err
	}
	return jobs.WalletHostEnvelope{Envelope: sealed.Envelope, Version: sealed.Version}, nil
}
