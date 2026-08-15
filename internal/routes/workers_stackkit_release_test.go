package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/pkg/httpx"
)

func TestAgentStackKitReleaseServesAuthenticatedExactBundle(t *testing.T) {
	handler, _ := newAgentBinaryTestHandler(t, "unused", "amd64")
	artifact, err := handler.binaryArtifact()
	if err != nil {
		t.Fatalf("resolve artifact: %v", err)
	}
	handler.stackKitReleaseArtifact = func() (agentBinaryArtifact, error) { return artifact, nil }
	router := httpx.NewRouter()
	router.POST("/api/v1/agent/stackkit-release/{os}/{arch}", handler.agentStackKitRelease)
	recorder := performAgentBinaryTestRequest(t, router, "/api/v1/agent/stackkit-release/linux/amd64")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get(agentBinaryChecksumHeader); len(got) != 64 {
		t.Fatalf("artifact checksum = %q", got)
	}
	if recorder.Body.String() != "unused" {
		t.Fatalf("bundle body = %q", recorder.Body.String())
	}
}

func TestAgentStackKitReleaseReturnsNotModifiedForExactBundle(t *testing.T) {
	handler, _ := newAgentBinaryTestHandler(t, "unused", "amd64")
	artifact, err := handler.binaryArtifact()
	if err != nil {
		t.Fatalf("resolve artifact: %v", err)
	}
	handler.stackKitReleaseArtifact = func() (agentBinaryArtifact, error) { return artifact, nil }
	router := httpx.NewRouter()
	router.POST("/api/v1/agent/stackkit-release/{os}/{arch}", handler.agentStackKitRelease)

	for attempt := 0; attempt < 3; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/stackkit-release/linux/amd64", nil)
		request.Header.Set("Authorization", "Bearer "+agentBinaryTestCredential())
		request.Header.Set("If-None-Match", `"`+artifact.sha256+`"`)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotModified {
			t.Fatalf("attempt %d status = %d, want 304; body=%s", attempt, recorder.Code, recorder.Body.String())
		}
		if recorder.Body.Len() != 0 {
			t.Fatalf("attempt %d body length = %d, want 0", attempt, recorder.Body.Len())
		}
	}
	// Metadata-only convergence checks must not consume the bounded archive
	// download allowance for a later real release change.
	if recorder := performAgentBinaryTestRequest(t, router, "/api/v1/agent/stackkit-release/linux/amd64"); recorder.Code != http.StatusOK {
		t.Fatalf("release download after 304 checks = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
}
