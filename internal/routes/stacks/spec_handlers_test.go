package stacks

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	kscore "github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/unifier"
)

func TestSpecStackIDFromPathRejectsMissingPath(t *testing.T) {
	e, rec := specRouteTestEvent(http.MethodGet, "/api/v1/stacks//intent", "", "owner-a")

	stackID, ok, err := specStackIDFromPath(e)
	if err != nil {
		t.Fatalf("specStackIDFromPath returned unexpected response error: %v", err)
	}
	if ok {
		t.Fatal("specStackIDFromPath ok = true, want false")
	}
	if stackID != "" {
		t.Fatalf("specStackIDFromPath stackID = %q, want empty", stackID)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("response status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestSpecRouteContextRejectsMissingAuth(t *testing.T) {
	e, rec := specRouteTestEvent(http.MethodGet, "/api/v1/stacks/stack-a/intent", "stack-a", "")
	handler := specRouteHandlers{}

	routeCtx, ok, err := handler.routeContext(e)
	if err == nil {
		t.Fatal("routeContext must return an error once it has written the refusal")
	}
	if ok {
		t.Fatal("routeContext ok = true, want false")
	}
	if routeCtx.stackID != "" || routeCtx.persister != nil {
		t.Fatalf("routeContext returned populated context: %+v", routeCtx)
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("response status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestSpecRouteContextRejectsForeignOwner(t *testing.T) {
	app := newOwnerSpecTestApp(t)
	stack := createOwnerSpecTestStack(t, app, "owner-a")
	e, rec := specRouteTestEvent(http.MethodGet, "/api/v1/stacks/"+stack.Id+"/intent", stack.Id, "owner-b")
	handler := specRouteHandlers{app: app}

	routeCtx, ok, err := handler.routeContext(e)
	if err != nil {
		t.Fatalf("routeContext returned unexpected response error: %v", err)
	}
	if ok {
		t.Fatal("routeContext ok = true, want false")
	}
	if routeCtx.stackID != "" || routeCtx.persister != nil {
		t.Fatalf("routeContext returned populated context: %+v", routeCtx)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("response status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestSpecIntentResponse(t *testing.T) {
	data := []byte("kind: IntentSpec\n")
	got := specIntentResponse("stack-a", data)

	if got[specResponseStackIDKey] != "stack-a" {
		t.Fatalf("stack ID = %v, want stack-a", got[specResponseStackIDKey])
	}
	if got[specResponseFilenameKey] != unifier.IntentSpecFilename {
		t.Fatalf("filename = %v, want %s", got[specResponseFilenameKey], unifier.IntentSpecFilename)
	}
	if got[specResponseEncodingKey] != specBase64Encoding {
		t.Fatalf("encoding = %v, want %s", got[specResponseEncodingKey], specBase64Encoding)
	}
	if got[specResponseSHA256Key] != unifier.ComputeDataHash(data) {
		t.Fatalf("sha256 = %v, want %s", got[specResponseSHA256Key], unifier.ComputeDataHash(data))
	}
	if got[specResponseByteLengthKey] != len(data) {
		t.Fatalf("byteLength = %v, want %d", got[specResponseByteLengthKey], len(data))
	}
	if got[specResponseContentKey] != base64.StdEncoding.EncodeToString(data) {
		t.Fatalf("content = %v, want base64 payload", got[specResponseContentKey])
	}
}

func TestSpecPipelineStatusResponseIncludesPersistedMetadata(t *testing.T) {
	persister, err := unifier.NewSpecPersisterWithPath(t.TempDir())
	if err != nil {
		t.Fatalf("new persister: %v", err)
	}
	intentPath, _, err := persister.SaveIntentBytes([]byte("kind: IntentSpec\n"))
	if err != nil {
		t.Fatalf("save intent: %v", err)
	}
	requirements := &kscore.RequirementsSpec{}
	requirementsPath, err := persister.SaveRequirementsSpec(requirements, intentPath)
	if err != nil {
		t.Fatalf("save requirements: %v", err)
	}
	unified := &kscore.UnifiedSpec{}
	if _, err := persister.SaveUnifiedSpec(unified, requirementsPath); err != nil {
		t.Fatalf("save unified: %v", err)
	}

	got := specPipelineStatusResponse("stack-a", persister)
	if got[specResponseIntentExistsKey] != true {
		t.Fatalf("intent_exists = %v, want true", got[specResponseIntentExistsKey])
	}
	if got[specResponseRequirementsKey] != true {
		t.Fatalf("requirements_exists = %v, want true", got[specResponseRequirementsKey])
	}
	if got[specResponseUnifiedExistsKey] != true {
		t.Fatalf("unified_exists = %v, want true", got[specResponseUnifiedExistsKey])
	}
	if got[specResponseHashChainValidKey] != true {
		t.Fatalf("hash_chain_valid = %v, want true", got[specResponseHashChainValidKey])
	}
	if got[specResponseIntentHashKey] == "" {
		t.Fatal("intent_sha256 is empty")
	}
	if got[specResponseRequirementsMeta] == nil {
		t.Fatal("requirements_metadata is missing")
	}
	if got[specResponseUnifiedMetaKey] == nil {
		t.Fatal("unified_metadata is missing")
	}
}

func specRouteTestEvent(method, target, stackID, userID string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, nil)
	if stackID != "" {
		req.SetPathValue(specPathStackIDKey, stackID)
	}
	if userID != "" {
		req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{UserID: userID}))
	}
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}
