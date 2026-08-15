package routes

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/workerauth"
)

const testWorkerCredentialSecret = "test-worker-credential-secret"

func TestWorkerConnectIdempotentReplayDoesNotRotateCredential(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", testWorkerCredentialSecret)
	store := controlplane.NewMemoryStore()
	handler := workerRouteHandlers{wst: store, serverStore: store}
	body := `{"server_id":"server-1","runtime_agent_id":"runtime-1","stack_id":"stack-1","hostname":"NODE-1","os":"Ubuntu","arch":"AMD64","mode":"advanced","connection_mode":"ssh","provider":"local"}`

	connect := func(key, requestBody string) (int, map[string]any) {
		t.Helper()
		event, recorder := workerRouteTestEvent(http.MethodPost, "/v1/ril/servers/connect", requestBody)
		event.Request.Header.Set("Idempotency-Key", key)
		event.Request = event.Request.WithContext(identity.NewContext(
			event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"},
		))
		if err := handler.connectServer(event); err != nil {
			t.Fatalf("connectServer: %v", err)
		}
		var envelope struct {
			Data map[string]any `json:"data"`
		}
		_ = json.Unmarshal(recorder.Body.Bytes(), &envelope)
		return recorder.Code, envelope.Data
	}

	status, first := connect("connect-attempt-0001", body)
	if status != http.StatusOK {
		t.Fatalf("initial connect status = %d", status)
	}
	firstToken, _ := first["agent_token"].(string)
	if !workerauth.IsOpaqueToken(firstToken) || first["credential_generation"] != float64(1) {
		t.Fatalf("initial credential = %#v", first)
	}

	status, replay := connect(
		"connect-attempt-0001",
		`{"server_id":"server-1","runtime_agent_id":"runtime-1","stack_id":"stack-1","hostname":"node-1","os":"ubuntu","arch":"amd64","mode":"advanced","connection_mode":"ssh","provider":"local"}`,
	)
	if status != http.StatusOK || replay["agent_token"] != firstToken ||
		replay["credential_generation"] != float64(1) {
		t.Fatalf("same semantic request did not replay exact credential: status=%d response=%#v", status, replay)
	}

	status, _ = connect("connect-attempt-0002", body)
	if status != http.StatusConflict {
		t.Fatalf("different key status = %d, want 409", status)
	}
	status, _ = connect(
		"connect-attempt-0001",
		`{"server_id":"server-1","runtime_agent_id":"runtime-1","stack_id":"stack-1","hostname":"other-node","os":"ubuntu","arch":"amd64","mode":"advanced","connection_mode":"ssh","provider":"local"}`,
	)
	if status != http.StatusConflict {
		t.Fatalf("different request status = %d, want 409", status)
	}

	worker, err := store.GetWorker(t.Context(), "tenant-1", "runtime-1")
	if err != nil {
		t.Fatal(err)
	}
	state, err := controlplane.WorkerCredentialStateFromWorker(*worker)
	if err != nil || state.Generation != 1 || state.TokenSHA256 != workerauth.SHA256Hex(firstToken) {
		t.Fatalf("connect rotated or corrupted credential: state=%+v err=%v", state, err)
	}
}

func TestWorkerConnectRequiresExactlyOneBoundedIdempotencyKey(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", testWorkerCredentialSecret)
	for _, test := range []struct {
		name   string
		values []string
	}{
		{name: "missing"},
		{name: "too short", values: []string{"short"}},
		{name: "duplicate", values: []string{"connect-attempt-0001", "connect-attempt-0002"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := controlplane.NewMemoryStore()
			event, recorder := workerRouteTestEvent(
				http.MethodPost, "/v1/ril/servers/connect",
				`{"server_id":"server-1","runtime_agent_id":"runtime-1","stack_id":"stack-1"}`,
			)
			event.Request.Header.Del("Idempotency-Key")
			for _, value := range test.values {
				event.Request.Header.Add("Idempotency-Key", value)
			}
			event.Request = event.Request.WithContext(identity.NewContext(
				event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"},
			))
			if err := (workerRouteHandlers{wst: store, serverStore: store}).connectServer(event); err != nil {
				t.Fatalf("connectServer: %v", err)
			}
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d body=%s, want 400", recorder.Code, recorder.Body.String())
			}
			workers, err := store.ListWorkersByTenant(t.Context(), "tenant-1")
			if err != nil || len(workers) != 0 {
				t.Fatalf("invalid header mutated workers: workers=%#v err=%v", workers, err)
			}
		})
	}
}

