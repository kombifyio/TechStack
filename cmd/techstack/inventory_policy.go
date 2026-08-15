package main

import (
	"context"
	"errors"
	"fmt"

	commonfga "github.com/kombifyio/go-common/fga"

	"github.com/kombifyio/techstack/internal/routes"
	"github.com/kombifyio/techstack/pkg/config"
)

var errInventoryFGAModelUnpinned = errors.New("SaaS inventory FGA authorization model id is required")

type inventoryFGACheckerFactory func() (routes.InventoryRelationshipChecker, error)

type strictInventoryRelationshipChecker interface {
	CheckStrict(context.Context, string, string, string) (bool, error)
}

type strictInventoryRelationshipCheckerAdapter struct {
	checker strictInventoryRelationshipChecker
}

func (a strictInventoryRelationshipCheckerAdapter) Check(ctx context.Context, user, relation, object string) (bool, error) {
	return a.checker.CheckStrict(ctx, user, relation, object)
}

func inventoryPolicyFromEnvironment(mode config.DeploymentMode) (routes.InventoryPolicy, error) {
	if mode.IsSaaS() && v2FirstEnv("AUTH0_FGA_MODEL_ID", "FGA_MODEL_ID") == "" {
		return nil, fmt.Errorf("%w (configure AUTH0_FGA_MODEL_ID/FGA_MODEL_ID; latest-model fallback is forbidden)", errInventoryFGAModelUnpinned)
	}
	return inventoryPolicyForDeployment(mode, func() (routes.InventoryRelationshipChecker, error) {
		checker, err := commonfga.FromEnv()
		if err != nil {
			return nil, err
		}
		return preferStrictInventoryRelationshipChecker(checker), nil
	})
}

// preferStrictInventoryRelationshipChecker is the release-pin boundary for the
// error-preserving common FGA API. Common versions without CheckStrict retain
// their fail-closed deny behavior; versions with it preserve backend errors.
// Consumers activate the latter through a tagged dependency, never a local
// module replacement or a second FGA implementation.
func preferStrictInventoryRelationshipChecker(checker routes.InventoryRelationshipChecker) routes.InventoryRelationshipChecker {
	strict, ok := checker.(strictInventoryRelationshipChecker)
	if !ok {
		return checker
	}
	return strictInventoryRelationshipCheckerAdapter{checker: strict}
}

func inventoryPolicyForDeployment(mode config.DeploymentMode, newChecker inventoryFGACheckerFactory) (routes.InventoryPolicy, error) {
	switch mode {
	case config.ModeSelfHosted:
		return routes.NewSelfHostedInventoryPolicy(), nil
	case config.ModeSaaS:
		if newChecker == nil {
			return nil, fmt.Errorf("SaaS inventory FGA checker factory is required")
		}
		checker, err := newChecker()
		if err != nil {
			return nil, fmt.Errorf("SaaS inventory FGA is required (configure AUTH0_FGA_API_HOST/FGA_API_URL, AUTH0_FGA_STORE_ID/FGA_STORE_ID, and an exact AUTH0_FGA_MODEL_ID/FGA_MODEL_ID): %w", err)
		}
		if checker == nil {
			return nil, fmt.Errorf("SaaS inventory FGA is required: checker factory returned nil")
		}
		return routes.NewInventoryFGAPolicy(checker), nil
	default:
		return nil, fmt.Errorf("inventory authorization: unsupported deployment mode %q", mode)
	}
}
