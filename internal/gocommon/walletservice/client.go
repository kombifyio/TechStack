package walletservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
)

const (
	pathPrefix = "/v1/wallet/service"
	// walletTarget makes the service token's audience `kombify-wallet`.
	walletTarget = "wallet"
	// tokenTTL is the Wallet edge's ceiling for a product-service token.
	tokenTTL = 60 * time.Second
	// deliverLifetime is how long a sealed result may be opened; the store
	// admits up to five minutes and a use needs one round trip.
	deliverLifetime = 60 * time.Second
	captureLifetime = 120 * time.Second

	maxRequestBytes        = 200_000
	maxBrokerRequestBytes  = 1_500_000
	maxResponseBytes       = 262_144
	maxBrokerResponseBytes = 1_700_000
)

var (
	itemIDPattern      = regexp.MustCompile(`^itm_[a-z2-7]{26}$`)
	ownerPattern       = regexp.MustCompile(`^[A-Za-z0-9._:|@/-]{1,256}$`)
	operationIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	scopePattern       = regexp.MustCompile(`^(?:account|project:[A-Za-z0-9._-]{1,64}|environment:[A-Za-z0-9._-]{1,64}/[A-Za-z0-9._-]{1,64})$`)
)

// eligibleOwner mirrors the Gateway: an Auth0 subject in the partition grammar,
// never a service, anonymous or machine-to-machine subject.
func eligibleOwner(owner string) bool {
	return ownerPattern.MatchString(owner) && !strings.HasPrefix(owner, "service:") &&
		!strings.HasPrefix(owner, "anonymous:") && !strings.HasSuffix(owner, "@clients")
}

// post sends one service-authenticated JSON request for the owner and returns
// the body of a 200 or 201 answer with its status. Every other answer becomes
// an *Error. The token is the product's own 60-second token for the Wallet
// audience, speaking for the owner the body names; nothing else of identity is
// sent.
func (c *Client) post(ctx context.Context, owner, route string, fields map[string]any, brokered bool) ([]byte, int, error) {
	body := make(map[string]any, len(fields)+1)
	for name, value := range fields {
		body[name] = value
	}
	body["owner"] = owner
	payload, err := json.Marshal(body)
	requestLimit, responseLimit, timeout := maxRequestBytes, int64(maxResponseBytes), c.timeout
	if brokered {
		requestLimit, responseLimit, timeout = maxBrokerRequestBytes, maxBrokerResponseBytes, c.brokerTimeout
	}
	if err != nil || len(payload) > requestLimit {
		return nil, 0, errInvalid("request_too_large")
	}
	token, err := servicecall.IssueToken(servicecall.Config{
		ServiceName: c.serviceID, Secret: c.secret, TokenTTL: tokenTTL,
	}, walletTarget, &servicecall.OnBehalfOf{Sub: owner}, uuid.NewString())
	if err != nil {
		return nil, 0, errUnavailable("service_token")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.origin+pathPrefix+route, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, errUnavailable("request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set(servicecall.HeaderServiceAuth, token)
	response, err := c.http.Do(request)
	if err != nil {
		// Never wrap err: a transport error can echo the request URL.
		return nil, 0, transportFailure(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil {
		return nil, 0, newError(CodeUnavailable, response.StatusCode, "response", true)
	}
	if int64(len(raw)) > responseLimit {
		return nil, 0, errResponse(response.StatusCode, "too_large")
	}
	if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusCreated {
		return raw, response.StatusCode, nil
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil, 0, errResponse(response.StatusCode, "status")
	}
	return nil, 0, refusal(response.StatusCode, raw)
}

// transportFailure names a failed round trip without echoing the request.
func transportFailure(ctx context.Context, err error) *Error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return newError(CodeUnavailable, 0, "timeout", true)
		}
		return newError(CodeUnavailable, 0, "canceled", false)
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return newError(CodeUnavailable, 0, "timeout", true)
	}
	return newError(CodeUnavailable, 0, "transport", true)
}

// displayText mirrors the Wallet's display grammar: 1 to maximum characters, no
// leading or trailing space, no control characters (a description may hold
// line feeds).
func displayText(value string, maximum int, multiline bool) bool {
	if value == "" || value != strings.TrimSpace(value) || len([]rune(value)) > maximum {
		return false
	}
	for _, r := range value {
		if r == '\n' && multiline {
			continue
		}
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || unicode.Is(unicode.Cs, r) {
			return false
		}
	}
	return true
}
