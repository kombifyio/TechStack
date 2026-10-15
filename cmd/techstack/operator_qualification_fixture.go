package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
)

const operatorQualificationFixtureGate = "TECHSTACK_OPERATOR_QUALIFICATION_FIXTURE"

var qualificationRegionPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{0,62}$`)

var errOperatorQualificationPrerequisite = errors.New("operator qualification requires its assigned managed test runtime and verified shared identity")

type operatorQualificationFixtureStore interface {
	GetMembership(context.Context, string, string) (*controlplane.Membership, error)
	GetOrCreateHomelabForOwner(context.Context, controlplane.CreateHomelabRequest) (*controlplane.Homelab, error)
	CreateStack(context.Context, controlplane.CreateStackRequest) (*controlplane.Stack, error)
	GetStack(context.Context, string, string) (*controlplane.Stack, error)
}

type operatorQualificationFixture struct {
	Attempt  string
	TenantID string
	Provider string
	Region   string
	// Supplied only by the verified shared Auth0 assignment, never a CLI argument.
	OwnerSubjectID string
}

type operatorQualificationFixtureResult struct {
	SchemaVersion  string `json:"schemaVersion"`
	AttemptID      string `json:"attemptId"`
	TenantID       string `json:"tenantId"`
	OwnerSubjectID string `json:"ownerSubjectId"`
	HomelabID      string `json:"homelabId"`
	StackID        string `json:"stackId"`
	IdempotencyKey string `json:"idempotencyKey"`
	ProvisionPath  string `json:"provisionPath"`
	AutoDeploy     bool   `json:"autoDeploy"`
}

func runOperatorQualificationFixture(ctx context.Context, args []string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	request, err := parseOperatorQualificationFixture(args, os.Getenv)
	if err != nil {
		return err
	}
	assignment, err := db.LoadManagedTestAssignment()
	if err != nil {
		return fmt.Errorf("%w: %v", errOperatorQualificationPrerequisite, err)
	}
	if request.TenantID != assignment.TenantID {
		return fmt.Errorf("%w: tenant differs from the assignment", errOperatorQualificationPrerequisite)
	}
	if err := assignment.VerifyPin(); err != nil {
		return fmt.Errorf("%w: %v", errOperatorQualificationPrerequisite, err)
	}
	claims, err := verifyManagedTestIdentity(ctx, *assignment, os.Getenv("TECHSTACK_MANAGED_TEST_ID_TOKEN"))
	if err != nil {
		return fmt.Errorf("%w: %v", errOperatorQualificationPrerequisite, err)
	}
	request.OwnerSubjectID = claims.Subject
	cfg, err := db.ManagedTestConfig(*assignment, os.Getenv("DATABASE_URL"), db.ManagedTestApp)
	if err != nil {
		return fmt.Errorf("%w: %v", errOperatorQualificationPrerequisite, err)
	}
	database, err := db.OpenContext(ctx, cfg)
	if err != nil {
		return fmt.Errorf("open qualification database: %w", err)
	}
	defer func() { _ = database.Close() }()
	if err := db.VerifyManagedTestSession(ctx, database.DB, *assignment, db.ManagedTestApp); err != nil {
		return fmt.Errorf("verify qualification app session: %w", err)
	}
	if err := database.VerifyMigrations(ctx); err != nil {
		return fmt.Errorf("verify qualification migration ledger: %w", err)
	}
	result, err := seedOperatorQualificationFixture(ctx, controlplane.NewPostgresStore(database.DB), request)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

func parseOperatorQualificationFixture(args []string, getenv func(string) string) (operatorQualificationFixture, error) {
	if len(args) != 5 || args[0] != "seed-standalone-cloud" {
		return operatorQualificationFixture{}, fmt.Errorf("usage: techstack operator-qualification-fixture seed-standalone-cloud <attempt-uuid> <tenant-id> <provider> <region>")
	}
	if strings.TrimSpace(getenv(operatorQualificationFixtureGate)) != "1" {
		return operatorQualificationFixture{}, fmt.Errorf("operator qualification fixture is disabled")
	}
	if strings.ToLower(strings.TrimSpace(getenv("TECHSTACK_ENV"))) != "local" {
		return operatorQualificationFixture{}, fmt.Errorf("operator qualification fixture requires TECHSTACK_ENV=local")
	}
	attempt, err := uuid.Parse(args[1])
	if err != nil || attempt == uuid.Nil {
		return operatorQualificationFixture{}, fmt.Errorf("operator qualification fixture requires a non-zero attempt UUID")
	}
	tenantID := strings.TrimSpace(args[2])
	if tenantID == "" || tenantID != strings.TrimSpace(getenv("TECHSTACK_V2_DEFAULT_TENANT_ID")) {
		return operatorQualificationFixture{}, fmt.Errorf("operator qualification tenant must equal TECHSTACK_V2_DEFAULT_TENANT_ID")
	}
	provider := strings.ToLower(strings.TrimSpace(args[3]))
	if provider != monthlyruntime.ProviderIONOS && provider != monthlyruntime.ProviderCentron {
		return operatorQualificationFixture{}, fmt.Errorf("operator qualification provider must be ionos or centron")
	}
	region := strings.ToLower(strings.TrimSpace(args[4]))
	if !qualificationRegionPattern.MatchString(region) {
		return operatorQualificationFixture{}, fmt.Errorf("operator qualification region is invalid")
	}
	return operatorQualificationFixture{Attempt: attempt.String(), TenantID: tenantID, Provider: provider, Region: region}, nil
}

func seedOperatorQualificationFixture(ctx context.Context, store operatorQualificationFixtureStore, request operatorQualificationFixture) (operatorQualificationFixtureResult, error) {
	if store == nil {
		return operatorQualificationFixtureResult{}, fmt.Errorf("operator qualification fixture store is required")
	}
	if request.OwnerSubjectID == "" {
		return operatorQualificationFixtureResult{}, errOperatorQualificationPrerequisite
	}
	membership, err := store.GetMembership(ctx, request.TenantID, request.OwnerSubjectID)
	if err != nil || membership == nil || membership.TenantID != request.TenantID ||
		membership.SubjectID != request.OwnerSubjectID || membership.UserID != request.OwnerSubjectID ||
		membership.ProviderKey != "cloud" || membership.Status != "active" {
		return operatorQualificationFixtureResult{}, fmt.Errorf("%w: existing active shared identity membership is required", errOperatorQualificationPrerequisite)
	}
	namespace := uuid.MustParse(request.Attempt)
	ownerID := request.OwnerSubjectID
	homelabID := uuid.NewSHA1(namespace, []byte("homelab")).String()
	stackID := uuid.NewSHA1(namespace, []byte("stack")).String()
	stackkitID := uuid.NewSHA1(namespace, []byte("stackkit")).String()
	name := "cloud-qualification-" + strings.Split(request.Attempt, "-")[0]
	config := operatorQualificationStackConfig(request, name, stackkitID)
	userConfig := config["user_config"].(map[string]any)
	allocation, err := jobs.PrimaryManagedLeaseRequestFromUIConfig(userConfig, stackID, name, request.TenantID, ownerID)
	if err != nil {
		return operatorQualificationFixtureResult{}, fmt.Errorf("qualification allocation intent is not canonical: %w", err)
	}
	if allocation.StackID != stackID || allocation.TenantID != request.TenantID ||
		allocation.OwnerID != ownerID || allocation.Provider != request.Provider || allocation.StackKit != "cloud-kit" {
		return operatorQualificationFixtureResult{}, fmt.Errorf("qualification allocation intent binding mismatch")
	}
	homelab, err := store.GetOrCreateHomelabForOwner(ctx, controlplane.CreateHomelabRequest{
		ID: homelabID, TenantID: request.TenantID, OwnerSubjectID: ownerID, Name: name,
		Intent: map[string]any{"qualification_attempt": request.Attempt, "kit": "cloud-kit", "provider_id": request.Provider},
	})
	if err != nil {
		return operatorQualificationFixtureResult{}, fmt.Errorf("seed qualification homelab: %w", err)
	}
	if homelab == nil || homelab.TenantID != request.TenantID || homelab.OwnerSubjectID != ownerID {
		return operatorQualificationFixtureResult{}, fmt.Errorf("qualification homelab ownership differs")
	}
	stack, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID: stackID, TenantID: request.TenantID, OwnerSubjectID: ownerID, HomelabID: homelab.ID,
		StackKitInstanceID: stackkitID, Name: name, Description: "Operator-only standalone Cloud qualification",
		Mode: "easy", Status: "draft", Config: config,
	})
	if errors.Is(err, controlplane.ErrConflict) || errors.Is(err, controlplane.ErrStackKitInstanceConflict) {
		stack, err = store.GetStack(ctx, request.TenantID, stackID)
		if err == nil && (stack.OwnerSubjectID != ownerID || stack.HomelabID != homelab.ID || !reflect.DeepEqual(stack.Config, config)) {
			return operatorQualificationFixtureResult{}, fmt.Errorf("qualification fixture replay conflicts with existing stack")
		}
	}
	if err != nil {
		return operatorQualificationFixtureResult{}, fmt.Errorf("seed qualification stack: %w", err)
	}
	return operatorQualificationFixtureResult{
		SchemaVersion: "techstack.operator-qualification-fixture/v1", AttemptID: request.Attempt,
		TenantID: request.TenantID, OwnerSubjectID: ownerID, HomelabID: homelab.ID, StackID: stack.ID,
		IdempotencyKey: "standalone-cloud-" + request.Attempt, ProvisionPath: "/api/v1/stacks/" + stack.ID + "/provision",
		AutoDeploy: false,
	}, nil
}

func operatorQualificationStackConfig(request operatorQualificationFixture, name, stackkitID string) map[string]any {
	// Photos needs 6 GiB. IONOS standard has 4 GiB, while Centron standard
	// already has 8 GiB in the governed catalog.
	offering := "monthly-runtime-standard"
	if request.Provider == "ionos" {
		offering = "monthly-runtime-premium"
	}
	userConfig := map[string]any{
		"name": name, "mode": "easy", "stackkit": "cloud-kit", "kit": "cloud-kit",
		"provider_id": request.Provider, "provider_region": request.Region,
		"server_provisioning_mode": "kombify-cloud", "server_mode": "monthly-runtime",
		"runtime_lane": "monthly-runtime", "runtime_offering_id": offering,
		"services": []any{"photos", "files", "vault"},
	}
	return map[string]any{
		"qualification_fixture": map[string]any{"schemaVersion": "techstack.operator-qualification-intent/v1", "attemptId": request.Attempt},
		"stackkit_instance_id":  stackkitID,
		"user_config":           userConfig,
	}
}
