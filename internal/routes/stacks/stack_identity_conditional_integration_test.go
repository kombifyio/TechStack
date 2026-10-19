package stacks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/localdb"
	"github.com/kombifyio/techstack/pkg/controlplane"
	pgkdb "github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

type stackIdentityReadInterleaver struct {
	controlplane.HomelabStore
	conditional controlplane.ConditionalStackIdentityStore
	afterRead   func()
}

func (s stackIdentityReadInterleaver) ReadStackIdentitySnapshot(ctx context.Context, tenantID, ownerID string) (*controlplane.Homelab, int64, error) {
	homelab, revision, err := s.conditional.ReadStackIdentitySnapshot(ctx, tenantID, ownerID)
	if err == nil && s.afterRead != nil {
		s.afterRead()
	}
	return homelab, revision, err
}

func (s stackIdentityReadInterleaver) ApplyStackIdentityMutation(ctx context.Context, input controlplane.StackIdentityMutation) (*controlplane.StackIdentityMutationResult, error) {
	return s.conditional.ApplyStackIdentityMutation(ctx, input)
}

// The HTTP boundary runs against a disposable real PostgreSQL in the normal
// focused package gate. CI supplies the embedded PostgreSQL binaries.
func TestConditionalStackIdentityIsDurableAndOwnerScoped(t *testing.T) {
	baseDir := t.TempDir()
	t.Setenv(localdb.EnvEmbeddedPostgresDir, filepath.Join(baseDir, "postgres"))
	t.Setenv(localdb.EnvEmbeddedPostgresPort, "")
	t.Setenv(localdb.EnvEmbeddedPostgresBundleDir, "")
	embedded, err := localdb.StartEmbeddedPostgres(baseDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := embedded.Stop(); err != nil {
			t.Error(err)
		}
	})
	database, err := pgkdb.Open(pgkdb.Config{Backend: pgkdb.StoreBackendPostgres, DSN: embedded.DSN(), DriverName: pgkdb.PostgresDriverName})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	store := controlplane.NewPostgresStore(database.DB)
	tenantID := fmt.Sprintf("identity-mutation-%d", time.Now().UnixNano())
	if _, err := store.EnsureTenant(t.Context(), controlplane.Tenant{ID: tenantID, DisplayName: tenantID, Kind: "self_hosted", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	h := crudRouteHandlers{homelabStore: store}
	call := func(owner, method, body, match, key string) (*httptest.ResponseRecorder, stackIdentityResponse) {
		t.Helper()
		path := "/api/v1/auth/stack-identity"
		if method == http.MethodPut {
			path += "/conditional"
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{UserID: owner, OrgID: tenantID}))
		req.Header.Set("If-Match", match)
		req.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		event := &httpx.Event{Request: req, Response: rec}
		if method == http.MethodPut {
			_ = h.updateStackIdentityConditional(event)
		} else {
			_ = h.getStackIdentity(event)
		}
		var envelope struct {
			Data stackIdentityResponse `json:"data"`
		}
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
		}
		return rec, envelope.Data
	}

	initial, _ := call("owner-one", http.MethodGet, "", "", "")
	if initial.Code != http.StatusOK || initial.Header().Get("ETag") != `"0"` {
		t.Fatalf("initial read status=%d etag=%q", initial.Code, initial.Header().Get("ETag"))
	}
	body := `{"name":"Home Alpha"}`
	key := "identity-mutation-one"
	first, firstData := call("owner-one", http.MethodPut, body, `"0"`, key)
	if first.Code != http.StatusOK || first.Header().Get("ETag") != `"1"` ||
		firstData.StackIdentity == nil || firstData.StackIdentity.Name != "Home Alpha" ||
		firstData.Mutation == nil || firstData.Mutation.ID != key ||
		firstData.Mutation.TenantID != tenantID || firstData.Mutation.OwnerSubjectID != "owner-one" {
		t.Fatalf("first write status=%d etag=%q body=%s", first.Code, first.Header().Get("ETag"), first.Body.String())
	}
	// The reply is intentionally lost. The old precondition and exact key must
	// recover the original result, without applying a second edit.
	retry, retryData := call("owner-one", http.MethodPut, body, `"0"`, key)
	if retry.Code != http.StatusOK || retry.Header().Get("ETag") != `"1"` || !reflect.DeepEqual(firstData, retryData) {
		t.Fatalf("retry changed the result: status=%d etag=%q body=%s", retry.Code, retry.Header().Get("ETag"), retry.Body.String())
	}
	afterRetry, afterRetryData := call("owner-one", http.MethodGet, "", "", "")
	if afterRetry.Code != http.StatusOK || afterRetry.Header().Get("ETag") != `"1"` ||
		afterRetryData.StackIdentity == nil || afterRetryData.StackIdentity.Name != "Home Alpha" {
		t.Fatalf("retry changed stored identity: status=%d etag=%q body=%s", afterRetry.Code, afterRetry.Header().Get("ETag"), afterRetry.Body.String())
	}
	changedKey, _ := call("owner-one", http.MethodPut, `{"name":"Different"}`, `"0"`, key)
	if changedKey.Code != http.StatusConflict {
		t.Fatalf("changed payload under same key status=%d body=%s", changedKey.Code, changedKey.Body.String())
	}
	stale, _ := call("owner-one", http.MethodPut, `{"name":"Stale"}`, `"0"`, "identity-mutation-two")
	if stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale precondition status=%d body=%s", stale.Code, stale.Body.String())
	}
	secondOwner, secondData := call("owner-two", http.MethodPut, `{"name":"Home Beta"}`, `"0"`, key)
	if secondOwner.Code != http.StatusOK || secondOwner.Header().Get("ETag") != `"1"` ||
		secondData.StackIdentity == nil || secondData.StackIdentity.Name != "Home Beta" {
		t.Fatalf("other owner write status=%d etag=%q body=%s", secondOwner.Code, secondOwner.Header().Get("ETag"), secondOwner.Body.String())
	}
	ownerOne, ownerOneData := call("owner-one", http.MethodGet, "", "", "")
	if ownerOne.Code != http.StatusOK || ownerOneData.StackIdentity == nil || ownerOneData.StackIdentity.Name != "Home Alpha" {
		t.Fatalf("other owner changed first identity: status=%d body=%s", ownerOne.Code, ownerOne.Body.String())
	}
	ownedHomelab, err := store.GetHomelabByOwner(t.Context(), tenantID, "owner-one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateHomelabName(t.Context(), tenantID, ownedHomelab.ID, "Renamed Online"); err != nil {
		t.Fatal(err)
	}
	afterLegacy, legacyData := call("owner-one", http.MethodGet, "", "", "")
	if afterLegacy.Code != http.StatusOK || afterLegacy.Header().Get("ETag") != `"2"` ||
		legacyData.StackIdentity == nil || legacyData.StackIdentity.Name != "Renamed Online" {
		t.Fatalf("online rename did not advance identity revision: status=%d etag=%q body=%s", afterLegacy.Code, afterLegacy.Header().Get("ETag"), afterLegacy.Body.String())
	}
	replayAfterLegacy, replayAfterLegacyData := call("owner-one", http.MethodPut, body, `"0"`, key)
	if replayAfterLegacy.Code != http.StatusOK || replayAfterLegacy.Header().Get("ETag") != `"1"` ||
		!reflect.DeepEqual(firstData, replayAfterLegacyData) {
		t.Fatalf("later online edit changed original receipt: status=%d etag=%q body=%s", replayAfterLegacy.Code, replayAfterLegacy.Header().Get("ETag"), replayAfterLegacy.Body.String())
	}
	beforeCreate, _ := call("owner-create", http.MethodGet, "", "", "")
	if beforeCreate.Header().Get("ETag") != `"0"` {
		t.Fatalf("pre-create etag=%q", beforeCreate.Header().Get("ETag"))
	}
	created, err := store.CreateHomelab(t.Context(), controlplane.CreateHomelabRequest{
		ID: "created-homelab", TenantID: tenantID, OwnerSubjectID: "owner-create", Name: "Generated Home",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "created-homelab" {
		t.Fatalf("created homelab id=%q", created.ID)
	}
	readCreated, _ := call("owner-create", http.MethodGet, "", "", "")
	if readCreated.Header().Get("ETag") != `"1"` {
		t.Fatalf("create did not advance revision: etag=%q", readCreated.Header().Get("ETag"))
	}
	staleAfterCreate, _ := call("owner-create", http.MethodPut, `{"name":"Old Draft"}`, `"0"`, "identity-after-create")
	if staleAfterCreate.Code != http.StatusPreconditionFailed {
		t.Fatalf("pre-create draft was accepted: status=%d body=%s", staleAfterCreate.Code, staleAfterCreate.Body.String())
	}
	for i := 0; i < 2; i++ {
		if _, err := store.GetOrCreateHomelabForOwner(t.Context(), controlplane.CreateHomelabRequest{
			ID: "get-or-create-homelab", TenantID: tenantID, OwnerSubjectID: "owner-get-or-create", Name: "Generated Home",
		}); err != nil {
			t.Fatal(err)
		}
	}
	afterGetOrCreate, _ := call("owner-get-or-create", http.MethodGet, "", "", "")
	if afterGetOrCreate.Header().Get("ETag") != `"1"` {
		t.Fatalf("get-or-create revision=%q, want one advance", afterGetOrCreate.Header().Get("ETag"))
	}
	staleAfterGetOrCreate, _ := call("owner-get-or-create", http.MethodPut, `{"name":"Old Draft"}`, `"0"`, "identity-after-get-or-create")
	if staleAfterGetOrCreate.Code != http.StatusPreconditionFailed {
		t.Fatalf("pre-get-or-create draft was accepted: status=%d body=%s", staleAfterGetOrCreate.Code, staleAfterGetOrCreate.Body.String())
	}

	// Force a conditional edit after sync's snapshot but before its Cloud
	// adoption write. The latter must refuse to overwrite the newer edit.
	interleaved := false
	interleaver := stackIdentityReadInterleaver{HomelabStore: store, conditional: store, afterRead: func() {
		interleaved = true
		edit, _ := call("owner-create", http.MethodPut, `{"name":"Offline Won"}`, `"1"`, "identity-interleaved-edit")
		if edit.Code != http.StatusOK {
			t.Fatalf("interleaved edit status=%d body=%s", edit.Code, edit.Body.String())
		}
	}}
	syncRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/stack-identity/sync",
		strings.NewReader(`{"cloud":{"revision":1,"name":"Cloud Name","updated_at":"2026-09-27T10:00:00Z"}}`))
	syncRequest = syncRequest.WithContext(identity.NewContext(context.Background(), &identity.Identity{UserID: "owner-create", OrgID: tenantID}))
	syncReply := httptest.NewRecorder()
	_ = (crudRouteHandlers{homelabStore: interleaver}).syncStackIdentity(&httpx.Event{Request: syncRequest, Response: syncReply})
	if !interleaved || syncReply.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale Cloud adoption status=%d interleaved=%v body=%s", syncReply.Code, interleaved, syncReply.Body.String())
	}
	afterSync, afterSyncData := call("owner-create", http.MethodGet, "", "", "")
	if afterSync.Header().Get("ETag") != `"2"` || afterSyncData.StackIdentity == nil || afterSyncData.StackIdentity.Name != "Offline Won" {
		t.Fatalf("stale Cloud adoption overwrote edit: etag=%q body=%s", afterSync.Header().Get("ETag"), afterSync.Body.String())
	}
}
