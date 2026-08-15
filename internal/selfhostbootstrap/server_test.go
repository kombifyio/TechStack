package selfhostbootstrap

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSelfhostDefaultsAndStaticManifest(t *testing.T) {
	t.Setenv("KOMBIFY_EDITION", "")
	t.Setenv("DEPLOYMENT_MODE", "")
	config, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if config.Edition != Edition || config.Mode != DeploymentMode {
		t.Fatalf("unexpected config: %#v", config)
	}
	handler, err := Handler(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/health", "/api/v1/release-manifest"} {
		request := httptest.NewRequest("GET", path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 200 {
			t.Fatalf("%s returned %d", path, response.Code)
		}
		if strings.Contains(strings.ToLower(response.Body.String()), "auth0") {
			t.Fatalf("%s leaked hosted auth", path)
		}
	}
}

func TestSelfhostRejectsIndependentHostedMode(t *testing.T) {
	t.Setenv("KOMBIFY_EDITION", Edition)
	t.Setenv("DEPLOYMENT_MODE", "saas")
	if _, err := FromEnv(); err == nil {
		t.Fatal("expected independently configured hosted mode to fail")
	}
}
