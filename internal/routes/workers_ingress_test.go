package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// endlessBody streams a never-ending JSON string and counts what the server
// actually pulled from it.
type endlessBody struct {
	prefix []byte
	read   int64
}

func (b *endlessBody) Read(p []byte) (int, error) {
	n := 0
	if len(b.prefix) > 0 {
		n = copy(p, b.prefix)
		b.prefix = b.prefix[n:]
	}
	for ; n < len(p); n++ {
		p[n] = 'a'
	}
	b.read += int64(len(p))
	return len(p), nil
}

// An unauthenticated peer must not make the control plane buffer its body: a
// request without a credential is refused unread, and one with a garbage
// credential is cut off at the route bound (Guard finding F-04).
func TestWorkerRoutesRefuseOversizedUnauthenticatedBodiesWithoutBuffering(t *testing.T) {
	store := controlplane.NewMemoryStore()
	router := httpx.NewRouter()
	RegisterWorkerRoutesWithStore(router, WorkerRouteConfig{Store: store, Servers: store})
	prefix := `{"runtime_agent_id":"x","tenant_id":"t","known_commands":["`

	for _, tc := range []struct {
		name, path, authorization string
		wantRead                  int64
	}{
		{"no credential", "/api/v1/workers/x/commands/next", "", 64 << 10},
		{"garbage credential", "/api/v1/workers/x/heartbeat", "Bearer tsra.not-a-real-credential", workerHeartbeatBodyLimit + 128<<10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &endlessBody{prefix: []byte(prefix)}
			req := httptest.NewRequest(http.MethodPost, tc.path, body)
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			if recorder.Code < 400 || recorder.Code >= 500 {
				t.Fatalf("status = %d, want a 4xx refusal; body=%s", recorder.Code, recorder.Body.String())
			}
			if body.read > tc.wantRead {
				t.Fatalf("server consumed %d bytes of an unauthenticated body, bound is %d", body.read, tc.wantRead)
			}
		})
	}
}
