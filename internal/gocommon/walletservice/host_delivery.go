package walletservice

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"time"
)

// Host deliveries of an item's value to one StackKits node.
const (
	HostDeliveryItemImport = "item-import"
	HostDeliveryItemRotate = "item-rotate"

	// MaxHostEnvelopeBytes bounds the sealed envelope a node reads on stdin.
	MaxHostEnvelopeBytes = 64 << 10
	// maxHostLifetime is the Wallet's ceiling for a host delivery.
	maxHostLifetime   = 15 * time.Minute
	maxStatementBytes = 16 << 10
)

var hostRefPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// HostDeliveryRequest seals an item's newest value to one node. Statement is
// the node's owner-signed recipient statement (`stackkit host delivery
// recipient`) answering Challenge; the Wallet verifies it against the item's
// host targets, so the service never chooses the recipient key.
type HostDeliveryRequest struct {
	Delivery  string // HostDeliveryItemImport or HostDeliveryItemRotate
	SiteRef   string
	NodeRef   string
	Statement json.RawMessage
	Challenge string
	ExpiresAt time.Time
}

// HostDelivery is the sealed envelope for the node. It is ciphertext only:
// only the node's recipient key opens it, so the service may carry it.
type HostDelivery struct {
	OperationID  string
	ItemID       string
	Version      string
	Operation    string // wallet-item-import or wallet-item-rotate
	Envelope     []byte
	AuditPending bool
}

// HostDeliver asks the Wallet to seal an item's value to a StackKits node
// (`items/{id}/host-delivery`, needs `use`). The answer is checked to be for
// this item and delivery before the envelope is returned.
func (c *Client) HostDeliver(ctx context.Context, owner, itemID string, in HostDeliveryRequest) (*HostDelivery, error) {
	if err := c.gate(); err != nil {
		return nil, err
	}
	if !eligibleOwner(owner) {
		return nil, errInvalid("owner")
	}
	if !itemIDPattern.MatchString(itemID) {
		return nil, errInvalid("item_id")
	}
	if reason := validateHostDelivery(in, time.Now()); reason != "" {
		return nil, errInvalid(reason)
	}
	body := map[string]any{
		"delivery": in.Delivery, "siteRef": in.SiteRef, "nodeRef": in.NodeRef,
		"statement": in.Statement, "challenge": in.Challenge, "expiresAt": in.ExpiresAt.UnixMilli(),
	}
	raw, status, err := c.post(ctx, owner, "/items/"+itemID+"/host-delivery", body, false)
	if err != nil {
		return nil, err
	}
	delivery, ok := parseHostDelivery(raw, itemID, in.Delivery)
	if status != 200 || !ok {
		return nil, errResponse(status, "host_delivery")
	}
	return delivery, nil
}

func validateHostDelivery(in HostDeliveryRequest, now time.Time) string {
	switch {
	case in.Delivery != HostDeliveryItemImport && in.Delivery != HostDeliveryItemRotate:
		return "delivery"
	case !hostRefPattern.MatchString(in.SiteRef) || !hostRefPattern.MatchString(in.NodeRef):
		return "host_ref"
	case in.Challenge == "" || len(in.Challenge) > 256:
		return "challenge"
	case len(in.Statement) == 0 || len(in.Statement) > maxStatementBytes || !jsonObject(in.Statement, maxStatementBytes):
		return "statement"
	case !in.ExpiresAt.After(now) || in.ExpiresAt.After(now.Add(maxHostLifetime)):
		return "expires_at"
	}
	return ""
}

func parseHostDelivery(raw []byte, itemID, delivery string) (*HostDelivery, bool) {
	var answer struct {
		OperationID  string `json:"operationId"`
		ItemID       string `json:"itemId"`
		Version      string `json:"version"`
		Operation    string `json:"operation"`
		Envelope     string `json:"envelope"`
		AuditPending bool   `json:"auditPending"`
	}
	want := map[string]string{HostDeliveryItemImport: "wallet-item-import", HostDeliveryItemRotate: "wallet-item-rotate"}[delivery]
	if json.Unmarshal(raw, &answer) != nil || answer.ItemID != itemID || answer.Operation != want ||
		answer.OperationID == "" || len(answer.OperationID) > 128 || !decimalPattern.MatchString(answer.Version) ||
		answer.Envelope == "" || len(answer.Envelope) > MaxHostEnvelopeBytes || !json.Valid([]byte(answer.Envelope)) ||
		!bytes.HasPrefix(bytes.TrimSpace([]byte(answer.Envelope)), []byte("{")) {
		return nil, false
	}
	return &HostDelivery{
		OperationID: answer.OperationID, ItemID: itemID, Version: answer.Version, Operation: answer.Operation,
		Envelope: []byte(answer.Envelope), AuditPending: answer.AuditPending,
	}, true
}
