package routes

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

func TestWalletRevealRequiresFreshReauth(t *testing.T) {
	app := newWalletRouteTestApp(t)
	item := createWalletRouteTestItem(t, app, "owner-1", "secret-value")

	handler := walletRouteHandlers{app: app}
	event, recorder := walletRevealRequestEvent("owner-1", item.Id, `{"reason":"copy recovery credential"}`)

	if err := handler.reveal(event); err != nil {
		t.Fatalf("reveal returned unexpected error: %v", err)
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want 403", recorder.Code, recorder.Body.String())
	}
	assertWalletRouteActivity(t, app, walletRevealActionDenied)
}

func TestWalletReauthProofReturnsSignedProofForOwner(t *testing.T) {
	app := newWalletRouteTestApp(t)
	item := createWalletRouteTestItem(t, app, "owner-1", "secret-value")
	secret := "test-reauth-secret" // #nosec G101 -- test-only signing key.
	t.Setenv("TECHSTACK_REAUTH_SECRET", secret)
	now := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	timestamp, signature := signWalletPlatformReauthTestAssertion("owner-1", item.Id, secret, now)

	handler := walletRouteHandlers{
		app: app,
		now: func() time.Time { return now },
	}
	body := `{"reason":"copy recovery credential","platform_reauth_at":` +
		strconv.Quote(timestamp) +
		`,"reauth_assertion":` +
		strconv.Quote(signature) +
		`}`
	event, recorder := walletReauthProofRequestEvent("owner-1", item.Id, body)

	if err := handler.reauthProof(event); err != nil {
		t.Fatalf("reauthProof returned unexpected error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		Data walletReauthProofResponse `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.ID != item.Id || envelope.Data.ReauthTimestamp == "" || envelope.Data.ReauthSignature == "" {
		t.Fatalf("unexpected proof response: %+v", envelope.Data)
	}
	if err := verifyWalletSignature("owner-1", item.Id, envelope.Data.ReauthTimestamp, envelope.Data.ReauthSignature, secret, walletRevealAssertionPurpose, now); err != nil {
		t.Fatalf("verify proof signature: %v", err)
	}
}

func TestWalletReauthProofRejectsSessionOnly(t *testing.T) {
	app := newWalletRouteTestApp(t)
	item := createWalletRouteTestItem(t, app, "owner-1", "secret-value")
	secret := "test-reauth-secret" // #nosec G101 -- test-only signing key.
	t.Setenv("TECHSTACK_REAUTH_SECRET", secret)
	now := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)

	handler := walletRouteHandlers{
		app: app,
		now: func() time.Time { return now },
	}
	event, recorder := walletReauthProofRequestEvent("owner-1", item.Id, `{"reason":"copy recovery credential"}`)
	event.Request.AddCookie(secureWalletTestCookie("fresh-session-without-platform-proof"))

	if err := handler.reauthProof(event); err != nil {
		t.Fatalf("reauthProof returned unexpected error: %v", err)
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want 403", recorder.Code, recorder.Body.String())
	}
}

func TestWalletRevealAcceptsIssuedReauthProof(t *testing.T) {
	app := newWalletRouteTestApp(t)
	item := createWalletRouteTestItem(t, app, "owner-1", "secret-value")
	secret := "test-reauth-secret" // #nosec G101 -- test-only signing key.
	t.Setenv("TECHSTACK_REAUTH_SECRET", secret)
	now := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	timestamp, signature := signWalletPlatformReauthTestAssertion("owner-1", item.Id, secret, now)

	handler := walletRouteHandlers{
		app: app,
		now: func() time.Time { return now },
	}
	proofBody := `{"reason":"copy recovery credential","platform_reauth_at":` +
		strconv.Quote(timestamp) +
		`,"reauth_assertion":` +
		strconv.Quote(signature) +
		`}`
	proofEvent, proofRecorder := walletReauthProofRequestEvent("owner-1", item.Id, proofBody)
	if err := handler.reauthProof(proofEvent); err != nil {
		t.Fatalf("reauthProof returned unexpected error: %v", err)
	}
	var proofEnvelope struct {
		Data walletReauthProofResponse `json:"data"`
	}
	if err := json.Unmarshal(proofRecorder.Body.Bytes(), &proofEnvelope); err != nil {
		t.Fatalf("decode proof response: %v", err)
	}

	body := `{"reason":"copy recovery credential","reauth_timestamp":` +
		strconv.Quote(proofEnvelope.Data.ReauthTimestamp) +
		`,"reauth_signature":` +
		strconv.Quote(proofEnvelope.Data.ReauthSignature) +
		`}`
	revealEvent, revealRecorder := walletRevealRequestEvent("owner-1", item.Id, body)
	if err := handler.reveal(revealEvent); err != nil {
		t.Fatalf("reveal returned unexpected error: %v", err)
	}
	if revealRecorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", revealRecorder.Code, revealRecorder.Body.String())
	}
}

