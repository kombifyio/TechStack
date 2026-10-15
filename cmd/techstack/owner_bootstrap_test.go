package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/authlocal"
	"github.com/kombifyio/techstack/internal/gocommon/authsession"
	"github.com/kombifyio/techstack/internal/routes"
	"github.com/kombifyio/techstack/internal/routes/auth"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/pocketbase/pocketbase/tests"
)

// This is the production provisioner, exercised through the public setup and
// login handlers. The bootstrap proof comes from the host-local operator seam.
func TestOperatorBootstrapCompletesPublicOwnerSetup(t *testing.T) {
	for _, scenario := range []string{"expired", "consumed", "missing marker"} {
		t.Run(scenario, func(t *testing.T) {
			app, err := tests.NewTestApp()
			if err != nil {
				t.Fatal(err)
			}
			defer app.Cleanup()
			store := &memoryBreakglassStore{}
			manager, err := authsession.NewManager(authsession.Config{Audience: "operator-owner-test", Secret: []byte(strings.Repeat("s", 32))})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			service, err := authlocal.New(authlocal.Config{Store: store, Sessions: manager, BootstrapEmail: "breakglass@techstack.local", DefaultTenantID: "owner-fixture", Now: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			original, err := service.Bootstrap(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			before, err := service.Authenticate(t.Context(), "breakglass@techstack.local", original)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "expired" {
				now = now.Add(2 * authlocal.PasswordEnvelopeTTL)
			} else {
				if _, err = service.Reveal(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			proof, replaced, err := localOperatorBootstrapProof(t.Context(), service)
			if err != nil || !replaced {
				t.Fatalf("operator recovery failed: %v", err)
			}
			if _, err = service.Authenticate(t.Context(), "breakglass@techstack.local", original); !errors.Is(err, authlocal.ErrInvalidCreds) {
				t.Fatal("old proof still authenticates")
			}
			persistence := &setupMarkerStore{}
			runtime := auth.RuntimeStore{Persistence: persistence, Identities: ownerProjection{}, TenantID: "owner-fixture", InstanceID: "owner-instance"}
			router := httpx.NewRouter()
			routes.RegisterAuthRoutesWithConfig(router, app, config.ModeSelfHosted, config.EditionSelfHostOSS, routes.AuthRouteConfig{Runtime: runtime, LocalOwnerStore: store, LocalSetupProvisioner: localSetupProvisionerWithService(service)})
			router.POST("/api/v2/auth/local/login", func(e *httpx.Event) error {
				authlocal.NewHandlers(service, nil).LoginHandler().ServeHTTP(e.Response, e.Request)
				return nil
			})
			post := func(path, email, password, current string) *httptest.ResponseRecorder {
				t.Helper()
				body, encodeErr := json.Marshal(map[string]string{"mode": "local", "name": "Owner", "email": email, "password": password, "current_password": current})
				if encodeErr != nil {
					t.Fatal(encodeErr)
				}
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
				return response
			}
			for _, bad := range []string{"", "invalid-bootstrap-proof"} {
				if bad == "" {
					cloudChoice := httptest.NewRecorder()
					router.ServeHTTP(cloudChoice, httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", strings.NewReader(`{"mode":"cloud"}`)))
					if cloudChoice.Code != http.StatusOK {
						t.Fatal("legacy cloud navigation failed")
					}
					mode := httptest.NewRecorder()
					router.ServeHTTP(mode, httptest.NewRequest(http.MethodGet, "/api/v1/auth/mode", nil))
					var discovery struct {
						Data struct {
							IsFirstRun bool `json:"is_first_run"`
						} `json:"data"`
					}
					if err = json.NewDecoder(mode.Body).Decode(&discovery); err != nil || !discovery.Data.IsFirstRun {
						t.Fatal("anonymous cloud navigation stranded local setup")
					}
				}
				if response := post("/api/v1/auth/setup", "owner@example.test", "chosen-owner-password", bad); response.Code != http.StatusUnauthorized {
					t.Fatalf("bad proof admitted: %d", response.Code)
				}
			}
			persistence.failNext = scenario == "missing marker"
			response := post("/api/v1/auth/setup", "owner@example.test", "chosen-owner-password", proof)
			if scenario == "missing marker" {
				if response.Code != http.StatusInternalServerError {
					t.Fatalf("marker fault not observed: %d", response.Code)
				}
				if wrongRetry := post("/api/v1/auth/setup", "intruder@example.test", "different-password", proof); wrongRetry.Code != http.StatusUnauthorized {
					t.Fatal("marker retry changed owner")
				}
				response = post("/api/v1/auth/setup", "owner@example.test", "chosen-owner-password", "")
			}
			if response.Code != http.StatusCreated {
				t.Fatalf("setup failed: %d", response.Code)
			}
			login := post("/api/v2/auth/local/login", "owner@example.test", "chosen-owner-password", "")
			if login.Code != http.StatusOK {
				t.Fatalf("chosen login failed: %d", login.Code)
			}
			token := ""
			for _, cookie := range login.Result().Cookies() {
				if cookie.Name == service.CookieName() {
					token = cookie.Value
				}
			}
			owner, err := manager.Verify(token)
			if err != nil {
				t.Fatal(err)
			}
			if owner.Subject != before.Subject || owner.TenantID != before.TenantID {
				t.Fatal("setup changed canonical identity")
			}
			if repeated := post("/api/v1/auth/setup", "intruder@example.test", "different-password", proof); repeated.Code != http.StatusForbidden {
				t.Fatal("completed setup reopened")
			}
		})
	}
}

// setupMarkerStore is the canonical auth_config marker seam; failNext injects
// one marker write fault after the local owner claim has been accepted.
type setupMarkerStore struct {
	controlplane.AuthRuntimeStore
	config   *controlplane.AuthConfig
	failNext bool
}

func (s *setupMarkerStore) GetAuthConfig(context.Context, string, string) (*controlplane.AuthConfig, error) {
	if s.config == nil {
		return nil, controlplane.ErrNotFound
	}
	return s.config, nil
}

func (s *setupMarkerStore) UpsertAuthConfig(_ context.Context, value controlplane.AuthConfig) (*controlplane.AuthConfig, error) {
	if s.failNext {
		s.failNext = false
		return nil, errors.New("injected marker failure")
	}
	s.config = &value
	return s.config, nil
}

// ownerProjection accepts the canonical owner user and membership projection.
type ownerProjection struct{ controlplane.AuthStore }

func (ownerProjection) UpsertUser(_ context.Context, user controlplane.User) (*controlplane.User, error) {
	return &user, nil
}

func (ownerProjection) UpsertMembership(_ context.Context, membership controlplane.Membership) (*controlplane.Membership, error) {
	return &membership, nil
}
