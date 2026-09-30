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

// A stored self-disclosure decides the operator profile of that principal's
// recommendations, and of nobody else's.
func TestSelfDisclosureDrivesOnlyTheOwnersRecommendationProfile(t *testing.T) {
	store := controlplane.NewMemoryStore()
	put := selfDisclosureCall(t, store, http.MethodPut, "owner-1",
		`{"source":"cloud","disclosure":{"experience":"guided","goals":["photos"],"placement":"home","household":"household"}}`)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", put.Code, put.Body.String())
	}

	own := wizardRecommendationData(t, wizardRecommendationRequestAs(t, store, "owner-1", `{"goals":["photos"],"services":[],"deployment_lane":"self-hosted","surface":"easy"}`))
	if own.DecisionContext == nil || own.DecisionContext.Operator == nil || own.DecisionContext.Operator.Band != "guided" {
		t.Fatalf("owner recommendation operator = %#v, want the disclosed guided band", own.DecisionContext)
	}

	other := wizardRecommendationData(t, wizardRecommendationRequestAs(t, store, "owner-2", `{"goals":["photos"],"services":[],"deployment_lane":"self-hosted","surface":"easy"}`))
	if operator := other.DecisionContext.Operator; operator != nil && operator.Source == "self-disclosure" {
		t.Fatalf("another owner inherited the disclosure: %#v", operator)
	}
	var otherRead struct {
		Data core.OperatorSelfDisclosureRecord `json:"data"`
	}
	if err := json.Unmarshal(selfDisclosureCall(t, store, http.MethodGet, "owner-2", "").Body.Bytes(), &otherRead); err != nil {
		t.Fatal(err)
	}
	if !otherRead.Data.Disclosure.IsEmpty() || otherRead.Data.Profile != nil {
		t.Fatalf("another owner read the disclosure: %#v", otherRead.Data)
	}
}

// The score is derived server-side; a client-supplied score or an answer
// outside the closed vocabulary is refused instead of being stored.
func TestSelfDisclosureRejectsClientScoresAndUnknownAnswers(t *testing.T) {
	store := controlplane.NewMemoryStore()
	for _, body := range []string{
		`{"source":"cloud","disclosure":{"experience":"expert"},"score":10}`,
		`{"source":"cloud","disclosure":{"experience":"wizard-level-11"}}`,
	} {
		if recorder := selfDisclosureCall(t, store, http.MethodPut, "owner-1", body); recorder.Code != http.StatusBadRequest {
			t.Fatalf("PUT %s status = %d, want 400", body, recorder.Code)
		}
	}
	if _, err := store.GetOperatorSelfDisclosure(t.Context(), "tenant-1", "owner-1"); err == nil {
		t.Fatal("a rejected self-disclosure was stored")
	}
}

func selfDisclosureCall(t *testing.T, store *controlplane.MemoryStore, method, owner, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := httpx.NewRouter()
	if err := RegisterUnifierRoutes(router, store); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, "/api/v1/operator/self-disclosure", bytes.NewBufferString(body))
	request = request.WithContext(identity.NewContext(request.Context(), &identity.Identity{UserID: owner, OrgID: "tenant-1"}))
	recorder := httptest.NewRecorder()
	router.BuildMux().ServeHTTP(recorder, request)
	return recorder
}
