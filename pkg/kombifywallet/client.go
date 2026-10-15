package kombifywallet

import (
	"context"
	"errors"
	"regexp"

	"github.com/kombifyio/techstack/internal/gocommon/walletservice"
)

// itemIDPattern is the Wallet item id grammar. Legacy wallet ids (uuids,
// wallet_<hex>, <stack>:<service>) never match it, which is how a caller tells
// a Wallet item from a legacy row.
var itemIDPattern = regexp.MustCompile(`^itm_[a-z2-7]{26}$`)

// projectIDPattern is the Wallet's project reference grammar.
var projectIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// IsItemID reports whether value is a kombify Wallet item id.
func IsItemID(value string) bool { return itemIDPattern.MatchString(value) }

// ProjectScope is the Wallet scope of a stack (kit deployment): Techstack's
// stack is the item's project. ok is false for a stack id the Wallet's scope
// grammar does not admit.
func ProjectScope(stackID string) (scope string, ok bool) {
	if !projectIDPattern.MatchString(stackID) {
		return "", false
	}
	return "project:" + stackID, true
}

var errNotConfigured = errors.New("kombify wallet client is not configured")

// Client is Techstack's handle on the kombify Wallet. A nil *Client means the
// deployment reads the legacy wallet; every caller branches on that.
type Client struct {
	wallet *walletservice.Client
}

// New wraps a Wallet service client. Production code gets its client from
// Boot; tests build one against a fake Gateway.
func New(wallet *walletservice.Client) *Client { return &Client{wallet: wallet} }

// Enabled reports whether Wallet calls reach the Wallet. It is false for a nil
// Client, which is the legacy source.
func (c *Client) Enabled() bool { return c != nil && c.wallet.Enabled() }

// service returns the underlying client or an unavailable denial. A disabled
// client cannot be built by Boot; this guards a hand-built zero value.
func (c *Client) service() (*walletservice.Client, error) {
	if c == nil || !c.wallet.Enabled() {
		return nil, unavailable(errNotConfigured, "wallet_not_configured", false)
	}
	return c.wallet, nil
}

// CanCreate is the create-right preflight: it asks the Wallet for its capture
// key in the owner's partition, which the Wallet grants only to a service that
// holds the partition's create relation. Nothing is written. A missing right
// returns the wallet_create_right_missing denial.
func (c *Client) CanCreate(ctx context.Context, owner string) error {
	wallet, err := c.service()
	if err != nil {
		return err
	}
	if _, err := wallet.CaptureKey(ctx, owner, ""); err != nil {
		denial := Denied(err)
		if denial.Reason == ReasonRightMissing {
			return CreateRightMissing(denial.cause)
		}
		return denial
	}
	return nil
}