func TestWalletRevealRejectsDifferentOwner(t *testing.T) {
	app := newWalletRouteTestApp(t)
	item := createWalletRouteTestItem(t, app, "owner-1", "secret-value")
	secret := "test-reauth-secret" // #nosec G101 -- test-only signing key.
	t.Setenv("TECHSTACK_REAUTH_SECRET", secret)
	timestamp, signature := signWalletRevealTestAssertion("owner-2", item.Id, secret, time.Now())

	body := `{"reason":"copy recovery credential","reauth_timestamp":` +
		strconv.Quote(timestamp) +
		`,"reauth_signature":` +
		strconv.Quote(signature) +
		`}`
	handler := walletRouteHandlers{app: app}
	event, recorder := walletRevealRequestEvent("owner-2", item.Id, body)

	if err := handler.reveal(event); err != nil {
		t.Fatalf("reveal returned unexpected error: %v", err)
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s, want 404", recorder.Code, recorder.Body.String())
	}
	assertWalletRouteActivity(t, app, walletRevealActionDenied)
}

func TestWalletRevealReturnsSecretAndAudits(t *testing.T) {
	app := newWalletRouteTestApp(t)
	item := createWalletRouteTestItem(t, app, "owner-1", "secret-value")
	secret := "test-reauth-secret" // #nosec G101 -- test-only signing key.
	t.Setenv("TECHSTACK_REAUTH_SECRET", secret)
	timestamp, signature := signWalletRevealTestAssertion("owner-1", item.Id, secret, time.Now())

	body := `{"reason":"copy recovery credential","reauth_timestamp":` +
		strconv.Quote(timestamp) +
		`,"reauth_signature":` +
		strconv.Quote(signature) +
		`}`
	handler := walletRouteHandlers{app: app}
	event, recorder := walletRevealRequestEvent("owner-1", item.Id, body)

	if err := handler.reveal(event); err != nil {
		t.Fatalf("reveal returned unexpected error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}

	var envelope struct {
		Data struct {
			ID     string `json:"id"`
			Secret string `json:"secret"`
			TOTP   string `json:"totp"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if envelope.Data.ID != item.Id {
		t.Fatalf("response id = %q, want %q", envelope.Data.ID, item.Id)
	}
	if envelope.Data.Secret != "secret-value" {
		t.Fatalf("secret = %q, want secret-value", envelope.Data.Secret)
	}
	if strings.Contains(recorder.Body.String(), "owner_id") {
		t.Fatalf("response leaked owner_id: %s", recorder.Body.String())
	}
	assertWalletRouteActivity(t, app, walletRevealAction)
}

func TestWalletCRUDUsesControlPlaneStore(t *testing.T) {
	store := newFakeWalletStore()
	handler := walletRouteHandlers{wst: store}

	createEvent, createRecorder := walletStoreRequestEvent(
		http.MethodPost,
		"/api/v1/wallet",
		`{"id":"wallet-1","name":"Admin","kind":"password","stack_id":"stack-1","service_id":"svc-1","secret":"secret-value"}`,
		"owner-1",
		"tenant-1",
	)
	if err := handler.create(createEvent); err != nil {
		t.Fatalf("create returned unexpected error: %v", err)
	}
	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s, want 201", createRecorder.Code, createRecorder.Body.String())
	}
	created := store.items["wallet-1"]
	if created.TenantID != "tenant-1" || created.StackID != "stack-1" || created.ItemType != "password" {
		t.Fatalf("unexpected stored wallet item: %#v", created)
	}
	if created.Metadata["owner_id"] != "owner-1" || created.Metadata["has_secret"] != true {
		t.Fatalf("unexpected stored metadata: %#v", created.Metadata)
	}
	if strings.Contains(createRecorder.Body.String(), "secret-value") {
		t.Fatalf("create response leaked secret: %s", createRecorder.Body.String())
	}

	listEvent, listRecorder := walletStoreRequestEvent(http.MethodGet, "/api/v1/wallet?stack_id=stack-1", "", "owner-1", "tenant-1")
	if err := handler.list(listEvent); err != nil {
		t.Fatalf("list returned unexpected error: %v", err)
	}
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s, want 200", listRecorder.Code, listRecorder.Body.String())
	}
	if !strings.Contains(listRecorder.Body.String(), `"wallet-1"`) {
		t.Fatalf("list response missing wallet item: %s", listRecorder.Body.String())
	}

	deleteEvent, deleteRecorder := walletStoreRequestEvent(http.MethodDelete, "/api/v1/wallet/wallet-1", "", "owner-1", "tenant-1")
	deleteEvent.Request.SetPathValue("id", "wallet-1")
	if err := handler.delete(deleteEvent); err != nil {
		t.Fatalf("delete returned unexpected error: %v", err)
	}
	if deleteRecorder.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%s, want 200", deleteRecorder.Code, deleteRecorder.Body.String())
	}
	if _, ok := store.items["wallet-1"]; ok {
		t.Fatal("wallet item was not deleted")
	}
}

func TestWalletRevealUsesControlPlaneStore(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertWalletItem(context.Background(), controlplane.WalletItem{
		ID:       "wallet-1",
		TenantID: "tenant-1",
		StackID:  "stack-1",
		ItemType: "password",
		Metadata: map[string]any{
			"owner_id":   "owner-1",
			"name":       "Admin",
			"kind":       "password",
			"secret":     "secret-value",
			"has_secret": true,
			"revealable": true,
		},
	}); err != nil {
		t.Fatalf("UpsertWalletItem: %v", err)
	}
	secret := "test-reauth-secret" // #nosec G101 -- test-only signing key.
	t.Setenv("TECHSTACK_REAUTH_SECRET", secret)
	timestamp, signature := signWalletRevealTestAssertion("owner-1", "wallet-1", secret, time.Now())
	body := `{"reason":"copy recovery credential","reauth_timestamp":` +
		strconv.Quote(timestamp) +
		`,"reauth_signature":` +
		strconv.Quote(signature) +
		`}`

	handler := walletRouteHandlers{wst: store, ast: store}
	event, recorder := walletStoreRequestEvent(http.MethodPost, "/api/v1/wallet/wallet-1/reveal", body, "owner-1", "tenant-1")
	event.Request.SetPathValue("id", "wallet-1")
	if err := handler.reveal(event); err != nil {
		t.Fatalf("reveal returned unexpected error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("reveal status = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"secret":"secret-value"`) {
		t.Fatalf("reveal response missing secret: %s", recorder.Body.String())
	}
	events, err := store.ListActivity(context.Background(), "tenant-1", "", 10)
	if err != nil {
		t.Fatalf("ListActivity: %v", err)
	}
	if len(events) != 1 || events[0].Action != walletRevealAction || events[0].ActorSubjectID != "owner-1" {
		t.Fatalf("unexpected activity events: %#v", events)
	}
}

func newWalletRouteTestApp(t *testing.T) *tests.TestApp {
	t.Helper()

	app, err := tests.NewTestApp(walletRoutePocketBaseTestDataDir(t))
	if err != nil {
		t.Fatalf("new test app: %v", err)
	}
	ensureWalletRouteTestCollections(t, app)
	return app
}

func ensureWalletRouteTestCollections(t *testing.T, app core.App) {
	t.Helper()

	ensureWalletRouteTestCollection(t, app, "wallet",
		&core.TextField{Name: "owner_id"},
		&core.TextField{Name: "stack_id"},
		&core.TextField{Name: "service_id"},
		&core.TextField{Name: "name"},
		&core.TextField{Name: "kind"},
		&core.TextField{Name: "secret", Max: 5000},
		&core.TextField{Name: "totp", Max: 5000},
		&core.TextField{Name: "item_class"},
		&core.BoolField{Name: "revealable"},
		&core.BoolField{Name: "has_secret"},
		&core.BoolField{Name: "has_totp"},
	)
	ensureWalletRouteTestCollection(t, app, "activity_log",
		&core.SelectField{Name: "action", Required: true, Values: []string{
			walletRevealAction,
			walletRevealActionDenied,
		}},
		&core.TextField{Name: "details", Max: 2000},
		&core.JSONField{Name: "metadata"},
		&core.TextField{Name: "stack_id"},
		&core.TextField{Name: "user_id"},
		&core.SelectField{Name: "status", Values: []string{"success", "warning", "error", "info"}},
		&core.TextField{Name: "target", Max: 500},
		&core.TextField{Name: "actor", Max: 200},
		&core.TextField{Name: "resource_type", Max: 200},
		&core.TextField{Name: "resource_id", Max: 200},
	)
}

func ensureWalletRouteTestCollection(t *testing.T, app core.App, name string, fields ...core.Field) *core.Collection {
	t.Helper()

	collection, err := app.FindCollectionByNameOrId(name)
	if err != nil {
		collection = core.NewBaseCollection(name)
	}
	for _, field := range fields {
		if collection.Fields.GetByName(field.GetName()) == nil {
			collection.Fields.Add(field)
		}
	}
	if err := app.Save(collection); err != nil {
		t.Fatalf("save %s collection: %v", name, err)
	}
	return collection
}

func createWalletRouteTestItem(t *testing.T, app core.App, ownerID, secret string) *core.Record {
	t.Helper()

	collection, err := app.FindCollectionByNameOrId("wallet")
	if err != nil {
		t.Fatalf("find wallet collection: %v", err)
	}
	record := core.NewRecord(collection)
	record.Set("owner_id", ownerID)
	record.Set("name", "Recovery")
	record.Set("kind", "password")
	record.Set("secret", secret)
	record.Set("has_secret", true)
	record.Set("revealable", true)
	record.Set("item_class", "recovery")
	if err := app.Save(record); err != nil {
		t.Fatalf("save wallet item: %v", err)
	}
	return record
}

func walletRevealRequestEvent(ownerID, itemID, body string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/wallet/"+itemID+"/reveal", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", itemID)
	req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{UserID: ownerID}))
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

func walletReauthProofRequestEvent(ownerID, itemID, body string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/wallet/"+itemID+"/reauth-proof", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", itemID)
	req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{UserID: ownerID}))
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