func TestWorkerCredentialConcurrentSameKeyReturnsOneValidToken(t *testing.T) {
	store, handler, request := workerCredentialConcurrencyFixture(t)
	const callers = 32
	tokens := make(chan string, callers)
	errs := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := handler.ensureWorkerCredential(t.Context(), request)
			if err != nil {
				errs <- err
				return
			}
			tokens <- result.Token
		}()
	}
	group.Wait()
	close(tokens)
	close(errs)
	for err := range errs {
		t.Fatalf("same-key concurrent issuance failed: %v", err)
	}
	var expected string
	count := 0
	for token := range tokens {
		count++
		if expected == "" {
			expected = token
		}
		if token != expected || !workerauth.IsOpaqueToken(token) {
			t.Fatalf("same-key tokens differ or are invalid: got=%q expected=%q", token, expected)
		}
	}
	if count != callers {
		t.Fatalf("returned tokens = %d, want %d", count, callers)
	}
	worker, err := store.GetWorker(t.Context(), request.TenantID, request.RuntimeAgentID)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := controlplane.WorkerCredentialStateFromWorker(*worker)
	if state.Generation != 1 || state.TokenSHA256 != workerauth.SHA256Hex(expected) {
		t.Fatalf("same-key durable state = %+v", state)
	}
}

func TestWorkerCredentialConcurrentDifferentKeysHasExactlyOneWinner(t *testing.T) {
	store, handler, base := workerCredentialConcurrencyFixture(t)
	const callers = 32
	var successes atomic.Int32
	var conflicts atomic.Int32
	var unexpected atomic.Int32
	var winningTokenMu sync.Mutex
	winningToken := ""
	var group sync.WaitGroup
	for index := range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			request := base
			request.IdempotencyKey = "different-attempt-" + leftPadInteger(index, 4)
			result, err := handler.ensureWorkerCredential(t.Context(), request)
			switch {
			case err == nil:
				successes.Add(1)
				winningTokenMu.Lock()
				winningToken = result.Token
				winningTokenMu.Unlock()
			case errors.Is(err, controlplane.ErrConflict):
				conflicts.Add(1)
			default:
				unexpected.Add(1)
			}
		}()
	}
	group.Wait()
	if successes.Load() != 1 || conflicts.Load() != callers-1 || unexpected.Load() != 0 {
		t.Fatalf("different-key results: success=%d conflict=%d unexpected=%d",
			successes.Load(), conflicts.Load(), unexpected.Load())
	}
	worker, err := store.GetWorker(t.Context(), base.TenantID, base.RuntimeAgentID)
	if err != nil {
		t.Fatal(err)
	}
	state, _ := controlplane.WorkerCredentialStateFromWorker(*worker)
	if state.Generation != 1 || winningToken == "" ||
		state.TokenSHA256 != workerauth.SHA256Hex(winningToken) {
		t.Fatalf("winner was not the one durable credential: state=%+v token=%q", state, winningToken)
	}
}

