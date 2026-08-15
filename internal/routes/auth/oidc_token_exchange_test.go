package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestExchangeCodeForTokensRetriesNextSecretOnlyAfterInvalidClient(t *testing.T) {
	t.Setenv("TECHSTACK_AUTH_CLOUD_CLIENT_SECRET_NEXT", "next-client-secret")

	var receivedSecrets []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, secret, _ := r.BasicAuth()
		receivedSecrets = append(receivedSecrets, secret)
		w.Header().Set("Content-Type", "application/json")
		if len(receivedSecrets) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_client","error_description":"credentials rejected"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"rotated-access-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer provider.Close()

	request := httptest.NewRequest(http.MethodGet, "https://techstack.example/auth/callback", nil)
	token, err := exchangeCodeForTokens(
		request,
		provider.URL,
		"cloud-client",
		"current-client-secret",
		"authorization-code",
		"https://techstack.example/auth/callback",
		"",
	)
	if err != nil {
		t.Fatalf("exchangeCodeForTokens: %v", err)
	}
	if token.AccessToken != "rotated-access-token" {
		t.Fatalf("access token = %q, want rotated-access-token", token.AccessToken)
	}
	if got, want := receivedSecrets, []string{"current-client-secret", "next-client-secret"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("provider credentials = %#v, want %#v", got, want)
	}
}

func TestExchangeCodeForTokensDoesNotRetryNextSecretAfterOAuthError(t *testing.T) {
	t.Setenv("TECHSTACK_AUTH_CLOUD_CLIENT_SECRET_NEXT", "next-client-secret")

	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"next-client-secret must never be exposed"}`))
	}))
	defer provider.Close()

	request := httptest.NewRequest(http.MethodGet, "https://techstack.example/auth/callback", nil)
	_, err := exchangeCodeForTokens(
		request,
		provider.URL,
		"cloud-client",
		"current-client-secret",
		"authorization-code",
		"https://techstack.example/auth/callback",
		"",
	)
	if err == nil {
		t.Fatal("exchangeCodeForTokens unexpectedly succeeded")
	}
	if requests.Load() != 1 {
		t.Fatalf("provider requests = %d, want 1", requests.Load())
	}
	if message := err.Error(); strings.Contains(message, "current-client-secret") || strings.Contains(message, "next-client-secret") {
		t.Fatalf("token exchange error exposed a client secret: %q", message)
	}
}

func TestExchangeCodeForTokensDoesNotRetryNextSecretAfterNetworkError(t *testing.T) {
	t.Setenv("TECHSTACK_AUTH_CLOUD_CLIENT_SECRET_NEXT", "next-client-secret")

	var requests atomic.Int32
	originalClient := oidcHTTPClient
	oidcHTTPClient = &http.Client{Transport: oidcRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("provider network unavailable")
	})}
	t.Cleanup(func() {
		oidcHTTPClient = originalClient
	})

	request := httptest.NewRequest(http.MethodGet, "https://techstack.example/auth/callback", nil)
	_, err := exchangeCodeForTokens(
		request,
		"https://login.example",
		"cloud-client",
		"current-client-secret",
		"authorization-code",
		"https://techstack.example/auth/callback",
		"",
	)
	if err == nil {
		t.Fatal("exchangeCodeForTokens unexpectedly succeeded")
	}
	if requests.Load() != 1 {
		t.Fatalf("provider requests = %d, want 1", requests.Load())
	}
}

func TestExchangeCodeForTokensPreservesCurrentSecretBehaviorWithoutNext(t *testing.T) {
	t.Setenv("TECHSTACK_AUTH_CLOUD_CLIENT_SECRET_NEXT", "")

	var requests atomic.Int32
	var receivedSecret string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, receivedSecret, _ = r.BasicAuth()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"current-access-token","token_type":"Bearer","expires_in":3600}`))
	}))
	defer provider.Close()

	request := httptest.NewRequest(http.MethodGet, "https://techstack.example/auth/callback", nil)
	token, err := exchangeCodeForTokens(
		request,
		provider.URL,
		"cloud-client",
		"current-client-secret",
		"authorization-code",
		"https://techstack.example/auth/callback",
		"",
	)
	if err != nil {
		t.Fatalf("exchangeCodeForTokens: %v", err)
	}
	if token.AccessToken != "current-access-token" || receivedSecret != "current-client-secret" || requests.Load() != 1 {
		t.Fatalf("token=%#v receivedSecret=%q requests=%d", token, receivedSecret, requests.Load())
	}
}

type oidcRoundTripFunc func(*http.Request) (*http.Response, error)

func (function oidcRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
