package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	commonauthlocal "github.com/kombifyio/techstack/internal/gocommon/authlocal"
	"github.com/kombifyio/techstack/internal/gocommon/authsession"
	"github.com/kombifyio/techstack/internal/routes"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

func TestLocalWalletRevealRequiresCanonicalOwnerReauth(t *testing.T) {
	ctx := context.Background()
	manager, err := authsession.NewManager(authsession.Config{
		Audience: "wallet-local-test", Secret: []byte(strings.Repeat("s", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := commonauthlocal.New(commonauthlocal.Config{
		Store: &memoryBreakglassStore{}, Sessions: manager, DefaultTenantID: "default",
	})
	if err != nil {
		t.Fatal(err)
	}
	bootstrapPassword, err := service.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const ownerPassword = "local-owner-current-password"
	if err := service.Claim(ctx, commonauthlocal.ClaimRequest{
		CurrentPassword: bootstrapPassword, NewEmail: "owner@selfhost.invalid", NewPassword: ownerPassword,
	}); err != nil {
		t.Fatal(err)
	}
	ownerClaims, err := service.Authenticate(ctx, "owner@selfhost.invalid", ownerPassword)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name            string
		mode            config.DeploymentMode
		password        string
		mutate          func(*authsession.Claims)
		projectedTenant string
		allow           bool
	}{
		{name: "local owner", mode: config.ModeSelfHosted, password: ownerPassword, allow: true},
		{name: "wrong password", mode: config.ModeSelfHosted, password: "wrong-owner-password"},
		{name: "missing password", mode: config.ModeSelfHosted},
		{name: "different subject", mode: config.ModeSelfHosted, password: ownerPassword,
			mutate: func(c *authsession.Claims) { c.Subject = "another-owner" }},
		{name: "different tenant", mode: config.ModeSelfHosted, password: ownerPassword,
			mutate: func(c *authsession.Claims) { c.TenantID = "another-tenant" }},
		{name: "reprojected request tenant", mode: config.ModeSelfHosted, password: ownerPassword, projectedTenant: "reprojected-tenant"},
		{name: "cloud provider", mode: config.ModeSelfHosted, password: ownerPassword,
			mutate: func(c *authsession.Claims) { c.Provider = "cloud" }},
		{name: "hosted password cannot replace platform proof", mode: config.ModeSaaS, password: ownerPassword},
	} {
		t.Run(test.name, func(t *testing.T) {
			claims := ownerClaims
			if test.mutate != nil {
				test.mutate(&claims)
			}
			store := controlplane.NewMemoryStore()
			effectiveTenant := claims.TenantID
			if test.projectedTenant != "" {
				effectiveTenant = test.projectedTenant
			}
			const recoveredSecret = "retained-wallet-recovery-credential"
			item, err := store.UpsertWalletItem(ctx, controlplane.WalletItem{
				ID: "wallet-local-test", TenantID: effectiveTenant, ItemType: "password",
				Metadata: map[string]any{"owner_id": claims.Subject, "secret": recoveredSecret, "revealable": true},
			})
			if err != nil {
				t.Fatal(err)
			}
			router := httpx.NewRouter()
			router.BindFunc(func(e *httpx.Event) error {
				requestCtx := authsession.WithClaims(e.Request.Context(), &claims)
				requestCtx = identity.NewContext(requestCtx, &identity.Identity{
					UserID: claims.Subject, OrgID: effectiveTenant, Email: claims.Email,
				})
				e.Request = e.Request.WithContext(requestCtx)
				return e.Next()
			})
			routes.RegisterWalletRoutesWithConfig(router, routes.WalletRouteConfig{
				Store: store, Activity: store, LocalReauth: newWalletLocalReauth(test.mode, service),
			})
			body, err := json.Marshal(map[string]string{"reason": "local recovery", "current_password": test.password})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/api/v1/wallet/"+item.ID+"/reveal", strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.BuildMux().ServeHTTP(recorder, request)
			wantStatus, wantAudit := http.StatusForbidden, "wallet_reveal_denied"
			if test.allow {
				wantStatus, wantAudit = http.StatusOK, "wallet_reveal"
			}
			if recorder.Code != wantStatus {
				t.Fatalf("HTTP %d, want %d: %s", recorder.Code, wantStatus, recorder.Body.String())
			}
			if test.allow {
				var response struct {
					Data struct {
						Secret string `json:"secret"`
					} `json:"data"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.Data.Secret != recoveredSecret {
					t.Fatalf("authorized recovery failed: %v", err)
				}
			} else if strings.Contains(recorder.Body.String(), recoveredSecret) {
				t.Fatal("denied request disclosed the recovery credential")
			}
			activity, err := store.ListActivity(ctx, effectiveTenant, "", 20)
			if err != nil {
				t.Fatal(err)
			}
			audited := false
			for _, event := range activity {
				if event.Action == wantAudit {
					audited = true
				}
			}
			if !audited {
				t.Fatalf("recovery outcome was not audited: %s", wantAudit)
			}
		})
	}
}
