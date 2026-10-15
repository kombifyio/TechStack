package routes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

func TestWalletHTTPDerivesOwnerAndProtectsIdentity(t *testing.T) {
	store := controlplane.NewMemoryStore()
	encryptor, err := auth.NewSecretEncryptor([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	raceStore := &walletDeleteRaceStore{WalletStore: store}
	handlers := walletRouteHandlers{wst: raceStore, encryptor: encryptor}
	router := httpx.NewRouter()
	router.GET("/api/v1/wallet", handlers.list)
	router.POST("/api/v1/wallet", handlers.create)
	router.PATCH("/api/v1/wallet/{id}", handlers.update)
	router.DELETE("/api/v1/wallet/{id}", handlers.delete)
	mux := router.BuildMux()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This fixture substitutes only the trusted authentication middleware.
		principal := &identity.Identity{UserID: r.Header.Get("Test-Owner"), OrgID: "tenant-shared"}
		mux.ServeHTTP(w, r.WithContext(identity.NewContext(r.Context(), principal)))
	}))
	t.Cleanup(server.Close)
	request := func(method, path, owner, body string, wantStatus int) map[string]any {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Test-Owner", owner)
		req.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != wantStatus {
			t.Fatalf("%s %s as %s: status %d, want %d, body %s", method, path, owner, response.StatusCode, wantStatus, data)
		}
		if strings.Contains(string(data), "synthetic-custody-canary") {
			t.Fatal("HTTP CRUD disclosed credential material")
		}
		var envelope struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data
	}
	created := request(http.MethodPost, "/api/v1/wallet", "owner-a",
		`{"owner_id":"owner-b","owner_subject_id":"owner-b","user_id":"owner-b","kind":"ssh_key","secret":"synthetic-custody-canary","name":"Original"}`, http.StatusCreated)
	id, _ := created["id"].(string)
	assertCustody := func(wantName string) {
		t.Helper()
		item, err := store.GetWalletItem(t.Context(), "tenant-shared", id)
		if err != nil || controlplane.WalletOwnerID(item.Metadata) != "owner-a" || item.ItemType != "ssh_key" || item.Metadata["name"] != wantName {
			t.Fatalf("authenticated custody changed: %v, %v", item, err)
		}
		secret, err := encryptor.Decrypt(walletString(item.Metadata["secret"]))
		if err != nil || secret != "synthetic-custody-canary" {
			t.Fatal("custody canary changed or lost encryption")
		}
	}
	assertCustody("Original")
	request(http.MethodPost, "/api/v1/wallet", "owner-b", `{"id":`+jsonQuoteWallet(id)+`,"kind":"ssh_key"}`, http.StatusBadRequest)
	request(http.MethodPatch, "/api/v1/wallet/"+id, "owner-b", `{"name":"Hijacked"}`, http.StatusNotFound)
	request(http.MethodDelete, "/api/v1/wallet/"+id, "owner-b", "", http.StatusNotFound)
	for _, attempt := range []string{
		`{"owner_id":"owner-b"}`, `{"owner_subject_id":"owner-b"}`, `{"user_id":"owner-b"}`,
		`{"kind":"password"}`, `{"item_type":"password"}`, `{"id":"attacker-id"}`,
	} {
		request(http.MethodPatch, "/api/v1/wallet/"+id, "owner-a", attempt, http.StatusConflict)
		assertCustody("Original")
	}
	foreign := request(http.MethodGet, "/api/v1/wallet", "owner-b", "", http.StatusOK)
	items, _ := foreign["items"].([]any)
	for _, item := range items {
		if item.(map[string]any)["id"] == id {
			t.Fatal("another owner discovered the credential")
		}
	}
	request(http.MethodPatch, "/api/v1/wallet/"+id, "owner-a", `{"name":"Renamed"}`, http.StatusOK)
	assertCustody("Renamed")
	raceStore.replacement = &controlplane.WalletItem{ID: id, TenantID: "tenant-shared", ItemType: "ssh_key",
		Metadata: map[string]any{"owner_id": "owner-b", "name": "Successor", "secret": "synthetic-replacement"}}
	request(http.MethodDelete, "/api/v1/wallet/"+id, "owner-a", "", http.StatusConflict)
	successor, err := store.GetWalletItem(t.Context(), "tenant-shared", id)
	if err != nil || controlplane.WalletOwnerID(successor.Metadata) != "owner-b" || successor.Metadata["secret"] != "synthetic-replacement" {
		t.Fatalf("HTTP stale delete changed successor custody: %v, %v", successor, err)
	}
}

// Simulate an internal writer replacing an ID after the route's owner check.
type walletDeleteRaceStore struct {
	controlplane.WalletStore
	replacement *controlplane.WalletItem
}

func (s *walletDeleteRaceStore) DeleteWalletItemForOwner(ctx context.Context, tenantID, itemID, ownerID, itemType string) error {
	if s.replacement != nil {
		if err := s.WalletStore.DeleteWalletItem(ctx, tenantID, itemID); err != nil {
			return err
		}
		if _, err := s.WalletStore.CreateWalletItem(ctx, *s.replacement); err != nil {
			return err
		}
		s.replacement = nil
	}
	return s.WalletStore.DeleteWalletItemForOwner(ctx, tenantID, itemID, ownerID, itemType)
}

func jsonQuoteWallet(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
