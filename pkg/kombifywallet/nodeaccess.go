package kombifywallet

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"

	"github.com/kombifyio/techstack/internal/gocommon/walletservice"
	"golang.org/x/crypto/ssh"
)

// NodeAccess is a parsed node_access_profile value: how to log in to one
// server over SSH. The value convention is shared with Cloud and the Techstack
// UI: a JSON object {"host","user","privateKey"} or {"host","user","password"},
// optionally with "port" (a number) and "publicKey" (an OpenSSH line).
//
// The two secrets render as a fixed marker in logs and JSON. Wipe empties
// them; a caller that must hand them to an API taking plain strings reveals
// them once, for that one call.
type NodeAccess struct {
	ItemID     string
	Version    string
	Host       string
	User       string
	Port       int
	PrivateKey walletservice.Secret
	Password   walletservice.Secret
	// PublicKey is the profile's OpenSSH public key line when it carries one.
	PublicKey string
}

// Wipe overwrites both secrets.
func (n *NodeAccess) Wipe() {
	if n == nil {
		return
	}
	n.PrivateKey.Wipe()
	n.Password.Wipe()
}

// HasPrivateKey and HasPassword say which authentication the profile offers.
func (n *NodeAccess) HasPrivateKey() bool { return n != nil && n.PrivateKey.Len() > 0 }
func (n *NodeAccess) HasPassword() bool   { return n != nil && n.Password.Len() > 0 }

// RequireHost refuses a profile that is bound to another host than the one the
// caller is about to log in to. A node access item is bound to its node, and
// the check keeps a request from sending the item's key or password to a host
// the owner never named in it. A profile without a host binds nothing.
func (n *NodeAccess) RequireHost(host string) error {
	profileHost := strings.ToLower(strings.TrimSpace(n.Host))
	if profileHost == "" || profileHost == strings.ToLower(strings.TrimSpace(host)) {
		return nil
	}
	return &Denial{
		Status: http.StatusConflict, Reason: ReasonHostMismatch, ItemID: n.ItemID,
		Title:     "That Wallet item belongs to another server",
		Body:      "The node access item you picked is saved for a different host than this server, so Techstack did not use it.",
		NextSteps: []string{"Pick the node access item that was saved for this server."},
	}
}

// RequirePrivateKey and RequirePassword refuse a profile that has no secret
// for the authentication method the caller needs.
func (n *NodeAccess) RequirePrivateKey() error {
	if n.HasPrivateKey() {
		return nil
	}
	return credentialMismatch(n.ItemID, "an SSH key")
}

func (n *NodeAccess) RequirePassword() error {
	if n.HasPassword() {
		return nil
	}
	return credentialMismatch(n.ItemID, "an SSH password")
}

func credentialMismatch(itemID, want string) *Denial {
	return &Denial{
		Status: http.StatusUnprocessableEntity, Reason: ReasonCredentialMismatch, ItemID: itemID,
		Title:     "That Wallet item has no " + strings.TrimPrefix(want, "an "),
		Body:      "The node access item you picked does not contain " + want + ", which is what this connection needs.",
		NextSteps: []string{"Pick a node access item that holds " + want + "."},
	}
}

// PublicKeyLine returns the profile's OpenSSH public key line and its SHA-256
// fingerprint: the stored publicKey when there is one, otherwise the public
// half of the private key. A password-only profile, or a private key that is
// passphrase protected, has none.
func (n *NodeAccess) PublicKeyLine() (line, fingerprint string, err error) {
	if candidate := strings.TrimSpace(n.PublicKey); candidate != "" {
		key, _, _, rest, parseErr := ssh.ParseAuthorizedKey([]byte(candidate))
		if parseErr == nil && len(bytes.TrimSpace(rest)) == 0 {
			return candidate, ssh.FingerprintSHA256(key), nil
		}
	}
	if n.HasPrivateKey() {
		if signer, parseErr := ssh.ParsePrivateKey(n.PrivateKey.Bytes()); parseErr == nil {
			public := signer.PublicKey()
			return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public))), ssh.FingerprintSHA256(public), nil
		}
	}
	return "", "", &Denial{
		Status: http.StatusBadRequest, Reason: ReasonNoPublicKey, ItemID: n.ItemID,
		Title: "That Wallet item has no SSH public key",
		Body:  "Techstack could not read a public SSH key from the item. Pick an SSH key item, or add the public key to it.",
	}
}

// NodeAccess delivers one node_access_profile item for the owner and parses it.
// The Wallet opens the value only for this call and records the disclosure.
// The caller wipes the result.
func (c *Client) NodeAccess(ctx context.Context, owner, itemID string) (*NodeAccess, error) {
	wallet, err := c.service()
	if err != nil {
		return nil, err
	}
	delivered, err := wallet.Deliver(ctx, owner, itemID)
	if err != nil {
		return nil, Denied(err)
	}
	defer delivered.Wipe()
	access, ok := parseNodeAccess(delivered.Bytes())
	if !ok {
		return nil, &Denial{
			Status: http.StatusUnprocessableEntity, Reason: ReasonNotNodeAccess, ItemID: itemID,
			Title:     "That Wallet item is not a node access item",
			Body:      "Techstack expects an item with host, user and an SSH key or password. Pick a node access item.",
			NextSteps: []string{"Pick a node access item from your kombify Wallet."},
		}
	}
	access.ItemID, access.Version = itemID, delivered.Version
	return access, nil
}

// parseNodeAccess reads the profile convention. It tolerates members it does
// not know (Cloud may add fields) but refuses a value with no usable secret.
func parseNodeAccess(value []byte) (*NodeAccess, bool) {
	var raw struct {
		Host       string          `json:"host"`
		User       string          `json:"user"`
		Port       json.RawMessage `json:"port"`
		PrivateKey json.RawMessage `json:"privateKey"`
		Password   json.RawMessage `json:"password"`
		PublicKey  string          `json:"publicKey"`
	}
	if json.Unmarshal(value, &raw) != nil {
		return nil, false
	}
	access := &NodeAccess{Host: strings.TrimSpace(raw.Host), User: strings.TrimSpace(raw.User), PublicKey: strings.TrimSpace(raw.PublicKey)}
	if len(raw.Port) > 0 && string(raw.Port) != "null" {
		var port float64
		if json.Unmarshal(raw.Port, &port) != nil || port != math.Trunc(port) || port < 1 || port > 65535 {
			return nil, false
		}
		access.Port = int(port)
	}
	access.PrivateKey = secretString(raw.PrivateKey)
	access.Password = secretString(raw.Password)
	clear(raw.PrivateKey)
	clear(raw.Password)
	if !access.HasPrivateKey() && !access.HasPassword() {
		return nil, false
	}
	return access, true
}

// secretString decodes a JSON string member into a wipeable secret. An absent,
// null or non-string member is an empty secret.
func secretString(raw json.RawMessage) walletservice.Secret {
	var text string
	if len(raw) == 0 || json.Unmarshal(raw, &text) != nil {
		return walletservice.Secret{}
	}
	return walletservice.NewSecret([]byte(text))
}
