package walletservice

import (
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// The closed set of failure classes. A caller maps a class to a denial; use
// Error.Reason for the Gateway's own reason code (for example
// `wallet_right_missing`, `value_use_only`, `item_unknown`).
const (
	// CodeUnavailable: the client is disabled or misconfigured (never retryable,
	// no network was touched), or the Gateway, the Wallet Store or the network
	// could not serve the call (Retryable says whether trying again may help).
	CodeUnavailable = "wallet_service_unavailable"
	// CodeDenied: the Gateway refused this service, this owner's assignment or a
	// missing right (HTTP 401 or 403).
	CodeDenied = "wallet_service_denied"
	// CodeRefused: the Wallet refused the request or the item for this
	// operation (unknown, revoked, use-only, wrong type, a reference instead of
	// a value, outside policy, an invalid capture).
	CodeRefused = "wallet_item_refused"
	// CodeTransportInvalid: a sealed result failed verification (another owner,
	// version, operation, audience, key or expiry, a tampered envelope or an
	// untrusted capture key), or a capture could not be sealed.
	CodeTransportInvalid = "wallet_transport_invalid"
	// CodeResponseInvalid: the Wallet answered with something other than the
	// documented shape, or an answer that does not match the request.
	CodeResponseInvalid = "wallet_response_invalid"
	// CodeInvalidRequest: the caller's arguments were refused before any call.
	CodeInvalidRequest = "wallet_request_invalid"
)

// Error is the one error type of the client. It carries a failure class and
// the Gateway's guidance, never a value, a token, a header, a ciphertext or a
// response body. Error() prints only the class, the reason and the status.
type Error struct {
	// Code is the failure class (CodeUnavailable and its siblings).
	Code string
	// Reason is the Gateway's `reason_code` when it sent a well-formed one, or
	// this client's own short reason token (for example `service_secret_missing`,
	// `transport`, `timeout`, `owner`). Empty when there is none.
	Reason string
	// Status is the HTTP status of the Gateway's answer, 0 when none arrived.
	Status int
	// Retryable is the Gateway's `retryable` flag, or true for an outage or a
	// 429. A retry of Create or AddVersion is a new operation: the failed one
	// may or may not have been applied, so list before repeating a write.
	Retryable bool
	// RequiredRight and ItemID are the Gateway's `required_right` and `item_id`
	// when it refused an operation for a missing right.
	RequiredRight string
	ItemID        string
	// Guidance is the Gateway's `user_guidance`, bounded and free of control
	// characters. It is for people; do not parse it.
	Guidance string
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("walletservice: ")
	b.WriteString(e.Code)
	if e.Reason != "" {
		b.WriteString(" (" + e.Reason + ")")
	}
	if e.Status != 0 {
		b.WriteString(" status=" + strconv.Itoa(e.Status))
	}
	return b.String()
}

// Is matches on the failure class, so `errors.Is(err, walletservice.ErrDenied)`
// holds for every error of that class.
func (e *Error) Is(target error) bool {
	other, ok := target.(*Error)
	return ok && other.Code == e.Code
}

// Sentinels for errors.Is, one per failure class.
var (
	ErrUnavailable      = &Error{Code: CodeUnavailable}
	ErrDenied           = &Error{Code: CodeDenied}
	ErrRefused          = &Error{Code: CodeRefused}
	ErrTransportInvalid = &Error{Code: CodeTransportInvalid}
	ErrResponseInvalid  = &Error{Code: CodeResponseInvalid}
	ErrInvalidRequest   = &Error{Code: CodeInvalidRequest}
)

// CodeOf returns the failure class of err, or "" when err is not a Wallet error.
func CodeOf(err error) string {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ""
}

// ReasonOf returns the reason of err (the Gateway's `reason_code` for a
// refusal), or "" when err is not a Wallet error or carries none.
func ReasonOf(err error) string {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Reason
	}
	return ""
}

var reasonPattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

func newError(code string, status int, reason string, retryable bool) *Error {
	if !reasonPattern.MatchString(reason) {
		reason = ""
	}
	return &Error{Code: code, Status: status, Reason: reason, Retryable: retryable}
}

func errTransport() *Error { return &Error{Code: CodeTransportInvalid} }
func errResponse(status int, reason string) *Error {
	return newError(CodeResponseInvalid, status, reason, false)
}
func errInvalid(reason string) *Error { return newError(CodeInvalidRequest, 0, reason, false) }
func errUnavailable(reason string) *Error {
	return newError(CodeUnavailable, 0, reason, false)
}

// boundedGuidance keeps the Gateway's guidance to printable text of at most
// 500 characters, so it is safe to show.
func boundedGuidance(text string) string {
	var b strings.Builder
	count := 0
	for _, r := range text {
		if count == 500 {
			break
		}
		if unicode.IsControl(r) || !unicode.IsPrint(r) {
			continue
		}
		b.WriteRune(r)
		count++
	}
	return strings.TrimSpace(b.String())
}

// refusal maps a non-success Gateway answer to a typed error. Only the status
// and the denial envelope's `reason_code`, `retryable`, `user_guidance`,
// `required_right` and `item_id` are read; nothing else of the body is kept.
func refusal(status int, payload []byte) *Error {
	var envelope struct {
		ReasonCode    string `json:"reason_code"`
		Retryable     bool   `json:"retryable"`
		UserGuidance  string `json:"user_guidance"`
		RequiredRight string `json:"required_right"`
		ItemID        string `json:"item_id"`
	}
	envelopeOK := json.Unmarshal(payload, &envelope) == nil
	reason := ""
	if envelopeOK && reasonPattern.MatchString(envelope.ReasonCode) {
		reason = envelope.ReasonCode
	}
	var out *Error
	switch {
	case status == 401 || status == 403:
		out = newError(CodeDenied, status, reason, false)
	case status == 429:
		out = newError(CodeUnavailable, status, reason, true)
	case status >= 500:
		out = newError(CodeUnavailable, status, reason, (envelopeOK && envelope.Retryable) || reason == "")
	default:
		out = newError(CodeRefused, status, reason, envelopeOK && envelope.Retryable)
	}
	if !envelopeOK {
		return out
	}
	out.Guidance = boundedGuidance(envelope.UserGuidance)
	if rightPattern.MatchString(envelope.RequiredRight) {
		out.RequiredRight = envelope.RequiredRight
	}
	if itemIDPattern.MatchString(envelope.ItemID) {
		out.ItemID = envelope.ItemID
	}
	return out
}

var rightPattern = regexp.MustCompile(`^[a-z_]{1,32}$`)
