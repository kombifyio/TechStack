package stacks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/kombifyio/techstack/internal/onboarding"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
)

const ownerFactsTestSecret = "owner-facts-servicecall-test-secret"

func ownerFactsTestEvent(t *testing.T, token, ifNoneMatch string) (*httpx.Event, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/onboarding/facts/techstack", nil)
	req.SetPathValue("authority", ownerFactsAuthority)
	req.Header.Set(servicecall.HeaderServiceAuth, token)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

func ownerFactsCloudToken(t *testing.T, obo *servicecall.OnBehalfOf) string {
	t.Helper()
	token, err := servicecall.IssueToken(
		servicecall.Config{ServiceName: ownerFactsCallerCloud, Secret: ownerFactsTestSecret, TokenTTL: time.Minute},
		ownerFactsAuthority,
		obo,
		"owner-facts-request",
	)
	if err != nil {
		t.Fatalf("issue service token: %v", err)
	}
	return token
}

func TestOwnerFactsReadIsPrincipalScopedAndConditionallyCacheable(t *testing.T) {
	ctx := context.Background()
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: "stack-other", TenantID: onboardingTestTenant, OwnerSubjectID: "auth0|other", Name: "theirs", Mode: "cloud", Status: "running",
	}); err != nil {
		t.Fatalf("seed other deployment: %v", err)
	}

	h := newOnboardingTestHandlers(store, config.ModeSaaS)
	h.serviceAuthSecret = ownerFactsTestSecret
	token := ownerFactsCloudToken(t, &servicecall.OnBehalfOf{Sub: onboardingTestOwner, OrgID: onboardingTestTenant})
	e, rec := ownerFactsTestEvent(t, token, "")
	if err := h.getOwnerFacts(e); err != nil {
		t.Fatalf("get facts: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var snapshot ownerFactsSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snapshot.Authority != ownerFactsAuthority || snapshot.Version != "1" {
		t.Fatalf("wrong contract identity: %+v", snapshot)
	}
	if snapshot.Facts[onboarding.PredicateKitDeploymentExists] {
		t.Fatal("another principal's deployment leaked into the snapshot")
	}
	if strings.Contains(rec.Body.String(), onboardingTestOwner) || strings.Contains(rec.Body.String(), onboardingTestTenant) {
		t.Fatal("principal identity leaked into the owner-facts body")
	}
	etag := rec.Header().Get("ETag")
	if etag == "" || rec.Header().Get("Cache-Control") != "private, no-cache" {
		t.Fatalf("conditional headers missing: %v", rec.Header())
	}

	conditional, conditionalRec := ownerFactsTestEvent(t, token, etag)
	if err := h.getOwnerFacts(conditional); err != nil {
		t.Fatalf("conditional get: %v", err)
	}
	if conditionalRec.Code != http.StatusNotModified || conditionalRec.Body.Len() != 0 {
		t.Fatalf("conditional response = %d %q", conditionalRec.Code, conditionalRec.Body.String())
	}
}

func TestOwnerFactsRequireCloudOnBehalfOfIdentity(t *testing.T) {
	h := newOnboardingTestHandlers(controlplane.NewMemoryStore(), config.ModeSaaS)
	h.serviceAuthSecret = ownerFactsTestSecret
	token := ownerFactsCloudToken(t, nil)
	e, rec := ownerFactsTestEvent(t, token, "")
	if err := h.getOwnerFacts(e); err != nil {
		t.Fatalf("get facts: %v", err)
	}
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "tenant_context_required") {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
}
