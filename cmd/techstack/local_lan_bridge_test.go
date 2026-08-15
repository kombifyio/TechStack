package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalLANBridgeExposesOnlyGuardRoutesToPrivatePeers(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, tc := range []struct {
		method, path, remote string
		want                 int
	}{
		{http.MethodGet, "/install.sh", "192.168.1.20:4000", http.StatusNoContent},
		{http.MethodPost, "/api/v1/workers/register", "10.0.0.20:4000", http.StatusNoContent},
		{http.MethodGet, "/", "192.168.1.20:4000", http.StatusNotFound},
		{http.MethodPost, "/api/v1/auth/device-session", "192.168.1.20:4000", http.StatusNotFound},
		{http.MethodPost, "/api/v1/workers/worker-1/approve", "192.168.1.20:4000", http.StatusNotFound},
		{http.MethodGet, "/install.sh", "203.0.113.20:4000", http.StatusNotFound},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.RemoteAddr = tc.remote
		res := httptest.NewRecorder()
		(localLANBridgeHandler{next: next}).ServeHTTP(res, req)
		if res.Code != tc.want {
			t.Fatalf("%s %s from %s status=%d want=%d", tc.method, tc.path, tc.remote, res.Code, tc.want)
		}
	}
}
