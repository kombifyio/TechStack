package hostidentity

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/guardidentity"
)

// Transport wraps base so every request to the control plane carries a
// key-bound access token and a fresh proof of possession instead of the bearer
// credential. Requests to other origins, the identity endpoints themselves and
// every request while the Client is in ModeLegacy pass through untouched, so
// existing call sites that set "Authorization: Bearer" keep working unchanged.
func (c *Client) Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &identityTransport{client: c, base: base}
}

type identityTransport struct {
	client *Client
	base   http.RoundTripper
}

func (t *identityTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.client.applies(req) {
		return t.base.RoundTrip(req)
	}
	return t.client.authorizedRoundTrip(t.base, req)
}

func (c *Client) applies(req *http.Request) bool {
	if c.Mode() != ModeHostKey || req == nil || req.URL == nil {
		return false
	}
	if strings.ToLower(req.URL.Scheme+"://"+req.URL.Host) != c.origin {
		return false
	}
	return !strings.Contains(req.URL.Path, "/identity/")
}

// authorizedRoundTrip sends req with a DPoP authorization. A token the control
// plane reports as expired (superseded credential, finished key overlap) is
// replaced once and the request replayed when its body can be re-read.
func (c *Client) authorizedRoundTrip(base http.RoundTripper, req *http.Request) (*http.Response, error) {
	if base == nil {
		base = http.DefaultTransport
	}
	ctx := req.Context()
	for attempt := 0; ; attempt++ {
		token, err := c.accessToken(ctx, attempt > 0)
		if err != nil {
			closeRequestBody(req)
			return nil, err
		}
		outgoing := req.Clone(ctx)
		if attempt > 0 && req.GetBody != nil {
			body, bodyErr := req.GetBody()
			if bodyErr != nil {
				return nil, bodyErr
			}
			outgoing.Body = body
		}
		proof, err := guardidentity.SignProof(c.cfg.Manager.Key(), guardidentity.ProofInput{
			Method: req.Method, URL: req.URL.String(), AccessToken: token, Now: c.cfg.Now(),
		})
		if err != nil {
			closeRequestBody(req)
			return nil, err
		}
		outgoing.Header.Set("Authorization", guardidentity.TokenType+" "+token)
		outgoing.Header.Set(guardidentity.ProofHeader, proof)
		response, err := base.RoundTrip(outgoing)
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusForbidden {
			return response, nil
		}
		apiErr := peekAPIError(response)
		switch {
		case apiErr != nil && apiErr.Code == guardidentity.CodeIdentityRevoked:
			_ = c.cfg.Manager.SetState(StateRevoked, guardidentity.CodeIdentityRevoked)
			return response, nil
		case apiErr != nil && apiErr.Code == guardidentity.CodeTokenExpired && attempt == 0 && replayable(req):
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
			_ = response.Body.Close()
			continue
		}
		return response, nil
	}
}

func replayable(req *http.Request) bool {
	return req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
}

func closeRequestBody(req *http.Request) {
	if req.Body != nil {
		_ = req.Body.Close()
	}
}

// peekAPIError reads a bounded error envelope and restores the body so the
// caller still sees the complete response.
func peekAPIError(response *http.Response) *APIError {
	const limit = 8 << 10
	head, _ := io.ReadAll(io.LimitReader(response.Body, limit))
	response.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), response.Body), response.Body}
	apiErr := parseAPIError(response, head, timeNow())
	if apiErr.Code == "" {
		return nil
	}
	return apiErr
}

func timeNow() time.Time { return time.Now().UTC() }
