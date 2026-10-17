package kombifywallet

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

// PlacementRef is the reference under which Techstack names the server
// (its Agent) in the value deliveries it asks the Wallet for, so that a
// later detach withdraws exactly that server (HostWithdraw). It is a
// one-way digest: the Wallet learns no Techstack identifier. Empty for an
// empty agent id.
func PlacementRef(agentID string) string {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("techstack-agent:" + agentID))
	return "techstack-agent:" + hex.EncodeToString(sum[:16])
}

// HostDeliver has the Wallet seal an item's newest value to the StackKits node
// whose owner-signed recipient statement answers challenge. The item must list
// the node as a host target; the Wallet refuses otherwise
// (`host_target_unknown`). placementRef (PlacementRef of the server's Agent)
// lets a later HostWithdraw find the node; empty records none. Failures are
// Denials.
func (c *Client) HostDeliver(ctx context.Context, owner, itemID string, rotate bool, siteRef, nodeRef, placementRef string, statement []byte, challenge string, expiresAt time.Time) (HostDelivery, error) {
	delivery := walletservice.HostDeliveryItemImport
	if rotate {
		delivery = walletservice.HostDeliveryItemRotate
	}
	return c.hostDeliver(ctx, owner, itemID, delivery, siteRef, nodeRef, placementRef, statement, challenge, expiresAt)
}

// HostDeliverBackupTarget has the Wallet seal and sign a generic secret
// holding S3 backup-target material to the node, under the StackKits operation
// backup-target-import or, with rebind, backup-target-rebind. The same host
// target and placement rules as HostDeliver apply. Failures are Denials.
func (c *Client) HostDeliverBackupTarget(ctx context.Context, owner, itemID string, rebind bool, siteRef, nodeRef, placementRef string, statement []byte, challenge string, expiresAt time.Time) (HostDelivery, error) {
	delivery := walletservice.HostDeliveryBackupTargetImport
	if rebind {
		delivery = walletservice.HostDeliveryBackupTargetRebind
	}
	return c.hostDeliver(ctx, owner, itemID, delivery, siteRef, nodeRef, placementRef, statement, challenge, expiresAt)
}

func (c *Client) hostDeliver(ctx context.Context, owner, itemID, delivery, siteRef, nodeRef, placementRef string, statement []byte, challenge string, expiresAt time.Time) (HostDelivery, error) {
	wallet, err := c.service()
	if err != nil {
		return HostDelivery{}, err
	}
	if !IsItemID(itemID) {
		return HostDelivery{}, ItemInvalid()
	}
	sealed, err := wallet.HostDeliver(ctx, owner, itemID, walletservice.HostDeliveryRequest{
		Delivery: delivery, SiteRef: siteRef, NodeRef: nodeRef, PlacementRef: placementRef,
		Statement: statement, Challenge: challenge, ExpiresAt: expiresAt,
	})
	if err != nil {
		return HostDelivery{}, Denied(err)
	}
	return HostDelivery{Envelope: sealed.Envelope, Version: sealed.Version}, nil
}

// HostWithdrawal is what the Wallet recorded when a detached server was
// withdrawn: how many items lost the node as a host target and how many now
// need rotation because the node held their current value.
type HostWithdrawal struct {
	OperationID    string
	TargetsRemoved int
	NeedsRotation  int
}

// HostWithdraw tells the Wallet that the server whose Agent is agentID is
// detached: for each item Techstack delivered to that server under its
// PlacementRef, the Wallet drops the node from the item's host targets and
// marks the item for rotation when the node holds its current value. Items
// the owner delivered there herself stay untouched (a detached server may keep
// running on its own). It releases no value and is safe to repeat. Failures
// are Denials.
func (c *Client) HostWithdraw(ctx context.Context, owner, agentID string) (HostWithdrawal, error) {
	wallet, err := c.service()
	if err != nil {
		return HostWithdrawal{}, err
	}
	placement := PlacementRef(agentID)
	if placement == "" {
		return HostWithdrawal{}, ItemInvalid()
	}
	withdrawn, err := wallet.HostWithdraw(ctx, owner, placement)
	if err != nil {
		return HostWithdrawal{}, Denied(err)
	}
	out := HostWithdrawal{OperationID: withdrawn.OperationID}
	for _, node := range withdrawn.Nodes {
		for _, item := range node.Items {
			if item.TargetRemoved {
				out.TargetsRemoved++
			}
			if item.NeedsRotation {
				out.NeedsRotation++
			}
		}
	}
	return out, nil
}
