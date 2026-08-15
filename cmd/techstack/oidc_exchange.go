package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kombifyio/go-common/oidcclient"
)

const (
	techstackAuthCloudClientSecretNext = "TECHSTACK_AUTH_CLOUD_CLIENT_SECRET_NEXT" //nolint:gosec // env var NAME, not a credential value
	maxRotatingOIDCTokenBodyBytes      = 1 << 20
)

type rotatingOIDCCodeExchanger struct {
	client     *http.Client
	nextSecret string
}

func newRotatingOIDCCodeExchanger(client *http.Client, nextSecret string) *rotatingOIDCCodeExchanger {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &rotatingOIDCCodeExchanger{
		client:     client,
		nextSecret: strings.TrimSpace(nextSecret),
	}
}

func (e *rotatingOIDCCodeExchanger) ExchangeCode(ctx context.Context, provider *oidcclient.Provider, request oidcclient.CodeExchangeRequest) (*oidcclient.CodeExchangeResult, error) {
	result, err := e.exchangeCodeWithSecret(ctx, provider, request, providerClientSecret(provider))
	if err == nil {
		return result, nil
	}

	var providerError *rotatingOIDCProviderError
	if provider == nil ||
		provider.Kind() != oidcclient.KindAuth0 ||
		e.nextSecret == "" ||
		subtle.ConstantTimeCompare([]byte(provider.ClientSecret()), []byte(e.nextSecret)) == 1 ||
		!errors.As(err, &providerError) ||
		providerError.code != "invalid_client" {
		return nil, err
	}

	return e.exchangeCodeWithSecret(ctx, provider, request, e.nextSecret)
}

func (e *rotatingOIDCCodeExchanger) exchangeCodeWithSecret(ctx context.Context, provider *oidcclient.Provider, request oidcclient.CodeExchangeRequest, clientSecret string) (*oidcclient.CodeExchangeResult, error) {
	if provider == nil {
		return nil, fmt.Errorf("%w: provider is nil", oidcclient.ErrInvalidProvider)
	}
	if strings.TrimSpace(request.Code) == "" {
		return nil, fmt.Errorf("%w: code is required", oidcclient.ErrTokenExchange)
	}
	if strings.TrimSpace(request.RedirectURI) == "" {
		return nil, fmt.Errorf("%w: redirect_uri is required", oidcclient.ErrTokenExchange)
	}

	values := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {request.Code},
		"redirect_uri": {request.RedirectURI},
	}
	if clientSecret == "" {
		values.Set("client_id", provider.ClientID())
	}
	if request.PKCEVerifier != "" {
		values.Set("code_verifier", request.PKCEVerifier)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.TokenURL(), strings.NewReader(values.Encode()))
	if err != nil {
		return nil, fmt.Errorf("%w: create request: %w", oidcclient.ErrTokenExchange, err)
	}
	httpRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpRequest.Header.Set("Accept", "application/json")
	if clientSecret != "" {
		httpRequest.SetBasicAuth(provider.ClientID(), clientSecret)
	}

	response, err := e.client.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("%w: token request failed: %w", oidcclient.ErrTokenExchange, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxRotatingOIDCTokenBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: read token response: %w", oidcclient.ErrTokenExchange, err)
	}

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var payload struct {
			Code string `json:"error"`
		}
		_ = json.Unmarshal(body, &payload)
		return nil, &rotatingOIDCProviderError{
			statusCode: response.StatusCode,
			code:       strings.TrimSpace(payload.Code),
		}
	}

	result := &oidcclient.CodeExchangeResult{}
	if err := json.Unmarshal(body, result); err != nil {
		return nil, fmt.Errorf("%w: decode response: %v", oidcclient.ErrTokenExchange, err)
	}
	if strings.TrimSpace(result.IDToken) == "" {
		return nil, fmt.Errorf("%w: response missing id_token", oidcclient.ErrTokenExchange)
	}
	return result, nil
}

type rotatingOIDCProviderError struct {
	statusCode int
	code       string
}

func (e *rotatingOIDCProviderError) Error() string {
	return fmt.Sprintf("%s: status %d", oidcclient.ErrTokenExchange, e.statusCode)
}

func (e *rotatingOIDCProviderError) Unwrap() error {
	return oidcclient.ErrTokenExchange
}

func providerClientSecret(provider *oidcclient.Provider) string {
	if provider == nil {
		return ""
	}
	return provider.ClientSecret()
}
