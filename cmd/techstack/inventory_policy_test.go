package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	commonfga "github.com/kombifyio/go-common/fga"

	"github.com/kombifyio/techstack/internal/routes"
	"github.com/kombifyio/techstack/pkg/config"
)

type inventoryPolicyTestChecker struct {
	allowed     bool
	legacyCalls int
	strictCalls int
	strictErr   error
}

func (c *inventoryPolicyTestChecker) Check(context.Context, string, string, string) (bool, error) {
	c.legacyCalls++
	return c.allowed, nil
}

func (c *inventoryPolicyTestChecker) CheckStrict(context.Context, string, string, string) (bool, error) {
	c.strictCalls++
	return c.allowed, c.strictErr
}

func TestInventoryPolicyForDeploymentKeepsSelfHostedOwnerScopedWithoutFGA(t *testing.T) {
	factoryCalled := false
	policy, err := inventoryPolicyForDeployment(config.ModeSelfHosted, func() (routes.InventoryRelationshipChecker, error) {
		factoryCalled = true
		return nil, errors.New("must not be called")
	})
	if err != nil {
		t.Fatalf("self-hosted policy: %v", err)
	}
	if factoryCalled {
		t.Fatal("self-hosted policy initialized FGA")
	}

	authorization := routes.InventoryAuthorization{
		TenantID: "tenant-1", SubjectID: "owner-1",
		ResourceType: "server_collection", Action: routes.InventoryActionRead,
	}
	decision, err := policy.AuthorizeInventory(t.Context(), authorization)
	if err != nil {
		t.Fatalf("owner-scoped self-hosted read: %v", err)
	}
	if !decision.ReadScope.IsOwnerScoped() || decision.ReadScope.OwnerSubjectID() != authorization.SubjectID {
		t.Fatalf("self-hosted decision = %#v, want authenticated-owner scope", decision.ReadScope)
	}
	authorization.SubjectID = ""
	if _, err := policy.AuthorizeInventory(t.Context(), authorization); !errors.Is(err, routes.ErrInventoryAccessDenied) {
		t.Fatalf("missing subject error = %v, want access denied", err)
	}
}

func TestInventoryPolicyForDeploymentFailsSaaSClosedOnFactoryErrors(t *testing.T) {
	sentinel := errors.New("invalid FGA configuration")
	policy, err := inventoryPolicyForDeployment(config.ModeSaaS, func() (routes.InventoryRelationshipChecker, error) {
		return nil, sentinel
	})
	if policy != nil {
		t.Fatalf("policy = %#v, want nil", policy)
	}
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "SaaS inventory FGA is required") {
		t.Fatalf("error = %v, want clear wrapped SaaS FGA failure", err)
	}

	policy, err = inventoryPolicyForDeployment(config.ModeSaaS, func() (routes.InventoryRelationshipChecker, error) {
		return nil, nil
	})
	if policy != nil || err == nil {
		t.Fatalf("nil checker result = (%#v, %v), want startup error", policy, err)
	}
}

func TestBuildRouterFailsStartupWhenSaaSFGAIsUnconfigured(t *testing.T) {
	for _, name := range []string{
		"AUTH0_FGA_API_HOST", "AUTH0_FGA_STORE_ID", "AUTH0_FGA_MODEL_ID",
		"AUTH0_FGA_CLIENT_ID", "AUTH0_FGA_CLIENT_SECRET", "AUTH0_FGA_API_TOKEN_ISSUER", "AUTH0_FGA_API_AUDIENCE",
		"FGA_API_URL", "FGA_STORE_ID", "FGA_MODEL_ID", "FGA_CLIENT_ID", "FGA_CLIENT_SECRET", "FGA_API_TOKEN_ISSUER", "FGA_API_AUDIENCE",
	} {
		t.Setenv(name, "")
	}

	cfg := config.DefaultConfig()
	cfg.DeploymentMode = config.ModeSaaS
	router, err := buildRouter(routeDeps{startup: &startupContext{cfg: cfg}})
	if router != nil {
		t.Fatalf("router = %#v, want nil", router)
	}
	if !errors.Is(err, errInventoryFGAModelUnpinned) || !strings.Contains(err.Error(), "initialize inventory authorization") {
		t.Fatalf("buildRouter error = %v, want exact-model startup failure", err)
	}

	t.Setenv("AUTH0_FGA_MODEL_ID", "model-inventory-v1")
	router, err = buildRouter(routeDeps{startup: &startupContext{cfg: cfg}})
	if router != nil {
		t.Fatalf("router = %#v, want nil", router)
	}
	if !errors.Is(err, commonfga.ErrNotConfigured) || !strings.Contains(err.Error(), "initialize inventory authorization") {
		t.Fatalf("buildRouter error = %v, want store/API startup failure", err)
	}
}

func TestInventoryPolicyForDeploymentRejectsUnknownMode(t *testing.T) {
	policy, err := inventoryPolicyForDeployment(config.DeploymentMode("preview-ish"), func() (routes.InventoryRelationshipChecker, error) {
		return &inventoryPolicyTestChecker{allowed: true}, nil
	})
	if policy != nil || err == nil {
		t.Fatalf("unknown mode result = (%#v, %v), want error", policy, err)
	}
}

func TestPreferStrictInventoryRelationshipCheckerPreservesBackendErrors(t *testing.T) {
	sentinel := errors.New("backend unavailable")
	checker := &inventoryPolicyTestChecker{allowed: false, strictErr: sentinel}
	selected := preferStrictInventoryRelationshipChecker(checker)
	allowed, err := selected.Check(t.Context(), "user:owner-1", "can_read", "server:tenant-1/server-1")
	if allowed || !errors.Is(err, sentinel) {
		t.Fatalf("strict check = (%v, %v), want preserved error", allowed, err)
	}
	if checker.strictCalls != 1 || checker.legacyCalls != 0 {
		t.Fatalf("calls = strict:%d legacy:%d, want strict only", checker.strictCalls, checker.legacyCalls)
	}
}
