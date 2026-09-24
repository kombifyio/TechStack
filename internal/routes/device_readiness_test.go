package routes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/httpx"
)

// Two things about this surface are security decisions rather than behaviour,
// and both are easy to undo by accident: whether the routes exist at all on a
// deployment that cannot reach the operator's machines, and whether an
// unanswerable entitlement question reads as a yes.

func readinessRouter(t *testing.T, cfg DeviceReadinessRouteConfig) *httpx.Router {
	t.Helper()
	router := httpx.NewRouter()
	RegisterDeviceReadinessRoutes(router, cfg)
	return router
}

func postReadiness(t *testing.T, router *httpx.Router, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestReadinessRoutesDoNotExistOnADeploymentThatCannotReachTheDevice(t *testing.T) {
	// A control plane somewhere else cannot reach a machine in someone's home,
	// and one that lent devices a way onto the network would be a relay rather
	// than a repair. The routes are therefore absent, not merely refusing.
	router := readinessRouter(t, DeviceReadinessRouteConfig{
		LocalDeployment: false,
		Entitled:        func(context.Context, string, string) (bool, error) { return true, nil },
	})

	for _, path := range []string{"/api/v1/device-readiness/probe", "/api/v1/device-readiness/prepare"} {
		if got := postReadiness(t, router, path, `{}`).Code; got != http.StatusNotFound {
			t.Errorf("%s answered %d on a hosted deployment; the route should not exist there", path, got)
		}
	}
}

func TestReadinessRoutesExistWhereTheControlPlaneIsWithTheOperator(t *testing.T) {
	router := readinessRouter(t, DeviceReadinessRouteConfig{
		LocalDeployment: true,
		Entitled:        func(context.Context, string, string) (bool, error) { return true, nil },
	})

	for _, path := range []string{"/api/v1/device-readiness/probe", "/api/v1/device-readiness/prepare"} {
		if got := postReadiness(t, router, path, `{}`).Code; got == http.StatusNotFound {
			t.Errorf("%s does not exist on a local deployment", path)
		}
	}
}

func TestAnUnanswerableEntitlementQuestionIsNotAYes(t *testing.T) {
	// Running commands on someone's machine is not something to fall back to
	// when the entitlement source is unreachable or absent.
	cases := map[string]DeviceReadinessRouteConfig{
		"no entitlement source": {LocalDeployment: true, Entitled: nil},
		"source failed": {LocalDeployment: true, Entitled: func(context.Context, string, string) (bool, error) {
			return false, errors.New("unreachable")
		}},
		"not entitled": {LocalDeployment: true, Entitled: func(context.Context, string, string) (bool, error) {
			return false, nil
		}},
	}

	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			router := readinessRouter(t, cfg)
			recorder := postReadiness(t, router, "/api/v1/device-readiness/probe", `{"connection":{"host":"h","user":"u"}}`)

			// Unauthenticated is the honest answer before an owner is known;
			// what must never happen is the request reaching a device.
			if recorder.Code == http.StatusOK {
				t.Fatalf("an unentitled request succeeded: %s", recorder.Body.String())
			}
			if recorder.Code >= 500 {
				t.Fatalf("refusal came out as a server error (%d): %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}
