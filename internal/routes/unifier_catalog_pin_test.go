package routes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

func TestUseCaseCatalogHTTPServesPinnedCatalogWithoutCheckout(t *testing.T) {
	t.Setenv("STACKKITS_REPO", "")
	t.Setenv("TECHSTACK_STACKKITS_DIR", "")
	t.Setenv("STACKKITS_PATH", "")
	t.Setenv("TECHSTACK_STACKKIT_USE_CASE_CATALOG", "")

	recorder := unifierCatalogRequest(t, http.MethodGet, "/api/v1/stackkits/use-cases", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data struct {
			Configured bool `json:"configured"`
			Release    struct {
				Tag     string `json:"tag"`
				Version string `json:"version"`
			} `json:"release"`
			UseCases []struct {
				ID           string `json:"id"`
				ComputeTiers map[string]struct {
					Included bool `json:"included"`
				} `json:"compute_tiers"`
			} `json:"use_cases"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.Data.Configured || envelope.Data.Release.Tag == "" {
		t.Fatalf("catalog was not served from the pin: %#v", envelope.Data)
	}
	var mediaLow *bool
	for _, useCase := range envelope.Data.UseCases {
		if useCase.ID != "media" {
			continue
		}
		if fit, ok := useCase.ComputeTiers["low"]; ok {
			included := fit.Included
			mediaLow = &included
		}
	}
	if mediaLow == nil || *mediaLow {
		t.Fatal("pinned catalog HTTP path did not report media omitted on low")
	}
}

func TestAnalyzeHTTPOmitsMediaOnLowWithoutCheckout(t *testing.T) {
	t.Setenv("STACKKITS_REPO", "")
	t.Setenv("TECHSTACK_STACKKITS_DIR", "")
	t.Setenv("STACKKITS_PATH", "")
	t.Setenv("TECHSTACK_STACKKIT_USE_CASE_CATALOG", "")

	body := `{"name":"catalog-pin","metadata":{"compute_tier":"low","use_cases":"media"},"services":[{"name":"jellyfin","type":"media"}]}`
	recorder := unifierCatalogRequest(t, http.MethodPost, "/api/v1/unifier/analyze", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data core.RequirementsSpec `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.DecisionTrace == nil {
		t.Fatal("analyze omitted decision trace")
	}
	found := false
	for _, gap := range envelope.Data.DecisionTrace.BlockingGaps {
		if gap.Code == "use_case_omitted_on_compute_tier" && gap.Blocking {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("analyze did not block media on low from the pinned catalog: %#v", envelope.Data.DecisionTrace.BlockingGaps)
	}
}

func unifierCatalogRequest(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := httpx.NewRouter()
	if err := RegisterUnifierRoutes(router, controlplane.NewMemoryStore()); err != nil {
		t.Fatal(err)
	}
	var request *http.Request
	if body == "" {
		request = httptest.NewRequest(method, path, nil)
	} else {
		request = httptest.NewRequest(method, path, bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
	}
	request = request.WithContext(identity.NewContext(request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"}))
	recorder := httptest.NewRecorder()
	router.BuildMux().ServeHTTP(recorder, request)
	return recorder
}
