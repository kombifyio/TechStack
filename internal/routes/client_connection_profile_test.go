package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/httpx"
)

const clientDiscoveryTestInstanceID = "11111111-2222-4333-8444-555555555555"

func TestClientConnectionProfileLocalRouteMatchesCoreValidatedFixture(t *testing.T) {
	router := httpx.NewRouter()
	RegisterClientConnectionProfileRoute(router, ClientConnectionProfileConfig{
		DeploymentMode: clientDeploymentModeLocal,
		InstanceID:     clientDiscoveryTestInstanceID,
	})
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5260/.well-known/kombify-client", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := rr.Header().Get("Cache-Control"); got != "public, max-age=300" {
		t.Fatalf("Cache-Control = %q", got)
	}

	fixtureJSON, err := os.ReadFile("testdata/client-connection-profile.local.json")
	if err != nil {
		t.Fatalf("read Core-validated fixture: %v", err)
	}
	var actual, fixture map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &actual); err != nil {
		t.Fatalf("decode route response: %v", err)
	}
	if err := json.Unmarshal(fixtureJSON, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	if !reflect.DeepEqual(actual, fixture) {
		t.Fatalf("profile drifted from Core-validated fixture:\nactual=%s\nfixture=%s", rr.Body.String(), fixtureJSON)
	}
	for _, forbidden := range []string{"token", "secret", "password", "credential", "private_key", "api_key"} {
		if strings.Contains(strings.ToLower(rr.Body.String()), `"`+forbidden+`"`) {
			t.Fatalf("profile contains forbidden field %q: %s", forbidden, rr.Body.String())
		}
	}
	if sync, ok := actual["sync"].(map[string]any); !ok || sync["offline_write"] != "disabled" || sync["etag"] != false || sync["tombstones"] != false {
		t.Fatalf("profile advertises unimplemented sync semantics: %#v", actual["sync"])
	}
}

func TestClientConnectionProfileSelfHostedPublishesOnlyConfiguredNativeOIDC(t *testing.T) {
	router := httpx.NewRouter()
	RegisterClientConnectionProfileRoute(router, ClientConnectionProfileConfig{
		DeploymentMode: clientDeploymentModeSelfHosted,
		BaseURL:        "https://techstack.home.example/",
		InstanceID:     clientDiscoveryTestInstanceID,
		OIDCIssuer:     "https://id.home.example/oidc",
		OIDCClientID:   "techstack-native-public",
		OIDCAudience:   "https://techstack.home.example/api",
		OIDCScopes:     []string{"openid", "profile", "offline_access"},
		OIDCFlow:       clientFlowPKCE,
	})
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://untrusted-host/.well-known/kombify-client", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	body := append([]byte(nil), rr.Body.Bytes()...)
	var profile clientConnectionProfile
	if err := json.Unmarshal(body, &profile); err != nil {
		t.Fatal(err)
	}
	if profile.DeploymentMode != clientDeploymentModeSelfHosted || profile.BaseURL != "https://techstack.home.example" {
		t.Fatalf("profile = %#v", profile)
	}
	if profile.OIDC.Issuer != "https://id.home.example/oidc" || profile.OIDC.ClientID != "techstack-native-public" || profile.OIDC.Flow != clientFlowPKCE {
		t.Fatalf("oidc = %#v", profile.OIDC)
	}
	fixtureJSON, err := os.ReadFile("testdata/client-connection-profile.self-hosted.json")
	if err != nil {
		t.Fatal(err)
	}
	var actual, fixture map[string]any
	if err := json.Unmarshal(body, &actual); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(fixtureJSON, &fixture); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, fixture) {
		t.Fatalf("self-hosted profile drifted from Core-validated fixture:\nactual=%s\nfixture=%s", rr.Body.String(), fixtureJSON)
	}
}

func TestClientConnectionProfileFailsClosedForUnsafeOrIncompleteConfiguration(t *testing.T) {
	tests := []struct {
		name       string
		cfg        ClientConnectionProfileConfig
		requestURL string
	}{
		{
			name:       "local plaintext non-loopback",
			cfg:        ClientConnectionProfileConfig{DeploymentMode: clientDeploymentModeLocal, InstanceID: clientDiscoveryTestInstanceID},
			requestURL: "http://192.168.1.50:5260/.well-known/kombify-client",
		},
		{
			name: "self-hosted plaintext",
			cfg: ClientConnectionProfileConfig{
				DeploymentMode: clientDeploymentModeSelfHosted, BaseURL: "http://techstack.home.example", InstanceID: clientDiscoveryTestInstanceID,
				OIDCIssuer: "https://id.home.example", OIDCClientID: "native", OIDCScopes: []string{"openid"}, OIDCFlow: clientFlowPKCE,
			},
			requestURL: "https://ignored/.well-known/kombify-client",
		},
		{
			name: "self-hosted missing native client",
			cfg: ClientConnectionProfileConfig{
				DeploymentMode: clientDeploymentModeSelfHosted, BaseURL: "https://techstack.home.example", InstanceID: clientDiscoveryTestInstanceID,
				OIDCIssuer: "https://id.home.example", OIDCScopes: []string{"openid"}, OIDCFlow: clientFlowPKCE,
			},
			requestURL: "https://ignored/.well-known/kombify-client",
		},
		{
			name:       "cloud belongs to Cloud Gateway",
			cfg:        ClientConnectionProfileConfig{DeploymentMode: "cloud", BaseURL: "https://techstack.kombify.io", InstanceID: clientDiscoveryTestInstanceID},
			requestURL: "https://techstack.kombify.io/.well-known/kombify-client",
		},
		{
			name:       "invalid stable instance identity",
			cfg:        ClientConnectionProfileConfig{DeploymentMode: clientDeploymentModeLocal, InstanceID: "UPPERCASE"},
			requestURL: "http://127.0.0.1:5260/.well-known/kombify-client",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := httpx.NewRouter()
			RegisterClientConnectionProfileRoute(router, test.cfg)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, test.requestURL, nil))
			if rr.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
			}
			var envelope map[string]any
			if err := json.NewDecoder(rr.Body).Decode(&envelope); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if envelope["error_code"] != "client_discovery_unavailable" || envelope["reason_code"] != "client_connection_profile_not_configured" || envelope["retryable"] != false {
				t.Fatalf("envelope = %#v", envelope)
			}
			if rr.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q", rr.Header().Get("Cache-Control"))
			}
		})
	}
}
