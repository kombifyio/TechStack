package kombifywallet

import (
	"errors"
	"net/http"
	"regexp"

	"github.com/kombifyio/techstack/internal/gocommon/walletservice"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// Reason codes of the structured denials this package produces. They are part
// of the API contract the Techstack UI branches on.
const (
	// ReasonItemRequired: kombify mode needs ssh_wallet_item_id; a label or a
	// plaintext password is refused.
	ReasonItemRequired = "wallet_item_required"
	// ReasonItemInvalid: the Wallet item id is not an item id.
	ReasonItemInvalid = "wallet_item_invalid"
	// ReasonItemUnsupported: a Wallet item id was sent to a deployment that
	// still reads the legacy wallet.
	ReasonItemUnsupported = "wallet_item_unsupported"
	// ReasonRightMissing: the owner has not assigned the right Techstack needs.
	ReasonRightMissing = "wallet_right_missing"
	// ReasonCreateRightMissing: Techstack may not save credentials in the
	// owner's Wallet yet.
	ReasonCreateRightMissing = "wallet_create_right_missing"
	// ReasonItemNotFound: the item does not exist, was revoked or was deleted.
	ReasonItemNotFound = "wallet_item_not_found"
	// ReasonItemUnusable: the Wallet refuses to hand this item to a service
	// (use-only value, reference, wrong type).
	ReasonItemUnusable = "wallet_item_unusable"
	// ReasonNotNodeAccess: the item's value is not a node-access profile.
	ReasonNotNodeAccess = "wallet_item_not_node_access"
	// ReasonCredentialMismatch: the profile has no secret for the requested
	// authentication method.
	ReasonCredentialMismatch = "wallet_item_credential_mismatch"
	// ReasonHostMismatch: the profile is bound to another host.
	ReasonHostMismatch = "wallet_item_host_mismatch"
	// ReasonNoPublicKey: no public SSH key can be read from the item.
	ReasonNoPublicKey = "wallet_item_no_public_key"
	// ReasonUnavailable: the Wallet could not serve the call.
	ReasonUnavailable = "wallet_unavailable"
	// ReasonResponseInvalid: the Wallet answered with something Techstack
	// refuses to trust.
	ReasonResponseInvalid = "wallet_response_invalid"
	// ReasonSelfHostedOnly: the operation exists only for the legacy wallet.
	ReasonSelfHostedOnly = "wallet_self_hosted_only"
	// ReasonOwnerIneligible: the caller is not a person the Wallet keeps items for.
	ReasonOwnerIneligible = "wallet_owner_ineligible"
	// ReasonRequestInvalid: the request was refused before it reached the Wallet.
	ReasonRequestInvalid = "wallet_request_invalid"
)

// Denial is a Wallet failure, or a refused Wallet request, in the shape of the
// platform's structured denial: reason_code, retryable and user_guidance. It
// carries no value, token or response body.
type Denial struct {
	Status    int
	Reason    string
	Retryable bool
	// RequiredRight and ItemID are the Wallet's own words for a missing right.
	RequiredRight string
	ItemID        string
	// GatewayReason is the Wallet Gateway's reason_code, kept for diagnosis.
	GatewayReason string
	Title         string
	Body          string
	NextSteps     []string
	cause         error
}

func (d *Denial) Error() string {
	text := "kombify wallet: " + d.Reason
	if d.GatewayReason != "" && d.GatewayReason != d.Reason {
		text += " (" + d.GatewayReason + ")"
	}
	return text
}

// Unwrap exposes the underlying walletservice error for errors.Is/As.
func (d *Denial) Unwrap() error { return d.cause }

// Details is the API error details object.
func (d *Denial) Details() map[string]any {
	guidance := map[string]any{"title": d.Title, "body": d.Body}
	if len(d.NextSteps) > 0 {
		guidance["next_steps"] = d.NextSteps
	}
	details := map[string]any{
		"reason_code":   d.Reason,
		"retryable":     d.Retryable,
		"user_guidance": guidance,
		"wallet_source": string(SourceKombify),
	}
	if d.RequiredRight != "" {
		details["required_right"] = d.RequiredRight
	}
	if d.ItemID != "" {
		details["item_id"] = d.ItemID
	}
	if d.GatewayReason != "" {
		details["wallet_reason"] = d.GatewayReason
	}
	return details
}

// Write answers the request with the denial.
func (d *Denial) Write(e *httpx.Event) error {
	return httpx.Error(e, d.Status, errorCodeFor(d.Status), d.Title, d.Details())
}

func errorCodeFor(status int) ksapi.ErrorCode {
	switch status {
	case http.StatusBadRequest:
		return ksapi.ErrCodeBadRequest
	case http.StatusForbidden:
		return ksapi.ErrCodeForbidden
	case http.StatusNotFound:
		return ksapi.ErrCodeNotFound
	case http.StatusConflict:
		return ksapi.ErrCodeConflict
	case http.StatusUnprocessableEntity:
		return ksapi.ErrCodeValidation
	default:
		return ksapi.ErrCodeUnavailable
	}
}

// AsDenial finds a Denial in err.
func AsDenial(err error) (*Denial, bool) {
	var denial *Denial
	if errors.As(err, &denial) {
		return denial, true
	}
	return nil, false
}

// Denied is the denial for any error from this package, or a generic
// unavailable denial for an error from elsewhere, so a handler always has a
// structured answer.
func Denied(err error) *Denial {
	if denial, ok := AsDenial(err); ok {
		return denial
	}
	return classify(err)
}

var safeReason = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// classify maps a walletservice failure onto a denial. It reads only the class,
// the Gateway's reason code and its right/item hints.
func classify(err error) *Denial {
	var failure *walletservice.Error
	if !errors.As(err, &failure) {
		return unavailable(err, "", false)
	}
	reason := failure.Reason
	if !safeReason.MatchString(reason) {
		reason = ""
	}
	switch failure.Code {
	case walletservice.CodeDenied:
		return classifyDenied(failure, reason)
	case walletservice.CodeRefused:
		return classifyRefused(failure, reason)
	case walletservice.CodeTransportInvalid, walletservice.CodeResponseInvalid:
		return &Denial{
			Status: http.StatusBadGateway, Reason: ReasonResponseInvalid, GatewayReason: reason,
			Title: "The kombify Wallet answered unexpectedly",
			Body:  "Techstack did not trust the Wallet's answer, so nothing was read or saved. This is a platform problem, not something to fix in your Wallet.",
			cause: err,
		}
	case walletservice.CodeInvalidRequest:
		return classifyInvalid(failure, reason)
	default:
		return unavailable(err, reason, failure.Retryable)
	}
}

func classifyInvalid(failure *walletservice.Error, reason string) *Denial {
	switch reason {
	case "item_id":
		denial := ItemInvalid()
		denial.cause = failure
		return denial
	case "owner":
		return &Denial{
			Status: http.StatusForbidden, Reason: ReasonOwnerIneligible, GatewayReason: reason,
			Title: "The kombify Wallet keeps items for people only",
			Body:  "This account is not a person with a kombify Wallet, so Techstack cannot read or save Wallet items for it.",
			cause: failure,
		}
	default:
		return &Denial{
			Status: http.StatusBadRequest, Reason: ReasonRequestInvalid, GatewayReason: reason,
			Title: "The kombify Wallet request was refused",
			Body:  "Techstack refused to send this request to the Wallet.",
			cause: failure,
		}
	}
}

func classifyDenied(failure *walletservice.Error, reason string) *Denial {
	if reason != ReasonRightMissing && failure.RequiredRight == "" {
		// Not admitted, wrong secret, replayed token: a platform configuration
		// problem that the owner cannot repair in the Wallet.
		denial := unavailable(failure, reason, false)
		denial.Body = "Techstack is not admitted to the kombify Wallet right now. Contact support if this persists."
		return denial
	}
	if failure.RequiredRight == "create" && failure.ItemID == "" {
		return CreateRightMissing(failure)
	}
	return &Denial{
		Status: http.StatusForbidden, Reason: ReasonRightMissing, RequiredRight: failure.RequiredRight, ItemID: failure.ItemID,
		GatewayReason: reason,
		Title:         "Allow Techstack to use this Wallet item",
		Body:          "You have not assigned this item to Techstack yet. Techstack only uses items you share with it, and only for the server you pick.",
		NextSteps:     []string{"Open your kombify Wallet, share the item with Techstack, then try again."},
		cause:         failure,
	}
}

func classifyRefused(failure *walletservice.Error, reason string) *Denial {
	switch reason {
	case "item_unknown", "item_denied":
		return &Denial{
			Status: http.StatusNotFound, Reason: ReasonItemNotFound, GatewayReason: reason, ItemID: failure.ItemID,
			Title:     "That Wallet item was not found",
			Body:      "The item does not exist in your Wallet, or it was revoked or deleted.",
			NextSteps: []string{"Pick another item from your kombify Wallet."},
			cause:     failure,
		}
	case "value_use_only", "value_is_reference", "item_action_unsupported":
		return &Denial{
			Status: http.StatusConflict, Reason: ReasonItemUnusable, GatewayReason: reason, ItemID: failure.ItemID,
			Title:     "Techstack cannot read that Wallet item",
			Body:      "This item's value never leaves the Wallet or refers to another item, so Techstack cannot use it to connect to a server.",
			NextSteps: []string{"Pick a node access item whose value Techstack may read."},
			cause:     failure,
		}
	}
	if failure.Retryable {
		return unavailable(failure, reason, true)
	}
	return &Denial{
		Status: http.StatusConflict, Reason: ReasonItemUnusable, GatewayReason: reason, ItemID: failure.ItemID,
		Title: "The kombify Wallet refused the request",
		Body:  "The Wallet did not accept this request for this item.",
		cause: failure,
	}
}

func unavailable(cause error, gatewayReason string, retryable bool) *Denial {
	return &Denial{
		Status: http.StatusServiceUnavailable, Reason: ReasonUnavailable, Retryable: retryable, GatewayReason: gatewayReason,
		Title: "The kombify Wallet is unavailable",
		Body:  "Techstack could not reach your kombify Wallet, so no credential was read or saved. Nothing was done with a stored copy.",
		cause: cause,
	}
}

// CreateRightMissing is the denial for a Techstack that may not yet save
// credentials in the owner's Wallet.
func CreateRightMissing(cause error) *Denial {
	return &Denial{
		Status: http.StatusConflict, Reason: ReasonCreateRightMissing, RequiredRight: "create",
		Title:     "Allow Techstack to save credentials in your kombify Wallet",
		Body:      "Techstack keeps the credentials a rollout produces, such as recovery material, in your kombify Wallet. It may not save to your Wallet yet, so the rollout did not start.",
		NextSteps: []string{"Allow Techstack to save credentials in your kombify Wallet, then start the rollout again."},
		cause:     cause,
	}
}

// ItemRequired is the denial for a kombify-mode request that names a label or
// a plaintext secret, or no Wallet item at all.
func ItemRequired() *Denial {
	return &Denial{
		Status: http.StatusBadRequest, Reason: ReasonItemRequired,
		Title:     "Pick a kombify Wallet item",
		Body:      "This deployment reads SSH credentials from your kombify Wallet. Pick a node access item (ssh_wallet_item_id); labels and passwords typed here are not accepted.",
		NextSteps: []string{"Save the SSH key or password as a node access item in your kombify Wallet and pick it."},
	}
}

// ItemInvalid is the denial for a value that is not a Wallet item id.
func ItemInvalid() *Denial {
	return &Denial{
		Status: http.StatusBadRequest, Reason: ReasonItemInvalid,
		Title: "That is not a kombify Wallet item",
		Body:  "ssh_wallet_item_id must be the id of an item in your kombify Wallet.",
	}
}

// ItemUnsupported is the denial for a Wallet item id sent to a deployment that
// still reads the legacy wallet.
func ItemUnsupported() *Denial {
	return &Denial{
		Status: http.StatusBadRequest, Reason: ReasonItemUnsupported,
		Title: "This deployment does not read the kombify Wallet",
		Body:  "ssh_wallet_item_id is accepted only by deployments that use the kombify Wallet. Use ssh_key_label with this deployment.",
	}
}

// SelfHostedOnly is the denial for an operation that exists only for the
// legacy wallet.
func SelfHostedOnly(title, body string) *Denial {
	return &Denial{
		Status: http.StatusConflict, Reason: ReasonSelfHostedOnly,
		Title: title, Body: body,
	}
}
