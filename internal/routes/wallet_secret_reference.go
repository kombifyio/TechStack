package routes

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/kombifywallet"
)

// WalletSecretReferences resolves the Wallet item a request names to the
// secret it holds, inside Techstack, so a caller (an agent above all) hands
// over a reference and never the secret itself (API-FIRST 13.6). On a
// deployment that reads the kombify Wallet the reference is a Wallet item id
// the owner assigned to Techstack; on one that keeps the built-in wallet it is
// the id of an item the owner saved there. The secret lives for the one call
// that needs it and is never answered or logged.
type WalletSecretReferences struct {
	Kombify *kombifywallet.Client
	Legacy  controlplane.WalletStore
}

// walletSSHCredential is the login a node access reference resolves to. A
// credential from the kombify Wallet is wiped after use; the caller defers
// Wipe in either case.
type walletSSHCredential struct {
	PrivateKey string
	Password   string
	access     *kombifywallet.NodeAccess
}

func (c *walletSSHCredential) Wipe() {
	if c == nil {
		return
	}
	c.access.Wipe()
	c.PrivateKey, c.Password = "", ""
}

// walletReferenceError is a refused reference on a deployment that keeps the
// built-in wallet. It carries no value.
type walletReferenceError struct {
	status int
	reason string
	title  string
}

func (e *walletReferenceError) Error() string { return "wallet reference: " + e.reason }

// writeWalletReferenceDenial answers a request whose Wallet reference was
// refused, with the structured denial of the wallet the deployment reads.
func writeWalletReferenceDenial(e *httpx.Event, err error) error {
	var legacy *walletReferenceError
	if errors.As(err, &legacy) {
		code := ksapi.ErrCodeValidation
		switch legacy.status {
		case http.StatusNotFound:
			code = ksapi.ErrCodeNotFound
		case http.StatusConflict:
			code = ksapi.ErrCodeConflict
		case http.StatusServiceUnavailable:
			code = ksapi.ErrCodeUnavailable
		}
		return httpx.Error(e, legacy.status, code, legacy.title, map[string]any{
			"reason_code": legacy.reason, "retryable": false, "wallet_source": string(kombifywallet.SourceLegacy),
		})
	}
	return kombifywallet.Denied(err).Write(e)
}

// SSH resolves a node access reference for a login to host. With the kombify
// Wallet the item must be a node access profile bound to host (or to no host);
// with the built-in wallet an ssh_key item is the private key and a password
// item the password.
func (r WalletSecretReferences) SSH(ctx context.Context, tenantID, ownerID, itemID, host string) (*walletSSHCredential, error) {
	itemID = strings.TrimSpace(itemID)
	if r.Kombify != nil {
		if !kombifywallet.IsItemID(itemID) {
			return nil, kombifywallet.ItemInvalid()
		}
		access, err := r.Kombify.NodeAccess(ctx, ownerID, itemID)
		if err != nil {
			return nil, err
		}
		if err := access.RequireHost(host); err != nil {
			access.Wipe()
			return nil, err
		}
		credential := &walletSSHCredential{access: access}
		if access.HasPrivateKey() {
			credential.PrivateKey = access.PrivateKey.Reveal()
		} else {
			credential.Password = access.Password.Reveal()
		}
		return credential, nil
	}
	kind, secret, err := r.legacySecret(ctx, tenantID, ownerID, itemID, host, "ssh_key", "password")
	if err != nil {
		return nil, err
	}
	if kind == "ssh_key" {
		return &walletSSHCredential{PrivateKey: secret}, nil
	}
	return &walletSSHCredential{Password: secret}, nil
}

// Token resolves a reference to an access token for target, a URL: a kombify
// Wallet item whose value is the token, or a built-in api_key or oauth_token
// item saved for target's host.
func (r WalletSecretReferences) Token(ctx context.Context, tenantID, ownerID, itemID, target string) (string, error) {
	itemID = strings.TrimSpace(itemID)
	if r.Kombify != nil {
		if !kombifywallet.IsItemID(itemID) {
			return "", kombifywallet.ItemInvalid()
		}
		secret, err := r.Kombify.Reveal(ctx, ownerID, itemID)
		if err != nil {
			return "", err
		}
		defer secret.Wipe()
		token := strings.TrimSpace(secret.Reveal())
		if token == "" {
			return "", &kombifywallet.Denial{Status: http.StatusUnprocessableEntity, Reason: kombifywallet.ReasonItemUnusable, ItemID: itemID,
				Title: "That Wallet item holds no token"}
		}
		return token, nil
	}
	_, secret, err := r.legacySecret(ctx, tenantID, ownerID, itemID, walletTargetHost(target), "api_key", "oauth_token")
	return secret, err
}

// legacySecret opens one built-in wallet item the owner saved, for this call.
// The item must be of one of kinds and saved for host: its url names where it
// may be used, so a request cannot aim a saved login or token at a machine the
// owner never saved it for. An item without a url is not used by reference.
func (r WalletSecretReferences) legacySecret(ctx context.Context, tenantID, ownerID, itemID, host string, kinds ...string) (string, string, error) {
	if r.Legacy == nil {
		return "", "", &walletReferenceError{status: http.StatusServiceUnavailable, reason: kombifywallet.ReasonUnavailable,
			title: "The wallet is not available on this deployment"}
	}
	notFound := &walletReferenceError{status: http.StatusNotFound, reason: kombifywallet.ReasonItemNotFound, title: "Wallet item not found"}
	if itemID == "" || kombifywallet.IsItemID(itemID) {
		return "", "", notFound
	}
	item, err := r.Legacy.GetWalletItem(ctx, tenantID, itemID)
	if err != nil || item == nil || !walletItemOwnedBy(item.Metadata, ownerID) {
		return "", "", notFound
	}
	kind := strings.TrimSpace(firstNonEmptyWallet(walletString(item.Metadata["kind"]), item.ItemType))
	if !slices.Contains(kinds, kind) {
		return "", "", &walletReferenceError{status: http.StatusUnprocessableEntity, reason: kombifywallet.ReasonCredentialMismatch,
			title: "That wallet item is not a " + strings.Join(kinds, " or ") + " item"}
	}
	saved := walletTargetHost(walletString(item.Metadata["url"]))
	if saved == "" || host == "" || !strings.EqualFold(saved, host) {
		return "", "", &walletReferenceError{status: http.StatusConflict, reason: kombifywallet.ReasonHostMismatch,
			title: "That wallet item is not saved for this host; set its URL to the host it belongs to"}
	}
	secret, err := walletRevealField(walletString(item.Metadata["secret"]))
	if err != nil || strings.TrimSpace(secret) == "" {
		return "", "", &walletReferenceError{status: http.StatusUnprocessableEntity, reason: kombifywallet.ReasonItemUnusable,
			title: "That wallet item holds no usable secret"}
	}
	return kind, strings.TrimSpace(secret), nil
}

// walletTargetHost is the host a URL, a host:port or a bare host names, in
// lower case; "" when there is none.
func walletTargetHost(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.Contains(value, "://") {
		value = "ssh://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}
