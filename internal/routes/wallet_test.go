package routes

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

func TestWalletRevealRequiresFreshReauth(t *testing.T) {
	app := newWalletRouteTestApp(t)
	item := createWalletRouteTestItem(t, app, "owner-1", "secret-value")

	handler := walletRouteHandlers{wst: app, ast: app}
	event, recorder := walletRevealRequestEvent("owner-1", item.Id, `{"reason":"copy recovery credential"}`)

	if err := handler.reveal(event); err != nil {
		t.Fatalf("reveal returned unexpected error: %v", err)
	}
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want 403", recorder.Code, recorder.Body.String())
	}
	assertWalletRouteActivity(t, app, "owner-1", walletRevealActionDenied)
}

func TestWalletReauthProofReturnsSignedProofForOwner(t *testing.T) {
	app := newWalletRouteTestApp(t)
	item := createWalletRouteTestItem(t, app, "owner-1", "secret-value")
	secret := "test-reauth-secret" // #nosec G101 -- test-only signing key.
	t.Setenv("TECHSTACK_REAUTH_SECRET", secret)
	now := time.Date(2026, 5, 14, 10, 0, 0, 0, time.UTC)
	timestamp, signature := signWalletPlatformReauthTestAssertion("owner-1", item.Id, secret, now)

	handler := walletRouteHandlers{
		wst: app,
		ast: app,
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
		wst: app,
		ast: app,
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
		wst: app,
		ast: app,
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
	handler := walletRouteHandlers{wst: app, ast: app}
	event, recorder := walletRevealRequestEvent("owner-2", item.Id, body)

	if err := handler.reveal(event); err != nil {
		t.Fatalf("reveal returned unexpected error: %v", err)
	}
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d body=%s, want 404", recorder.Code, recorder.Body.String())
	}
	assertWalletRouteActivity(t, app, "owner-2", walletRevealActionDenied)
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
	handler := walletRouteHandlers{wst: app, ast: app}
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
	assertWalletRouteActivity(t, app, "owner-1", walletRevealAction)
}

func TestWalletCRUDUsesControlPlaneStore(t *testing.T) {
	store := newFakeWalletStore()
	encryptor, err := auth.NewSecretEncryptor([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("NewSecretEncryptor: %v", err)
	}
	handler := walletRouteHandlers{wst: store, encryptor: encryptor}

	createEvent, createRecorder := walletStoreRequestEvent(
		http.MethodPost,
		"/api/v1/wallet",
		`{"id":"wallet-1","name":"Admin","kind":"password","stack_id":"stack-1","service_id":"svc-1","source_type":"stack","secret":"secret-value","totp":"123456"}`,
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
	storedSecret := walletString(created.Metadata["secret"])
	if !auth.IsEncrypted(storedSecret) {
		t.Fatalf("stored secret is not encrypted: %q", storedSecret)
	}
	decrypted, err := encryptor.Decrypt(storedSecret)
	if err != nil || decrypted != "secret-value" {
		t.Fatalf("decrypt stored secret = %q, %v", decrypted, err)
	}
	storedTOTP := walletString(created.Metadata["totp"])
	decryptedTOTP, err := encryptor.Decrypt(storedTOTP)
	if err != nil || decryptedTOTP != "123456" {
		t.Fatalf("decrypt stored totp = %q, %v", decryptedTOTP, err)
	}
	if strings.Contains(createRecorder.Body.String(), "secret-value") {
		t.Fatalf("create response leaked secret: %s", createRecorder.Body.String())
	}
	var createResponse struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(createRecorder.Body.Bytes(), &createResponse); err != nil || createResponse.Data["kit_deployment_id"] != "stack-1" || createResponse.Data["source_type"] != "kit_deployment" {
		t.Fatalf("create response = %s, want canonical wallet ownership", createRecorder.Body.String())
	}
	store.items["wallet-foreign"] = controlplane.WalletItem{
		ID: "wallet-foreign", TenantID: "tenant-1", StackID: "stack-1", Metadata: map[string]any{"owner_id": "owner-2", "name": "Foreign"},
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
	if strings.Contains(listRecorder.Body.String(), "wallet-foreign") {
		t.Fatalf("list response exposed another owner's wallet item: %s", listRecorder.Body.String())
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

func newWalletRouteTestApp(t *testing.T) *controlplane.MemoryStore {
	t.Helper()
	return controlplane.NewMemoryStore()
}

type walletRouteTestItem struct {
	Id string
}

func createWalletRouteTestItem(t *testing.T, app controlplane.WalletStore, ownerID, secret string) *walletRouteTestItem {
	t.Helper()
	item := controlplane.WalletItem{
		ID:       "wallet-" + ownerID,
		TenantID: ownerID,
		ItemType: "password",
		Metadata: map[string]any{
			"owner_id": ownerID, "name": "Recovery", "kind": "password",
			"secret": secret, "has_secret": true, "revealable": true, "item_class": "recovery",
		},
	}
	if _, err := app.UpsertWalletItem(context.Background(), item); err != nil {
		t.Fatalf("save wallet item: %v", err)
	}
	return &walletRouteTestItem{Id: item.ID}
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

func assertWalletRouteActivity(t *testing.T, app controlplane.ActivityStore, tenantID, action string) {
	t.Helper()
	records, err := app.ListActivity(context.Background(), tenantID, "", 100)
	if err != nil {
		t.Fatalf("find activity: %v", err)
	}
	for _, record := range records {
		if record.Action == action {
			return
		}
	}
	t.Fatalf("expected activity action %q", action)
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
