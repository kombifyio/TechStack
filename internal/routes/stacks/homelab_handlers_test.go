package stacks

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
)

type homelabEnvelope struct {
	Data struct {
		Homelab        map[string]any   `json:"homelab"`
		KitDeployments []map[string]any `json:"kit_deployments"`
	} `json:"data"`
}

func TestGetHomelabReturnsUmbrellaWithKitDeployments(t *testing.T) {
	store := controlplane.NewMemoryStore()
	ctx := context.Background()
	if _, err := store.CreateHomelab(ctx, controlplane.CreateHomelabRequest{
		ID:             "hl-1",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "My Homelab",
		Intent:         map[string]any{"goals": []any{"photos"}},
	}); err != nil {
		t.Fatalf("CreateHomelab: %v", err)
	}
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-1",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "basement",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}
	// Another owner's stack must never leak into the caller's homelab.
	if _, err := store.CreateStack(ctx, controlplane.CreateStackRequest{
		ID:             "stack-other",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-2",
		Name:           "foreign",
	}); err != nil {
		t.Fatalf("CreateStack (other owner): %v", err)
	}

	h := crudRouteHandlers{stackStore: store, homelabStore: store}
	event, rec := stackStoreRequestEvent("auth0|user-1", "tenant-1")

	if err := h.getHomelab(event); err != nil {
		t.Fatalf("getHomelab: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var envelope homelabEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if envelope.Data.Homelab == nil || envelope.Data.Homelab["id"] != "hl-1" {
		t.Fatalf("homelab payload = %#v, want id hl-1", envelope.Data.Homelab)
	}
	if len(envelope.Data.KitDeployments) != 1 || envelope.Data.KitDeployments[0]["id"] != "stack-1" {
		t.Fatalf("kit_deployments = %#v, want exactly stack-1", envelope.Data.KitDeployments)
	}
}

func TestGetHomelabWithoutRowStillListsDeployments(t *testing.T) {
	store := controlplane.NewMemoryStore()
	if _, err := store.CreateStack(context.Background(), controlplane.CreateStackRequest{
		ID:             "stack-1",
		TenantID:       "tenant-1",
		OwnerSubjectID: "auth0|user-1",
		Name:           "basement",
	}); err != nil {
		t.Fatalf("CreateStack: %v", err)
	}

	h := crudRouteHandlers{stackStore: store, homelabStore: store}
	event, rec := stackStoreRequestEvent("auth0|user-1", "tenant-1")

	if err := h.getHomelab(event); err != nil {
		t.Fatalf("getHomelab: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var envelope homelabEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if envelope.Data.Homelab != nil {
		t.Fatalf("homelab = %#v, want null before backfill/adoption", envelope.Data.Homelab)
	}
	if len(envelope.Data.KitDeployments) != 1 {
		t.Fatalf("kit_deployments = %#v, want the legacy deployment", envelope.Data.KitDeployments)
	}
}

func TestGetHomelabWithoutAnythingReturnsGuidedNotFound(t *testing.T) {
	store := controlplane.NewMemoryStore()
	h := crudRouteHandlers{stackStore: store, homelabStore: store}
	event, rec := stackStoreRequestEvent("auth0|user-1", "tenant-1")

	if err := h.getHomelab(event); err != nil {
		t.Fatalf("getHomelab: %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}

	var errEnvelope struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errEnvelope); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}
	if errEnvelope.Error.Code != "NOT_FOUND" {
		t.Fatalf("error code = %q, want NOT_FOUND", errEnvelope.Error.Code)
	}
	if errEnvelope.Error.Details["reason_code"] != "homelab_not_found" {
		t.Fatalf("details = %#v, want reason_code homelab_not_found", errEnvelope.Error.Details)
	}
	if _, ok := errEnvelope.Error.Details["user_guidance"]; !ok {
		t.Fatalf("details = %#v, want user_guidance", errEnvelope.Error.Details)
	}
}
