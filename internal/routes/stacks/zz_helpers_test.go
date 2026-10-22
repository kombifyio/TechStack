package stacks

// Helpers of retired unit tests that the remaining boundary tests of this package still use.

import (
	"bytes"
	"github.com/kombifyio/techstack/pkg/config"
	"context"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"fmt"
	"net/http"
	"net/http/httptest"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"encoding/json"
	"github.com/kombifyio/techstack/pkg/specv2"
	"testing"
	"github.com/kombifyio/techstack/internal/routes/trust"
)

func stackStoreRequestEvent(userID, orgID string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/stacks", nil)
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{
		UserID: userID,
		OrgID:  orgID,
	}))
	rec := httptest.NewRecorder()
	return &httpx.Event{
		Request:  req,
		Response: rec,
	}, rec
}

const testRecoveryPassphraseHash = "$argon2id$v=19$m=65536,t=3,p=4$demo$signed"

func ownerSpecRequestEvent(stackID, token string) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodGet, ownerSpecEndpoint(stackID), nil)
	req.SetPathValue("id", stackID)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

type wizardRunFakeFeatures struct {
	enabled bool
	err     error
}

func (f wizardRunFakeFeatures) IsEnabled(context.Context, string, string) (bool, error) {
	return f.enabled, f.err
}

type wizardRunFakeSeeds struct {
	seeds map[string]map[string]any
}

func (f wizardRunFakeSeeds) Seed(kitSlug string) (map[string]any, error) {
	seed, ok := f.seeds[kitSlug]
	if !ok {
		return nil, fmt.Errorf("no seed for %s", kitSlug)
	}
	return seed, nil
}

type wizardRunFakeValidator struct {
	err   error
	calls int
}

func (f *wizardRunFakeValidator) ValidateSpec(context.Context, map[string]any) error {
	f.calls++
	return f.err
}

type wizardRunFakeGoalAuthor struct{}

func (wizardRunFakeGoalAuthor) AuthorGoals(_ context.Context, _, _, _, _ string, selection specv2.GoalSelection) (specv2.GoalAuthoring, error) {
	goals := selection.Goals
	workloads := map[string]any{}
	var unmapped []string
	for _, goal := range goals {
		if goal != "photos" {
			unmapped = append(unmapped, goal)
			continue
		}
		workloads[goal] = map[string]any{
			"alternative": "immich",
			"placement": map[string]any{
				"siteRefs": []any{"home"}, "nodeRefs": []any{}, "requiresRoles": []any{},
			},
			"secretRefs": map[string]any{"database-password": "secret://workloads/photos/database-password"},
		}
	}
	return specv2.GoalAuthoring{Workloads: workloads, UnmappedGoals: unmapped}, nil
}

func wizardRunTestSeed(kitSlug string) map[string]any {
	return map[string]any{
		"apiVersion": "stackkit/v2alpha1",
		"kind":       "StackSpec",
		"kit":        map[string]any{"slug": kitSlug},
		"metadata":   map[string]any{"name": "seed-template"},
		"generation": map[string]any{"outputRoot": "deploy", "strategy": "kit-template", "target": "compose"},
		"network":    map[string]any{"domain": map[string]any{"base": "example.homelab"}},
		"nodes": []any{map[string]any{
			"id": "main", "roles": []any{"controller", "worker"}, "siteRef": "home", "enabled": true,
		}},
		"sites": []any{map[string]any{"id": "home", "kind": "home"}},
	}
}

func newWizardRunTestHandlers(store *controlplane.MemoryStore, validator specv2.SpecValidator) wizardRunHandlers {
	return wizardRunHandlers{
		crud: crudRouteHandlers{
			deploymentMode:  config.ModeSelfHosted,
			stackStore:      store,
			homelabStore:    store,
			jobStore:        store,
			walletStore:     store,
			serverStore:     store,
			runtimeFeatures: wizardRunFakeFeatures{enabled: true},
		},
		wizardRuns: store,
		cfg: WizardRunRouteConfig{
			DeploymentMode: config.ModeSelfHosted,
			Features:       wizardRunFakeFeatures{enabled: true},
			Seeds: wizardRunFakeSeeds{seeds: map[string]map[string]any{
				specv2.KitSlugBasement: wizardRunTestSeed(specv2.KitSlugBasement),
			}},
			Projector:      specv2.NewReleaseProjector(wizardRunFakeGoalAuthor{}),
			Validator:      validator,
			ReleaseVersion: "v0.9.9-test",
			Trust:          trust.RouteStores{Stacks: store, Workers: store, Jobs: store},
			Wallet:         store,
		},
	}
}

func wizardRunTestEvent(t *testing.T, payload any, idempotencyKey string) (*httpx.Event, *httptest.ResponseRecorder) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/wizard/runs", bytes.NewReader(body))
	req = req.WithContext(identity.NewContext(req.Context(), &identity.Identity{
		UserID: "auth0|user-1",
		OrgID:  "tenant-1",
	}))
	if idempotencyKey != "" {
		req.Header.Set("X-Idempotency-Key", idempotencyKey)
	}
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}

func decodeWizardRunSuccess(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rec.Body.String())
	}
	return envelope.Data
}

func decodeWizardRunError(t *testing.T, rec *httptest.ResponseRecorder) (string, map[string]any) {
	t.Helper()
	var envelope struct {
		Error struct {
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error response: %v (body=%s)", err, rec.Body.String())
	}
	return envelope.Error.Message, envelope.Error.Details
}

func wizardRunSeedV2Stack(t *testing.T, store *controlplane.MemoryStore, stackID, owner string, config map[string]any) {
	t.Helper()
	if _, err := store.CreateStack(context.Background(), controlplane.CreateStackRequest{
		ID:             stackID,
		TenantID:       "tenant-1",
		OwnerSubjectID: owner,
		Name:           "my-homelab",
		Mode:           "easy",
		Status:         "running",
		Config:         config,
	}); err != nil {
		t.Fatalf("seed stack: %v", err)
	}
}

func wizardRunJoinIntent(deploymentID string) specv2.WizardIntent {
	return specv2.WizardIntent{
		Schema:  specv2.WizardIntentSchema,
		RunKind: specv2.RunKindExpansion,
		Name:    "ignored-by-join",
		Server:  specv2.ServerIntent{Roles: []string{"worker"}},
		KitAssignment: specv2.KitAssignment{
			Mode:            specv2.KitAssignmentJoin,
			KitDeploymentID: deploymentID,
		},
	}
}

