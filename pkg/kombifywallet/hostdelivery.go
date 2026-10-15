package kombifywallet

import (
	"context"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/walletservice"
)

// Environment of the Wallet's public delivering key. The Wallet signs every
// host delivery with it (`WALLET_HOST_DELIVERY_KEY` holds the private half),
// and a node opens only deliveries signed by the key it pinned.
const (
	EnvHostDeliveryPublicKey = "TECHSTACK_WALLET_HOST_DELIVERY_PUBLIC_KEY"
	EnvHostDeliveryKeyID     = "TECHSTACK_WALLET_HOST_DELIVERY_KEY_ID"
)

var hostDeliveryKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// HostAuthority is the Wallet's public delivering key: an unpadded standard
// base64 Ed25519 public key and its key id.
type HostAuthority struct {
	Key   string
	KeyID string
}

// HostAuthorityFromEnv reads the delivering key. ok is false when it is not
// configured; a malformed value is an error.
func HostAuthorityFromEnv(getenv func(string) string) (HostAuthority, bool, error) {
	key, keyID := strings.TrimSpace(getenv(EnvHostDeliveryPublicKey)), strings.TrimSpace(getenv(EnvHostDeliveryKeyID))
	if key == "" && keyID == "" {
		return HostAuthority{}, false, nil
	}
	raw, err := base64.RawStdEncoding.Strict().DecodeString(key)
	if err != nil || len(raw) != 32 || !hostDeliveryKeyIDPattern.MatchString(keyID) {
		return HostAuthority{}, false, errors.New(EnvHostDeliveryPublicKey + " must be an unpadded base64 Ed25519 public key and " + EnvHostDeliveryKeyID + " its key id")
	}
	return HostAuthority{Key: key, KeyID: keyID}, true, nil
}

// HostDelivery is a sealed delivery of an item version to one node.
type HostDelivery struct {
	Envelope []byte
	Version  string
}

// HostDeliver has the Wallet seal an item's newest value to the StackKits node
// whose owner-signed recipient statement answers challenge. The item must list
// the node as a host target; the Wallet refuses otherwise
// (`host_target_unknown`). Failures are Denials.
func (c *Client) HostDeliver(ctx context.Context, owner, itemID string, rotate bool, siteRef, nodeRef string, statement []byte, challenge string, expiresAt time.Time) (HostDelivery, error) {
	wallet, err := c.service()
	if err != nil {
		return HostDelivery{}, err
	}
	if !IsItemID(itemID) {
		return HostDelivery{}, ItemInvalid()
	}
	delivery := walletservice.HostDeliveryItemImport
	if rotate {
		delivery = walletservice.HostDeliveryItemRotate
	}
	sealed, err := wallet.HostDeliver(ctx, owner, itemID, walletservice.HostDeliveryRequest{
		Delivery: delivery, SiteRef: siteRef, NodeRef: nodeRef,
		Statement: statement, Challenge: challenge, ExpiresAt: expiresAt,
	})
	if err != nil {
		return HostDelivery{}, Denied(err)
	}
	return HostDelivery{Envelope: sealed.Envelope, Version: sealed.Version}, nil
}
