package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kombifyio/go-common/oidcclient"
)

func TestRotatingOIDCCodeExchangerRetriesNextSecretAfterInvalidClient(t *testing.T) {
	var receivedSecrets []string
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, secret, _ := r.BasicAuth()
		receivedSecrets = append(receivedSecrets, secret)
		w.Header().Set("Content-Type", "application/json")
		if len(receivedSecrets) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"credentials rejected"}`))
			return
		}
		_, _ = w.Write([]byte(`{"id_token":"rotated-id-token","access_token":"rotated-access-token","token_type":"Bearer"}`))
	}))
	defer providerServer.Close()

	provider, err := oidcclient.NewProvider(oidcclient.ProviderConfig{
		ID:           "primary",
		Kind:         oidcclient.KindAuth0,
		Issuer:       providerServer.URL,
		ClientID:     "cloud-client",
		ClientSecret: "current-client-secret",
		TokenURL:     providerServer.URL + "/oauth/token",
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}

	exchanger := newRotatingOIDCCodeExchanger(providerServer.Client(), "next-client-secret")
	result, err := exchanger.ExchangeCode(context.Background(), provider, oidcclient.CodeExchangeRequest{
		Code:        "authorization-code",
		RedirectURI: "https://techstack.example/api/v2/auth/callback",
	})
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if result.IDToken != "rotated-id-token" {
		t.Fatalf("id token = %q, want rotated-id-token", result.IDToken)
	}
	if got, want := receivedSecrets, []string{"current-client-secret", "next-client-secret"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("provider credentials = %#v, want %#v", got, want)
	}
}

func TestRotatingOIDCCodeExchangerDoesNotRetryAfterOtherOAuthError(t *testing.T) {
	var requests atomic.Int32
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"next-client-secret must never be exposed"}`))
	}))
	defer providerServer.Close()

	provider := newRotatingOIDCTestProvider(t, providerServer.URL, "current-client-secret")
	exchanger := newRotatingOIDCCodeExchanger(providerServer.Client(), "next-client-secret")
	_, err := exchanger.ExchangeCode(context.Background(), provider, oidcclient.CodeExchangeRequest{
		Code:        "authorization-code",
		RedirectURI: "https://techstack.example/api/v2/auth/callback",
	})
	if err == nil {
		t.Fatal("ExchangeCode unexpectedly succeeded")
	}
	if requests.Load() != 1 {
		t.Fatalf("provider requests = %d, want 1", requests.Load())
	}
	if message := err.Error(); strings.Contains(message, "current-client-secret") || strings.Contains(message, "next-client-secret") {
		t.Fatalf("token exchange error exposed a client secret: %q", message)
	}
}

func TestRotatingOIDCCodeExchangerDoesNotRetryAfterNetworkError(t *testing.T) {
	var requests atomic.Int32
	client := &http.Client{Transport: rotatingOIDCRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("provider network unavailable")
	})}
	provider := newRotatingOIDCTestProvider(t, "https://login.example", "current-client-secret")
	exchanger := newRotatingOIDCCodeExchanger(client, "next-client-secret")

	_, err := exchanger.ExchangeCode(context.Background(), provider, oidcclient.CodeExchangeRequest{
		Code:        "authorization-code",
		RedirectURI: "https://techstack.example/api/v2/auth/callback",
	})
	if err == nil {
		t.Fatal("ExchangeCode unexpectedly succeeded")
	}
	if requests.Load() != 1 {
		t.Fatalf("provider requests = %d, want 1", requests.Load())
	}
}

func TestRotatingOIDCCodeExchangerPreservesCurrentSecretWithoutNext(t *testing.T) {
	var requests atomic.Int32
	var receivedSecret string
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, receivedSecret, _ = r.BasicAuth()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id_token":"current-id-token","token_type":"Bearer"}`))
	}))
	defer providerServer.Close()

	provider := newRotatingOIDCTestProvider(t, providerServer.URL, "current-client-secret")
	exchanger := newRotatingOIDCCodeExchanger(providerServer.Client(), "")
	result, err := exchanger.ExchangeCode(context.Background(), provider, oidcclient.CodeExchangeRequest{
		Code:        "authorization-code",
		RedirectURI: "https://techstack.example/api/v2/auth/callback",
	})
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if result.IDToken != "current-id-token" || receivedSecret != "current-client-secret" || requests.Load() != 1 {
		t.Fatalf("result=%#v receivedSecret=%q requests=%d", result, receivedSecret, requests.Load())
	}
}

func newRotatingOIDCTestProvider(t *testing.T, issuer, clientSecret string) *oidcclient.Provider {
	t.Helper()
	provider, err := oidcclient.NewProvider(oidcclient.ProviderConfig{
		ID:           "primary",
		Kind:         oidcclient.KindAuth0,
		Issuer:       issuer,
		ClientID:     "cloud-client",
		ClientSecret: clientSecret,
		TokenURL:     strings.TrimRight(issuer, "/") + "/oauth/token",
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	return provider
}

type rotatingOIDCRoundTripFunc func(*http.Request) (*http.Response, error)

func (function rotatingOIDCRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
