package routes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

type fakeWizardRemoteSSHTester struct {
	lastHost     string
	lastPort     int
	lastUser     string
	lastPassword string
	lastKey      string
	err          error
}

func (f *fakeWizardRemoteSSHTester) TestSSH(_ context.Context, host string, port int, username, password string) error {
	f.lastHost = host
	f.lastPort = port
	f.lastUser = username
	f.lastPassword = password
	return f.err
}

func (f *fakeWizardRemoteSSHTester) TestSSHWithKey(_ context.Context, host string, port int, username, privateKey string) error {
	f.lastHost = host
	f.lastPort = port
	f.lastUser = username
	f.lastKey = privateKey
	return f.err
}

type fakeWizardFeatureChecker struct {
	enabled bool
}

func (f fakeWizardFeatureChecker) IsEnabled(context.Context, string, string) (bool, error) {
	return f.enabled, nil
}

func TestDecodeWizardRemoteSSHTestRequestDefaultsPort(t *testing.T) {
	req, err := decodeWizardRemoteSSHTestRequest(io.NopCloser(strings.NewReader(`{"host":"server.example.test","user":"root","auth_method":"password","password":"secret"}`)))
	if err != nil {
		t.Fatalf("decodeWizardRemoteSSHTestRequest returned error: %v", err)
	}
	if req.Port != 22 {
		t.Fatalf("port = %d, want 22", req.Port)
	}
}

func TestWizardRemoteSSHTestRequiresPasswordForPasswordAuth(t *testing.T) {
	handler := wizardRouteHandlers{
		cfg: WizardRouteConfig{
			Features:          fakeWizardFeatureChecker{enabled: true},
			RemoteSSHTester:   &fakeWizardRemoteSSHTester{},
		},
	}
	event, recorder := wizardRemoteSSHTestEvent(`{"host":"server.example.test","user":"root","auth_method":"password"}`)
	if err := handler.testRemoteSSH(event); err != nil {
		t.Fatalf("testRemoteSSH returned error: %v", err)
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
}

func TestWizardRemoteSSHTestUsesPasswordAuth(t *testing.T) {
	tester := &fakeWizardRemoteSSHTester{}
	handler := wizardRouteHandlers{
		cfg: WizardRouteConfig{
			Features:        fakeWizardFeatureChecker{enabled: true},
			RemoteSSHTester: tester,
		},
	}
	event, recorder := wizardRemoteSSHTestEvent(`{"host":"server.example.test","user":"root","port":2222,"auth_method":"password","password":"secret"}`)
	if err := handler.testRemoteSSH(event); err != nil {
		t.Fatalf("testRemoteSSH returned error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if tester.lastHost != "server.example.test" || tester.lastPort != 2222 || tester.lastUser != "root" || tester.lastPassword != "secret" {
		t.Fatalf("unexpected tester call: %+v", tester)
	}
	var envelope struct {
		Data struct {
			Success bool   `json:"success"`
			Message string `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !envelope.Data.Success {
		t.Fatalf("success = false, body=%s", recorder.Body.String())
	}
}

func TestWizardRemoteSSHTestResolvesWalletSSHKeyByLabel(t *testing.T) {
	tester := &fakeWizardRemoteSSHTester{}
	store := controlplane.NewMemoryStore()
	item, err := store.UpsertWalletItem(context.Background(), controlplane.WalletItem{
		ID:       "wallet-1",
		TenantID: "tenant-1",
		ItemType: "ssh_key",
		Metadata: map[string]any{
			"owner_id":   "owner-1",
			"name":       "main-key",
			"kind":       "ssh_key",
			"secret":     "PRIVATE-KEY",
			"has_secret": true,
		},
	})
	if err != nil {
		t.Fatalf("UpsertWalletItem: %v", err)
	}
	if item == nil {
		t.Fatal("expected wallet item")
	}
	handler := wizardRouteHandlers{
		cfg: WizardRouteConfig{
			Features:          fakeWizardFeatureChecker{enabled: true},
			RemoteSSHTester:   tester,
			Wallet:            store,
		},
	}
	event, recorder := wizardRemoteSSHTestEvent(`{"host":"server.example.test","user":"ubuntu","auth_method":"ssh-key","ssh_key_label":"main-key"}`)
	if err := handler.testRemoteSSH(event); err != nil {
		t.Fatalf("testRemoteSSH returned error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if tester.lastKey != "PRIVATE-KEY" {
		t.Fatalf("lastKey = %q, want PRIVATE-KEY", tester.lastKey)
	}
}

func TestWizardRemoteSSHTestReturnsProbeFailure(t *testing.T) {
	handler := wizardRouteHandlers{
		cfg: WizardRouteConfig{
			Features:        fakeWizardFeatureChecker{enabled: true},
			RemoteSSHTester: &fakeWizardRemoteSSHTester{err: errors.New("SSH connection failed: auth denied")},
		},
	}
	event, recorder := wizardRemoteSSHTestEvent(`{"host":"server.example.test","user":"root","auth_method":"password","password":"secret"}`)
	if err := handler.testRemoteSSH(event); err != nil {
		t.Fatalf("testRemoteSSH returned error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "auth denied") {
		t.Fatalf("expected probe failure in body: %s", recorder.Body.String())
	}
}

func wizardRemoteSSHTestEvent(body string) (*httpx.Event, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/wizard/remote/test-ssh", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{
		UserID: "owner-1",
		OrgID:  "tenant-1",
	}))
	event := &httpx.Event{
		Request:  req,
		Response: rec,
		Auth:     &httpx.Principal{Id: "owner-1"},
	}
	return event, rec
}
