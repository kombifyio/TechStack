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
	// HostDeliveryBackupTargetImport and HostDeliveryBackupTargetRebind deliver
	// a generic secret holding S3 backup-target material (the object `stackkit
	// backup target import --sealed` reads) under the node operations
	// backup-target-import and backup-target-rebind.
	HostDeliveryBackupTargetImport = "backup-target-import"
	HostDeliveryBackupTargetRebind = "backup-target-rebind"
	// HostDeliveryItemRemove releases nothing: the node (`stackkit wallet-item
	// remove`) drops every version of the item from its local custody. The
	// Wallet admits it for a revoked item too.
	HostDeliveryItemRemove = "item-remove"

	// MaxHostEnvelopeBytes bounds the sealed envelope a node reads on stdin.
	MaxHostEnvelopeBytes = 64 << 10
	// maxHostLifetime is the Wallet's ceiling for a host delivery.
	maxHostLifetime   = 15 * time.Minute
	maxStatementBytes = 16 << 10
)

var (
	hostRefPattern   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	placementPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
)

// hostOperations maps each delivery to the node operation its envelope names.
var hostOperations = map[string]string{
	HostDeliveryItemImport:         "wallet-item-import",
	HostDeliveryItemRotate:         "wallet-item-rotate",
	HostDeliveryBackupTargetImport: "backup-target-import",
	HostDeliveryBackupTargetRebind: "backup-target-rebind",
	HostDeliveryItemRemove:         "wallet-item-remove",
}

// HostDeliveryRequest seals an item's newest value to one node. Statement is
// the node's owner-signed recipient statement (`stackkit host delivery
// recipient`) answering Challenge; the Wallet verifies it against the item's
// host targets, so the service never chooses the recipient key.
type HostDeliveryRequest struct {
	Delivery  string // HostDeliveryItemImport, HostDeliveryItemRotate, HostDeliveryBackupTargetImport, HostDeliveryBackupTargetRebind or HostDeliveryItemRemove
	SiteRef   string
	NodeRef   string
	Statement json.RawMessage
	Challenge string
	ExpiresAt time.Time
	// PlacementRef optionally names the server in the service's own terms
	// (for example its server id). The Wallet keeps it with a value delivery
	// so HostWithdraw can later withdraw that server by it. Not sent with
	// HostDeliveryItemRemove.
	PlacementRef string
}

// HostDelivery is the sealed envelope for the node. It is ciphertext only:
// only the node's recipient key opens it, so the service may carry it.
type HostDelivery struct {
	OperationID  string
	ItemID       string
	Version      string
	Operation    string // the node operation: wallet-item-import, wallet-item-rotate, wallet-item-remove, backup-target-import or backup-target-rebind
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
	if in.PlacementRef != "" {
		body["placementRef"] = in.PlacementRef
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
	case hostOperations[in.Delivery] == "":
		return "delivery"
	case !hostRefPattern.MatchString(in.SiteRef) || !hostRefPattern.MatchString(in.NodeRef):
		return "host_ref"
	case in.Challenge == "" || len(in.Challenge) > 256:
		return "challenge"
	case len(in.Statement) == 0 || len(in.Statement) > maxStatementBytes || !jsonObject(in.Statement, maxStatementBytes):
		return "statement"
	case !in.ExpiresAt.After(now) || in.ExpiresAt.After(now.Add(maxHostLifetime)):
		return "expires_at"
	case in.PlacementRef != "" && (in.Delivery == HostDeliveryItemRemove || !placementPattern.MatchString(in.PlacementRef)):
		return "placement_ref"
	}
	return ""
}

// HostWithdrawal is what the Wallet recorded when a server was withdrawn:
// each node the placement named and, per item, whether the node was taken off
// its host targets and whether the item now needs rotation because the node
// had received its current value.
type HostWithdrawal struct {
	OperationID string
	Nodes       []WithdrawnNode
}

// WithdrawnNode is one node of a withdrawal.
type WithdrawnNode struct {
	SiteRef string
	NodeRef string
	Items   []WithdrawnItem
}

// WithdrawnItem is one item a withdrawal changed.
type WithdrawnItem struct {
	ItemID        string
	TargetRemoved bool
	NeedsRotation bool
}

// HostWithdraw tells the Wallet that the server this service named
// placementRef in its own value deliveries is withdrawn (`host-withdraw`):
// for each item this service delivered to that node under the placement, the
// Wallet takes the node off the item's host targets and marks the item for
// rotation when the node holds its current value. Items the owner or another
// service delivered there are untouched, and the answer names only this
// service's items. It releases no value and needs no item right; an unknown
// placement withdraws nothing. Repeating it is harmless.
func (c *Client) HostWithdraw(ctx context.Context, owner, placementRef string) (*HostWithdrawal, error) {
	if err := c.gate(); err != nil {
		return nil, err
	}
	if !eligibleOwner(owner) {
		return nil, errInvalid("owner")
	}
	if !placementPattern.MatchString(placementRef) {
		return nil, errInvalid("placement_ref")
	}
	raw, status, err := c.post(ctx, owner, "/host-withdraw", map[string]any{"placementRef": placementRef}, false)
	if err != nil {
		return nil, err
	}
	var answer struct {
		OperationID string `json:"operationId"`
		Withdrawn   []struct {
			SiteRef string `json:"siteRef"`
			NodeRef string `json:"nodeRef"`
			Items   []struct {
				ItemID        string `json:"itemId"`
				TargetRemoved bool   `json:"targetRemoved"`
				NeedsRotation bool   `json:"needsRotation"`
			} `json:"items"`
		} `json:"withdrawn"`
	}
	if status != 200 || json.Unmarshal(raw, &answer) != nil || answer.OperationID == "" || len(answer.OperationID) > 128 || answer.Withdrawn == nil {
		return nil, errResponse(status, "host_withdraw")
	}
	withdrawal := &HostWithdrawal{OperationID: answer.OperationID, Nodes: []WithdrawnNode{}}
	for _, node := range answer.Withdrawn {
		if !hostRefPattern.MatchString(node.SiteRef) || !hostRefPattern.MatchString(node.NodeRef) {
			return nil, errResponse(status, "host_withdraw")
		}
		withdrawn := WithdrawnNode{SiteRef: node.SiteRef, NodeRef: node.NodeRef, Items: []WithdrawnItem{}}
		for _, item := range node.Items {
			if !itemIDPattern.MatchString(item.ItemID) {
				return nil, errResponse(status, "host_withdraw")
			}
			withdrawn.Items = append(withdrawn.Items, WithdrawnItem{ItemID: item.ItemID, TargetRemoved: item.TargetRemoved, NeedsRotation: item.NeedsRotation})
		}
		withdrawal.Nodes = append(withdrawal.Nodes, withdrawn)
	}
	return withdrawal, nil
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
	want := hostOperations[delivery]
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
