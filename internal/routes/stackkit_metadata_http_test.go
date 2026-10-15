package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/unifier"
	"github.com/kombifyio/stackkits/pkg/models"
)

// Regression: the authenticated catalog and every detail must describe the
// owning published metadata without a checkout, and broken explicit authority
// must fail both routes instead of producing substituted rows or a false 404.
func TestStackKitMetadataHTTPJourneyUsesPublishedAuthority(t *testing.T) {
	for _, name := range []string{"TECHSTACK_STACKKITS_DIR", "STACKKITS_REPO", "STACKKITS_PATH"} {
		t.Setenv(name, "")
	}
	newServer := func() *httptest.Server {
		t.Helper()
		router := httpx.NewRouter()
		router.BindFunc(func(e *httpx.Event) error {
			e.Request = e.Request.WithContext(identity.NewContext(e.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
			return e.Next()
		})
		if err := RegisterUnifierRoutes(router, nil); err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(router.BuildMux())
		t.Cleanup(server.Close)
		return server
	}
	get := func(server *httptest.Server, path string, target any) int {
		t.Helper()
		response, err := server.Client().Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if err := json.NewDecoder(response.Body).Decode(target); err != nil {
			t.Fatal(err)
		}
		return response.StatusCode
	}
	authority, err := stackkitrelease.PinnedKitMetadata()
	if err != nil {
		t.Fatal(err)
	}
	server := newServer()
	var catalog struct {
		Data []unifier.StackKitInfo `json:"data"`
	}
	if status := get(server, "/api/v1/stackkits", &catalog); status != http.StatusOK {
		t.Fatalf("catalog failed: %d", status)
	}
	listed := map[string]unifier.StackKitInfo{}
	for _, kit := range catalog.Data {
		listed[kit.Name] = kit
	}
	for _, source := range authority {
		var detail struct {
			Data unifier.StackKitInfo `json:"data"`
		}
		if status := get(server, "/api/v1/stackkits/"+source.Metadata.Name, &detail); status != http.StatusOK {
			t.Fatalf("listed kit detail failed: %s status=%d", source.Metadata.Name, status)
		}
		if !reflect.DeepEqual(detail.Data, listed[source.Metadata.Name]) {
			t.Fatal("catalog and detail disagree about the same published kit")
		}
		// Compare the public metadata envelope to the owner's typed contract,
		// excluding only owner fields this established HTTP DTO does not expose.
		encoded, err := json.Marshal(detail.Data)
		if err != nil {
			t.Fatal(err)
		}
		var actual models.KitMetadata
		if err := json.Unmarshal(encoded, &actual); err != nil {
			t.Fatal(err)
		}
		expected := source.Metadata
		expected.Summary, expected.Homepage, expected.Repository = "", "", ""
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("HTTP kit metadata differs from its published owner: %s", source.Metadata.Name)
		}
		if !reflect.DeepEqual(detail.Data.SupportedOS, source.SupportedOS) || !reflect.DeepEqual(detail.Data.Features, source.Features) {
			t.Fatal("HTTP catalog changed declared host compatibility or enabled features")
		}
		for mode, declaration := range source.Modes {
			info := detail.Data.Modes[mode]
			if info.Description != declaration.Description || info.Engine != declaration.Engine || info.TemplateDir != declaration.TemplateDir ||
				!reflect.DeepEqual(info.Requires, declaration.Requires) || !reflect.DeepEqual(info.RecommendedFor, declaration.RecommendedFor) {
				t.Fatal("HTTP catalog changed an owning mode declaration")
			}
		}
		delete(listed, source.Metadata.Name)
	}
	for name := range listed {
		t.Fatalf("catalog substituted a kit outside published authority: %s", name)
	}
	var unknown struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if status := get(server, "/api/v1/stackkits/unknown-kit", &unknown); status != http.StatusNotFound {
		t.Fatalf("unknown kit status=%d", status)
	}

	// Even a configured, existing lower-priority path cannot replace a broken
	// higher-priority explicit authority.
	t.Setenv("STACKKITS_REPO", t.TempDir())
	t.Setenv("TECHSTACK_STACKKITS_DIR", filepath.Join(t.TempDir(), "missing-authority"))
	broken := newServer()
	for _, path := range []string{"/api/v1/stackkits", "/api/v1/stackkits/" + authority[0].Metadata.Name} {
		var failure struct {
			Data  json.RawMessage `json:"data"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if status := get(broken, path, &failure); status != http.StatusInternalServerError || failure.Error.Code == "" || len(failure.Data) > 0 && string(failure.Data) != "null" {
			t.Fatalf("broken explicit catalog authority did not return a structured failure: status=%d", status)
		}
	}
}
