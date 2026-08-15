package vmleases

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kombifyio/go-common/servicecall"
)

const testServicecallFixture = "auth-secret"

func cloudToken(t *testing.T, subjectID, tenantID string) string {
	t.Helper()
	token, err := servicecall.IssueToken(
		servicecall.Config{ServiceName: serviceCallerCloud, Secret: testServicecallFixture, TokenTTL: time.Minute},
		"techstack",
		&servicecall.OnBehalfOf{Sub: subjectID, OrgID: tenantID},
		"req-1",
	)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	return token
}

func TestHTTPStatusForGenerationConflicts(t *testing.T) {
	for _, err := range []error{ErrResourceGenerationSuperseded, errors.Join(errors.New("wrapped"), ErrDecommissionClaimImmutable)} {
		if got := httpStatusForError(err); got != http.StatusConflict {
			t.Fatalf("httpStatusForError(%v) = %d", err, got)
		}
	}
}

func TestHandlerRequiresConfiguredServiceAuth(t *testing.T) {
	handler := NewHandler(NewService(NewMemoryStore(), ServiceConfig{}), HandlerConfig{})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/internal/vm-leases/lease-1", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

func TestHandlerCloudReadsExistingInventoryAndRejectsCreate(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	token := cloudToken(t, "user-1", "org-1")
	service := NewService(store, inventoryTestServiceConfig(func() time.Time { return now }))
	_, err := service.CreateOrUpdate(t.Context(), CreateRequest{Lease: testLease(now), IdempotencyKey: "inventory-1"})
	if err != nil {
		t.Fatalf("seed inventory: %v", err)
	}
	handler := NewHandler(service, HandlerConfig{ServiceAuthSecret: testServicecallFixture})
	create := httptest.NewRequest(http.MethodPost, "/api/v1/internal/vm-leases", nil)
	create.Header.Set(servicecall.HeaderServiceAuth, token)
	createRR := httptest.NewRecorder()
	handler.ServeHTTP(createRR, create)
	if createRR.Code != http.StatusForbidden {
		t.Fatalf("disabled create status = %d body=%s", createRR.Code, createRR.Body.String())
	}

	get := httptest.NewRequest(http.MethodGet, "/api/v1/internal/vm-leases/lease-1", nil)
	get.Header.Set(servicecall.HeaderServiceAuth, token)
	getRR := httptest.NewRecorder()
	handler.ServeHTTP(getRR, get)
	if getRR.Code != http.StatusOK {
		t.Fatalf("get status = %d body=%s", getRR.Code, getRR.Body.String())
	}
	if len(store.journal) != 0 {
		t.Fatalf("inventory surface emitted execution journal: %+v", store.journal)
	}
}

func TestHandlerRejectsLegacySimulateCaller(t *testing.T) {
	handler := NewHandler(NewService(NewMemoryStore(), ServiceConfig{}), HandlerConfig{ServiceAuthSecret: testServicecallFixture})
	token, err := servicecall.IssueToken(servicecall.Config{ServiceName: "simulate", Secret: testServicecallFixture, TokenTTL: time.Minute}, "techstack", nil, "req-legacy")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/vm-leases/lease-1", nil)
	req.Header.Set(servicecall.HeaderServiceAuth, token)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("legacy caller status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandlerRejectsEveryDeletedLegacySurface(t *testing.T) {
	handler := NewHandler(NewService(NewMemoryStore(), ServiceConfig{}), HandlerConfig{ServiceAuthSecret: testServicecallFixture})
	token := cloudToken(t, "user-1", "org-1")
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPatch, "/api/v1/internal/vm-leases/lease-1"},
		{http.MethodPost, "/api/v1/internal/vm-leases/lease-1/validate"},
		{http.MethodGet, "/api/v1/internal/vm-leases/lease-1/desired-spec"},
		{http.MethodPost, "/api/v1/internal/vm-leases/lease-1/executor-status"},
		{http.MethodPost, "/api/v1/internal/vm-leases/lease-1/executor-commands/provision"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set(servicecall.HeaderServiceAuth, token)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}