func walletStoreRequestEvent(method, target, body, userID, tenantID string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{UserID: userID, OrgID: tenantID}))
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

func secureWalletTestCookie(value string) *http.Cookie {
	return &http.Cookie{
		Name:     "techstack_session",
		Value:    value,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

func signWalletRevealTestAssertion(userID, walletID, secret string, at time.Time) (string, string) {
	timestamp := strconv.FormatInt(at.Unix(), 10)
	return timestamp, hex.EncodeToString(signWalletAssertion(walletRevealAssertionPurpose, userID, walletID, timestamp, secret))
}

func signWalletPlatformReauthTestAssertion(userID, walletID, secret string, at time.Time) (string, string) {
	timestamp := strconv.FormatInt(at.Unix(), 10)
	return timestamp, hex.EncodeToString(signWalletAssertion(walletPlatformReauthAssertionPurpose, userID, walletID, timestamp, secret))
}

func assertWalletRouteActivity(t *testing.T, app core.App, action string) {
	t.Helper()

	records, err := app.FindRecordsByFilter("activity_log", "action = {:action}", "", 0, 0, map[string]any{"action": action})
	if err != nil {
		t.Fatalf("find activity: %v", err)
	}
	if len(records) == 0 {
		t.Fatalf("expected activity action %q", action)
	}
}

func walletRoutePocketBaseTestDataDir(t *testing.T) string {
	t.Helper()

	cmd := exec.Command("go", "env", "GOMODCACHE")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("resolve go module cache: %v", err)
	}

	modCache := strings.TrimSpace(string(output))
	if modCache == "" {
		t.Fatal("resolve go module cache: empty result")
	}

	matches, err := filepath.Glob(filepath.Join(modCache, "github.com", "pocketbase", "pocketbase@*", "tests", "data"))
	if err != nil {
		t.Fatalf("resolve pocketbase test data: %v", err)
	}
	if len(matches) == 0 {
		matches, err = filepath.Glob(filepath.Join(modCache, "github.com", "*", "pocketbase@*", "tests", "data"))
		if err != nil {
			t.Fatalf("resolve pocketbase test data fallback: %v", err)
		}
	}
	for _, match := range matches {
		if strings.Contains(filepath.ToSlash(match), path.Join("github.com", "pocketbase", "pocketbase@")) {
			return match
		}
	}
	if len(matches) > 0 {
		return matches[0]
	}
	t.Fatal("resolve pocketbase test data: no matches")
	return ""
}

type fakeWalletStore struct {
	items map[string]controlplane.WalletItem
}

func newFakeWalletStore() *fakeWalletStore {
	return &fakeWalletStore{items: map[string]controlplane.WalletItem{}}
}

func (f *fakeWalletStore) UpsertWalletItem(_ context.Context, item controlplane.WalletItem) (*controlplane.WalletItem, error) {
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	item.UpdatedAt = time.Now().UTC()
	f.items[item.ID] = item
	return &item, nil
}

func (f *fakeWalletStore) GetWalletItem(_ context.Context, tenantID, itemID string) (*controlplane.WalletItem, error) {
	item, ok := f.items[itemID]
	if !ok || item.TenantID != tenantID {
		return nil, controlplane.ErrNotFound
	}
	return &item, nil
}

func (f *fakeWalletStore) ListWalletItems(_ context.Context, tenantID, stackID string) ([]controlplane.WalletItem, error) {
	var out []controlplane.WalletItem
	for _, item := range f.items {
		if item.TenantID != tenantID {
			continue
		}
		if stackID != "" && item.StackID != stackID {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func (f *fakeWalletStore) DeleteWalletItem(_ context.Context, tenantID, itemID string) error {
	item, ok := f.items[itemID]
	if !ok || item.TenantID != tenantID {
		return controlplane.ErrNotFound
	}
	delete(f.items, itemID)
	return nil
}