func TestWorkerCredentialRotationReplaysAndRejectsStaleGeneration(t *testing.T) {
	t.Setenv("TECHSTACK_WORKER_AGENT_TOKEN_SECRET", testWorkerCredentialSecret)
	store := controlplane.NewMemoryStore()
	handler := workerRouteHandlers{wst: store, serverStore: store}
	if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
		ID: "runtime-1", TenantID: "tenant-1", StackID: "stack-1",
		OwnerSubjectID: "owner-1", Approved: true,
		Capabilities: map[string]any{"server_id": "server-1", "runtime_agent_id": "runtime-1"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertServerRuntime(t.Context(), controlplane.ServerRuntime{
		ID: "server-1", TenantID: "tenant-1", StackID: "stack-1",
		OwnerSubjectID: "owner-1", WorkerID: "runtime-1", NodeID: "server-1",
		LifecycleState: string(serverregistry.LifecycleEnrolling),
	}); err != nil {
		t.Fatal(err)
	}
	digest, _ := workerConnectRequestDigest("tenant-1", "owner-1", "server-1", "runtime-1", workerConnectRequest{
		ServerID: "server-1", RuntimeAgentID: "runtime-1", StackID: "stack-1",
		Hostname: "node-1",
	})
	initial, err := handler.ensureWorkerCredential(t.Context(), workerCredentialRequest{
		TenantID: "tenant-1", OwnerID: "owner-1", StackID: "stack-1",
		ServerID: "server-1", RuntimeAgentID: "runtime-1",
		IdempotencyKey: "initial-attempt-0001", RequestDigest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}

	rotate := func(key string, expected int64) (int, map[string]any) {
		t.Helper()
		event, recorder := workerRouteTestEvent(
			http.MethodPost, "/v1/ril/servers/server-1/credential/rotate",
			`{"expected_credential_generation":`+integerString(expected)+`}`,
		)
		event.Request.SetPathValue("id", "server-1")
		event.Request.Header.Set("Idempotency-Key", key)
		event.Request = event.Request.WithContext(identity.NewContext(
			event.Request.Context(), &identity.Identity{UserID: "owner-1", OrgID: "tenant-1"},
		))
		if routeErr := handler.rotateServerCredential(event); routeErr != nil {
			t.Fatalf("rotate route: %v", routeErr)
		}
		var envelope struct {
			Data map[string]any `json:"data"`
		}
		_ = json.Unmarshal(recorder.Body.Bytes(), &envelope)
		return recorder.Code, envelope.Data
	}

	status, rotated := rotate("rotation-attempt-0001", 1)
	rotatedToken, _ := rotated["agent_token"].(string)
	if status != http.StatusOK || rotatedToken == "" || rotatedToken == initial.Token ||
		rotated["credential_generation"] != float64(2) {
		t.Fatalf("rotation response: status=%d data=%#v", status, rotated)
	}
	status, replay := rotate("rotation-attempt-0001", 1)
	if status != http.StatusOK || replay["agent_token"] != rotatedToken ||
		replay["credential_generation"] != float64(2) {
		t.Fatalf("rotation replay: status=%d data=%#v", status, replay)
	}
	status, _ = rotate("rotation-attempt-0001", 2)
	if status != http.StatusConflict {
		t.Fatalf("same rotation key with a different request status = %d, want 409", status)
	}
	status, _ = rotate("rotation-attempt-0002", 1)
	if status != http.StatusConflict {
		t.Fatalf("stale rotation status = %d, want 409", status)
	}
}

func workerCredentialConcurrencyFixture(t *testing.T) (*controlplane.MemoryStore, workerRouteHandlers, workerCredentialRequest) {
	t.Helper()
	store := controlplane.NewMemoryStore()
	if _, err := store.UpsertWorkerHeartbeat(t.Context(), controlplane.Worker{
		ID: "runtime-1", TenantID: "tenant-1", StackID: "stack-1",
		OwnerSubjectID: "owner-1",
		Capabilities:   map[string]any{"server_id": "server-1", "runtime_agent_id": "runtime-1"},
	}); err != nil {
		t.Fatal(err)
	}
	return store, workerRouteHandlers{
			wst: store, credentialSecret: []byte(testWorkerCredentialSecret),
		}, workerCredentialRequest{
			TenantID: "tenant-1", OwnerID: "owner-1", StackID: "stack-1",
			ServerID: "server-1", RuntimeAgentID: "runtime-1",
			IdempotencyKey: "concurrent-attempt-0001",
			RequestDigest:  "c5f6f126f491f4ef6687a36a2f16e56dfadf5bb8a507417d76c0d9c29c56f1bd",
		}
}

func leftPadInteger(value, width int) string {
	text := integerString(int64(value))
	for len(text) < width {
		text = "0" + text
	}
	return text
}

func integerString(value int64) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	out := make([]byte, 0, 20)
	for value > 0 {
		out = append(out, digits[value%10])
		value /= 10
	}
	for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
		out[left], out[right] = out[right], out[left]
	}
	return string(out)
}
